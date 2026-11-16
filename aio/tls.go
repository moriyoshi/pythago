package aio

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/moriyoshi/pythago/py"
)

// TLS on Go's side for GoEventLoop (goaio_tls.py decides when): Python describes an
// ssl.SSLContext as a dict (what the context was configured with, recorded by a patch of
// ssl.SSLContext), and Go's crypto/tls does the handshake and the records, so no TLS byte
// passes through Python. Verification follows Python's context, not Go's defaults: the
// trust anchors are what the context loaded (cafile, capath, cadata, the default verify
// paths), and a failure is reported with OpenSSL's verify code and message.

// OpenSSL X509_V_ERR codes Python reports in SSLCertVerificationError.verify_code.
const (
	verifyCertNotYetValid      = 9
	verifyCertHasExpired       = 10
	verifySelfSigned           = 18
	verifySelfSignedInChain    = 19
	verifyUnableToGetIssuer    = 20
	verifyInvalidPurpose       = 26
	verifyHostnameMismatch     = 62
	verifyUnspecified          = 1
	tlsErrorBase               = -1000 // evError n: tlsErrorBase - verify code; tlsHandshakeErr: other TLS failures
	tlsHandshakeErr            = -999
	pyCertNone, pyCertRequired = 0, 2
)

// verifyError is a certificate verification failure, as OpenSSL would report it.
type verifyError struct {
	code int
	msg  string
}

func (e *verifyError) Error() string { return e.msg }

// tlsSpec is goaio_tls.go_config's dict.
type tlsSpec struct {
	server         bool
	serverName     string
	verifyMode     int64
	checkHostname  bool
	checkCN        bool
	minVersion     uint16
	maxVersion     uint16
	cipherSuites   []uint16
	curves         []tls.CurveID
	alpn           []string
	keyLog         string
	noTickets      bool
	certs          [][2]string // (certfile, keyfile)
	caFiles        []string
	caDirs         []string
	caData         [][]byte // PEM text or DER, one or more certificates each
	handshakeLimit time.Duration
}

func parseTLSSpec(o *py.Object) (*tlsSpec, error) {
	v, err := o.Go()
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[any]any)
	if !ok {
		return nil, errors.New("goaio: TLS spec must be a dict")
	}
	s := &tlsSpec{}
	str := func(k string) string { x, _ := m[k].(string); return x }
	num := func(k string) int64 { x, _ := m[k].(int64); return x }
	flag := func(k string) bool { x, _ := m[k].(bool); return x }
	strs := func(k string) []string {
		var out []string
		for _, x := range asList(m[k]) {
			if t, ok := x.(string); ok {
				out = append(out, t)
			}
		}
		return out
	}
	s.server, s.serverName = flag("server_side"), str("server_hostname")
	s.verifyMode, s.checkHostname, s.checkCN = num("verify_mode"), flag("check_hostname"), flag("check_common_name")
	s.minVersion, s.maxVersion = uint16(num("min_version")), uint16(num("max_version"))
	for _, n := range strs("ciphers") {
		if id, ok := cipherIDs[n]; ok {
			s.cipherSuites = append(s.cipherSuites, id)
		}
	}
	for _, n := range strs("curves") {
		c, ok := curveIDs[n]
		if !ok {
			return nil, fmt.Errorf("goaio: curve %q", n)
		}
		s.curves = append(s.curves, c)
	}
	s.alpn, s.keyLog, s.noTickets = strs("alpn"), str("keylog"), flag("no_tickets")
	for _, x := range asList(m["certs"]) {
		pair := asList(x)
		if len(pair) != 2 {
			return nil, errors.New("goaio: certs must be (certfile, keyfile) pairs")
		}
		cert, _ := pair[0].(string)
		key, _ := pair[1].(string)
		s.certs = append(s.certs, [2]string{cert, key})
	}
	s.caFiles, s.caDirs = strs("cafiles"), strs("capaths")
	for _, x := range asList(m["cadata"]) {
		switch d := x.(type) {
		case string:
			s.caData = append(s.caData, []byte(d))
		case []byte:
			s.caData = append(s.caData, d)
		}
	}
	s.handshakeLimit = time.Duration(num("handshake_timeout_ms")) * time.Millisecond
	return s, nil
}

func asList(v any) []any {
	l, _ := v.([]any) // lists and tuples alike
	return l
}

// roots loads the trust anchors the context loaded: PEM files, OpenSSL hash directories
// (every certificate in them), and cadata (PEM text or concatenated DER).
func (s *tlsSpec) roots() (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	addPEM := func(b []byte) {
		for {
			var blk *pem.Block
			blk, b = pem.Decode(b)
			if blk == nil {
				return
			}
			if blk.Type == "CERTIFICATE" || blk.Type == "TRUSTED CERTIFICATE" {
				if c, err := x509.ParseCertificate(blk.Bytes); err == nil {
					pool.AddCert(c)
				}
			}
		}
	}
	for _, f := range s.caFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		addPEM(b)
	}
	for _, d := range s.caDirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue // OpenSSL ignores a missing capath until it looks something up
		}
		for _, e := range entries {
			if b, err := os.ReadFile(filepath.Join(d, e.Name())); err == nil {
				addPEM(b)
			}
		}
	}
	for _, b := range s.caData {
		if certs, err := x509.ParseCertificates(b); err == nil && len(certs) > 0 {
			for _, c := range certs {
				pool.AddCert(c)
			}
			continue
		}
		addPEM(b)
	}
	return pool, nil
}

// config builds the tls.Config. Verification is done here, the way OpenSSL does it for
// Python's context, rather than by crypto/tls's defaults.
func (s *tlsSpec) config() (*tls.Config, error) {
	cfg := &tls.Config{
		ServerName:             s.serverName,
		MinVersion:             s.minVersion,
		MaxVersion:             s.maxVersion,
		CipherSuites:           s.cipherSuites,
		CurvePreferences:       s.curves,
		NextProtos:             s.alpn,
		SessionTicketsDisabled: s.noTickets,
	}
	for _, c := range s.certs {
		key := c[1]
		if key == "" {
			key = c[0] // certfile holds the key too
		}
		pair, err := tls.LoadX509KeyPair(c[0], key)
		if err != nil {
			return nil, err
		}
		cfg.Certificates = append(cfg.Certificates, pair)
	}
	if s.keyLog != "" {
		f, err := os.OpenFile(s.keyLog, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}
		cfg.KeyLogWriter = f
	}
	pool, err := s.roots()
	if err != nil {
		return nil, err
	}
	if s.server {
		cfg.ClientCAs = pool
		switch s.verifyMode {
		case pyCertNone:
			cfg.ClientAuth = tls.NoClientCert
		case pyCertRequired:
			cfg.ClientAuth = tls.RequireAnyClientCert
		default:
			cfg.ClientAuth = tls.RequestClientCert
		}
	}
	cfg.InsecureSkipVerify = true // verified below, as Python's context says
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if s.verifyMode == pyCertNone || len(cs.PeerCertificates) == 0 && s.server {
			return nil
		}
		if len(cs.PeerCertificates) == 0 {
			return &verifyError{verifyUnableToGetIssuer, "unable to get local issuer certificate"}
		}
		inter := x509.NewCertPool()
		for _, c := range cs.PeerCertificates[1:] {
			inter.AddCert(c)
		}
		usage := x509.ExtKeyUsageServerAuth
		if s.server {
			usage = x509.ExtKeyUsageClientAuth
		}
		opts := x509.VerifyOptions{Roots: pool, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{usage}}
		leaf := cs.PeerCertificates[0]
		if _, err := leaf.Verify(opts); err != nil {
			return toVerifyError(err, cs.PeerCertificates)
		}
		if !s.server && s.checkHostname && s.serverName != "" {
			if err := leaf.VerifyHostname(s.serverName); err != nil && !(s.checkCN && commonNameMatches(leaf, s.serverName)) {
				return &verifyError{verifyHostnameMismatch, fmt.Sprintf("Hostname mismatch, certificate is not valid for '%s'.", s.serverName)}
			}
		}
		return nil
	}
	return cfg, nil
}

// commonNameMatches is OpenSSL's fallback to the subject's common name, which Python
// enables by default (hostname_checks_common_name): only for a certificate without DNS
// names, never for an IP address, and with a wildcard only as the whole leftmost label
// followed by at least two labels (X509_CHECK_FLAG_NO_PARTIAL_WILDCARDS).
func commonNameMatches(leaf *x509.Certificate, host string) bool {
	if len(leaf.DNSNames) > 0 || net.ParseIP(host) != nil {
		return false
	}
	cn, host := strings.ToLower(leaf.Subject.CommonName), strings.ToLower(strings.TrimSuffix(host, "."))
	if cn == "" {
		return false
	}
	if rest, ok := strings.CutPrefix(cn, "*."); ok {
		_, hostRest, found := strings.Cut(host, ".")
		return strings.Count(rest, ".") >= 1 && found && hostRest == rest
	}
	return cn == host
}

// toVerifyError maps a crypto/x509 failure to OpenSSL's verify code and message.
func toVerifyError(err error, chain []*x509.Certificate) error {
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	switch {
	case errors.As(err, &ua):
		last := chain[len(chain)-1]
		if last.CheckSignatureFrom(last) == nil { // the chain ends in a self-signed root
			if len(chain) == 1 {
				return &verifyError{verifySelfSigned, "self-signed certificate"}
			}
			return &verifyError{verifySelfSignedInChain, "self-signed certificate in certificate chain"}
		}
		return &verifyError{verifyUnableToGetIssuer, "unable to get local issuer certificate"}
	case errors.As(err, &ci):
		switch ci.Reason {
		case x509.Expired:
			if time.Now().Before(ci.Cert.NotBefore) {
				return &verifyError{verifyCertNotYetValid, "certificate is not yet valid"}
			}
			return &verifyError{verifyCertHasExpired, "certificate has expired"}
		case x509.IncompatibleUsage:
			return &verifyError{verifyInvalidPurpose, "unsupported certificate purpose"}
		}
	}
	return &verifyError{verifyUnspecified, err.Error()}
}

// tlsFailure posts a failed handshake to token: a verification failure as tlsErrorBase
// minus OpenSSL's code, a network error or the handshake timeout as an errno, anything
// else (alerts, protocol errors) as tlsHandshakeErr.
func tlsFailure(l *aioLoop, token int64, err error) {
	var ve *verifyError
	var opErr *net.OpError
	switch {
	case errors.As(err, &ve):
		l.post(completion{token: token, kind: evError, n: tlsErrorBase - int64(ve.code), msg: ve.msg})
	case errors.Is(err, context.DeadlineExceeded):
		l.post(completion{token: token, kind: evError, n: int64(syscall.ETIMEDOUT), msg: "SSL handshake timed out"})
	case errors.As(err, &opErr):
		errno, msg := errnoOf(err)
		l.post(completion{token: token, kind: evError, n: errno, msg: msg})
	default:
		l.post(completion{token: token, kind: evError, n: tlsHandshakeErr, msg: err.Error()})
	}
}

func handshake(c *tls.Conn, limit time.Duration) error {
	ctx := context.Background()
	if limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	return c.HandshakeContext(ctx)
}

// tlsState is what goaio's ssl_object reports: the version and cipher by OpenSSL's names,
// the cipher's secret bits, the ALPN protocol, and the peer's chain (DER).
func tlsState(c net.Conn) any {
	tc, ok := c.(*tls.Conn)
	if !ok {
		return nil
	}
	cs := tc.ConnectionState()
	version := map[uint16]string{tls.VersionTLS10: "TLSv1", tls.VersionTLS11: "TLSv1.1",
		tls.VersionTLS12: "TLSv1.2", tls.VersionTLS13: "TLSv1.3"}[cs.Version]
	name := tls.CipherSuiteName(cs.CipherSuite)
	if n, ok := cipherOpenSSLNames[cs.CipherSuite]; ok {
		name = n
	}
	chain := make([]any, len(cs.PeerCertificates))
	for i, c := range cs.PeerCertificates {
		chain[i] = c.Raw
	}
	return py.Tuple{version, name, cipherBits[cs.CipherSuite], cs.NegotiatedProtocol, chain}
}

// TLS 1.2 suites crypto/tls has, by OpenSSL's names (TLS 1.3 names are the same in both;
// Go always enables all three TLS 1.3 suites). RSA key exchange is left out: Go only
// offers it with GODEBUG=tlsrsakex=1.
var cipherOpenSSLNames = map[uint16]string{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256:       "ECDHE-ECDSA-AES128-GCM-SHA256",
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256:         "ECDHE-RSA-AES128-GCM-SHA256",
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384:       "ECDHE-ECDSA-AES256-GCM-SHA384",
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384:         "ECDHE-RSA-AES256-GCM-SHA384",
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256: "ECDHE-ECDSA-CHACHA20-POLY1305",
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256:   "ECDHE-RSA-CHACHA20-POLY1305",
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256:       "ECDHE-ECDSA-AES128-SHA256",
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256:         "ECDHE-RSA-AES128-SHA256",
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA:          "ECDHE-ECDSA-AES128-SHA",
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA:            "ECDHE-RSA-AES128-SHA",
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA:          "ECDHE-ECDSA-AES256-SHA",
	tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA:            "ECDHE-RSA-AES256-SHA",
}

var cipherIDs = func() map[string]uint16 {
	m := map[string]uint16{}
	for id, n := range cipherOpenSSLNames {
		m[n] = id
	}
	return m
}()

var cipherBits = map[uint16]int{
	tls.TLS_AES_128_GCM_SHA256:                        128,
	tls.TLS_AES_256_GCM_SHA384:                        256,
	tls.TLS_CHACHA20_POLY1305_SHA256:                  256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256:       128,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256:         128,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384:       256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384:         256,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256: 256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256:   256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256:       128,
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256:         128,
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA:          128,
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA:            128,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA:          256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA:            256,
}

// curveIDs are OpenSSL's names for the groups set_ecdh_curve takes.
var curveIDs = map[string]tls.CurveID{
	"prime256v1": tls.CurveP256, "secp256r1": tls.CurveP256, "P-256": tls.CurveP256,
	"secp384r1": tls.CurveP384, "P-384": tls.CurveP384,
	"secp521r1": tls.CurveP521, "P-521": tls.CurveP521,
	"X25519": tls.X25519,
}

// tlsModule adds the TLS functions to _goaio.
func tlsModule(def func(string, fn), loopOf func(*py.Object) *aioLoop, connOf func(*py.Object) *aioConn) {
	// dial_tls(loop, token, host, port, spec) → evConnected with a TLS connection whose
	// handshake is done, or evError (tlsErrorBase - verify code, tlsHandshakeErr, errno).
	def("dial_tls", func(a []*py.Object) any {
		l, token, host, port := loopOf(a[0]), intArg(a, 1), strArg(a, 2), intArg(a, 3)
		spec, err := parseTLSSpec(a[4])
		if err != nil {
			panic(err)
		}
		cfg, err := spec.config()
		if err != nil {
			return oserror(err)
		}
		go func() {
			c, err := net.Dial("tcp", net.JoinHostPort(host, strconv.FormatInt(port, 10)))
			if err != nil {
				errno, msg := errnoOf(err)
				l.post(completion{token: token, kind: evError, n: errno, msg: msg})
				return
			}
			tc := tls.Client(newNBConn(c), cfg)
			if err := handshake(tc, spec.handshakeLimit); err != nil {
				c.Close()
				tlsFailure(l, token, err)
				return
			}
			l.post(completion{token: token, kind: evConnected, n: newConn(l, tc)})
		}()
		return nil
	})
	// listen_tls(loop, token, host, port, backlog, spec) -> (listener, port): accepted
	// connections arrive (evAccepted) after their handshake; failed handshakes are dropped.
	def("listen_tls", func(a []*py.Object) any {
		l, token, host, port := loopOf(a[0]), intArg(a, 1), strArg(a, 2), intArg(a, 3)
		spec, err := parseTLSSpec(a[5])
		if err != nil {
			panic(err)
		}
		cfg, err := spec.config()
		if err != nil {
			return oserror(err)
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.FormatInt(port, 10)))
		if err != nil {
			return oserror(err)
		}
		h := register(ln)
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					if errors.Is(err, net.ErrClosed) {
						return
					}
					continue
				}
				go func() {
					tc := tls.Server(newNBConn(c), cfg)
					if err := handshake(tc, spec.handshakeLimit); err != nil {
						c.Close()
						return
					}
					l.post(completion{token: token, kind: evAccepted, n: newConn(l, tc)})
				}()
			}
		}()
		return py.Tuple{h, ln.Addr().(*net.TCPAddr).Port}
	})
	// tls_state(conn) -> (version, cipher, secret bits, alpn, [peer DER...]) or None.
	def("tls_state", func(a []*py.Object) any {
		if ac := connOf(a[0]); ac != nil {
			return tlsState(ac.c)
		}
		return nil
	})
}
