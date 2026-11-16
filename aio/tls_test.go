package aio_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testPKI writes a CA, a server certificate (localhost, 127.0.0.1) and a client
// certificate with their keys into dir, and returns the paths.
func testPKI(t *testing.T, dir string) map[string]string {
	t.Helper()
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	write := func(name, typ string, der []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	now := time.Now()
	caKey := key()
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "pythago test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leaf := func(file, name string, serial int64, usage x509.ExtKeyUsage, dns []string, ips []net.IP) (string, string) {
		k := key()
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name},
			NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), DNSNames: dns, IPAddresses: ips,
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		kder, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		return write(file+".pem", "CERTIFICATE", der), write(file+".key", "PRIVATE KEY", kder)
	}
	srvCert, srvKey := leaf("server", "localhost", 2, x509.ExtKeyUsageServerAuth, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	cliCert, cliKey := leaf("client", "client", 3, x509.ExtKeyUsageClientAuth, nil, nil)
	cnCert, cnKey := leaf("cn-only", "localhost", 4, x509.ExtKeyUsageServerAuth, nil, nil) // no SANs at all
	// A bundle in certifi's cacert.pem format: comment headers before each certificate,
	// other roots around ours.
	var bundle []byte
	for i, der := range [][]byte{otherCA(t, "Other Root A"), caDER, otherCA(t, "Other Root B")} {
		c, _ := x509.ParseCertificate(der)
		bundle = fmt.Appendf(bundle, "\n# Issuer: CN=%s\n# Subject: CN=%s\n# Label: \"%s\"\n# Serial: %d\n# MD5 Fingerprint: 00:11\n",
			c.Issuer.CommonName, c.Subject.CommonName, c.Subject.CommonName, i)
		bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	bundlePath := filepath.Join(dir, "cacert.pem")
	if err := os.WriteFile(bundlePath, bundle, 0o644); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"ca": write("ca.pem", "CERTIFICATE", caDER), "server": srvCert, "server_key": srvKey,
		"client": cliCert, "client_key": cliKey, "bundle": bundlePath, "cn_only": cnCert, "cn_only_key": cnKey}
}

// otherCA is a self-signed root that signs nothing here.
func otherCA(t *testing.T, name string) []byte {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(100), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

const tlsScenarios = `
import ssl

def server_ctx(require_client=False, cert="server"):
    ctx = ssl.create_default_context(ssl.Purpose.CLIENT_AUTH)
    ctx.load_cert_chain(PKI[cert], PKI[cert + "_key"])
    ctx.set_alpn_protocols(["h2", "http/1.1"])
    if require_client:
        ctx.verify_mode = ssl.CERT_REQUIRED
        ctx.load_verify_locations(PKI["ca"])
    return ctx

def client_ctx(cafile=True, cert=False):
    ctx = ssl.create_default_context(cafile=PKI["ca"] if cafile else None)
    ctx.set_alpn_protocols(["h2", "http/1.1"])
    if cert:
        ctx.load_cert_chain(PKI["client"], PKI["client_key"])
    return ctx

class SubContext(ssl.SSLContext):  # a subclass: Python's ssl does its TLS
    pass

def sub_client_ctx():
    ctx = SubContext(ssl.PROTOCOL_TLS_CLIENT)
    ctx.load_verify_locations(PKI["ca"])
    return ctx

async def tls_echo(handler):
    seen = {}
    async def serve(reader, writer):
        obj = writer.get_extra_info("ssl_object")
        cert = writer.get_extra_info("peercert")
        seen["client_cn"] = dict(x[0] for x in cert["subject"])["commonName"] if cert else None
        seen["server_alpn"] = obj.selected_alpn_protocol()
        while data := await reader.read(65536):
            writer.write(data)
            await writer.drain()
        writer.close()
    return seen, serve

CERT = "server"

async def roundtrip(sctx, cctx, server_hostname="localhost", start_tls=False):
    seen, serve = await tls_echo(None)
    server = await asyncio.start_server(serve, "127.0.0.1", 0, ssl=sctx)
    port = server.sockets[0].getsockname()[1]
    try:
        if start_tls:
            reader, writer = await asyncio.open_connection("127.0.0.1", port)
            await writer.start_tls(cctx, server_hostname=server_hostname)
        else:
            reader, writer = await asyncio.open_connection("127.0.0.1", port, ssl=cctx,
                                                           server_hostname=server_hostname)
    except ssl.SSLCertVerificationError as e:
        server.close()
        return ("verify", e.verify_code, e.verify_message)
    obj = writer.get_extra_info("ssl_object")
    payload = bytes(range(256)) * 4096  # 1 MiB
    # Read while writing: a client that drains everything before it reads waits on the
    # echo server, which waits on the client (on plain TCP every loop deadlocks so, stock
    # asyncio's included; Python's ssl buffers its way out, Go's crypto/tls does not), and
    # only kernel buffers bigger than the payload let it finish.
    async def send():
        writer.write(payload)
        await writer.drain()
    sending = asyncio.ensure_future(send())
    got = await reader.readexactly(len(payload))
    await sending
    peer = writer.get_extra_info("peercert")
    result = {
        "echo": got == payload,
        "version": obj.version(),
        "alpn": obj.selected_alpn_protocol(),
        "peer_cn": dict(x[0] for x in peer["subject"])["commonName"] if peer else peer,
        "peer_san": peer.get("subjectAltName") if peer else None,
        "der_matches": obj.getpeercert(True) == ssl.PEM_cert_to_DER_cert(open(PKI[CERT]).read()),
        "cipher_protocol": writer.get_extra_info("cipher")[1],
        "transport": type(writer.transport).__name__,
    }
    writer.close()
    try:
        await writer.wait_closed()
    except (ssl.SSLError, ConnectionError):
        pass
    server.close()
    await server.wait_closed()
    result.update(seen)
    return result

async def step(name, coro):
    """One scenario, with its own bound: a timeout says which one hung, on which loop."""
    try:
        return await asyncio.wait_for(coro, 20)
    except TimeoutError:
        raise TimeoutError("TLS scenario %r on %s" % (name, type(asyncio.get_running_loop()).__name__)) from None

async def all_tls():
    out = {}
    out["default"] = await step("default", roundtrip(server_ctx(), client_ctx()))
    out["client_cert"] = await step("client_cert", roundtrip(server_ctx(require_client=True), client_ctx(cert=True)))
    out["hostname_mismatch"] = await step("hostname_mismatch", roundtrip(server_ctx(), client_ctx(), server_hostname="wrong.example"))
    out["untrusted"] = await step("untrusted", roundtrip(server_ctx(), client_ctx(cafile=False)))
    none = client_ctx(cafile=False)
    none.check_hostname = False
    none.verify_mode = ssl.CERT_NONE
    out["cert_none"] = await step("cert_none", roundtrip(server_ctx(), none))
    out["start_tls"] = await step("start_tls", roundtrip(server_ctx(), client_ctx(), start_tls=True))
    out["subclass"] = await step("subclass", roundtrip(server_ctx(), sub_client_ctx()))
    # certifi's three ways: cafile=certifi.where(), load_verify_locations(certifi.where())
    # on top of the default paths (httpx), and cadata=certifi.contents().
    out["certifi_cafile"] = await step("certifi_cafile", roundtrip(server_ctx(), ssl.create_default_context(cafile=PKI["bundle"])))
    ctx = ssl.create_default_context()
    ctx.load_verify_locations(PKI["bundle"])
    out["certifi_load"] = await step("certifi_load", roundtrip(server_ctx(), ctx))
    out["certifi_cadata"] = await step("certifi_cadata", roundtrip(server_ctx(), ssl.create_default_context(cadata=open(PKI["bundle"]).read())))
    # A certificate without SANs: OpenSSL falls back to the common name, as Python
    # enables by default; with hostname_checks_common_name off it does not.
    global CERT
    CERT = "cn_only"
    out["cn_only"] = await step("cn_only", roundtrip(server_ctx(cert="cn_only"), client_ctx()))
    no_cn = client_ctx()
    no_cn.hostname_checks_common_name = False
    out["cn_only_off"] = await step("cn_only_off", roundtrip(server_ctx(cert="cn_only"), no_cn))
    CERT = "server"
    return out

def tls_on(factory):
    return asyncio.run(asyncio.wait_for(all_tls(), 120), loop_factory=factory)

def strip(results):  # what may differ between implementations
    return {k: {kk: vv for kk, vv in v.items() if kk != "transport"} if isinstance(v, dict) else v
            for k, v in results.items()}

stock = tls_on(asyncio.new_event_loop)
assert stock["default"]["echo"] and stock["default"]["alpn"] == "h2", stock
assert stock["client_cert"]["client_cn"] == "client", stock
assert stock["hostname_mismatch"][:2] == ("verify", 62), stock
assert stock["untrusted"][0] == "verify", stock
assert stock["cn_only"]["echo"] and stock["cn_only"]["peer_san"] is None, stock["cn_only"]
assert stock["cn_only_off"][:2] == ("verify", 62), stock["cn_only_off"]
for k in ("certifi_cafile", "certifi_load", "certifi_cadata"):
    assert stock[k]["echo"] and stock[k]["peer_cn"] == "localhost", (k, stock[k])
assert stock["cert_none"]["peer_cn"] == {}, stock  # getpeercert() is {} unverified
for name, factory in LOOPS.items():
    got = tls_on(factory)
    assert strip(got) == strip(stock), (name, got, stock)
    if name == "GoEventLoop":  # which side did the TLS
        paths = {k: v["transport"] for k, v in got.items() if isinstance(v, dict)}
        assert paths == {"default": "_GoTLSTransport", "client_cert": "_GoTLSTransport",
                         "cert_none": "_GoTLSTransport", "start_tls": "_SSLProtocolTransport",
                         "subclass": "_SSLProtocolTransport", "certifi_cafile": "_GoTLSTransport",
                         "certifi_load": "_GoTLSTransport", "certifi_cadata": "_GoTLSTransport",
                         "cn_only": "_GoTLSTransport"}, paths
`

// TestTLS: TLS on the Go-backed loops gives what asyncio's own loop gives (verification
// codes included), with crypto/tls doing it where the context maps and Python's ssl
// otherwise.
func TestTLS(t *testing.T) {
	pki := testPKI(t, t.TempDir())
	run(t, fmt.Sprintf("PKI = %s\n", pyDict(pki))+tlsScenarios)
}

func pyDict(m map[string]string) string {
	s := "{"
	for k, v := range m {
		s += fmt.Sprintf("%q: %q, ", k, v)
	}
	return s + "}"
}

// TestCoalescedReads: data that is already readable arrives in one event, not one per
// read. A TLS read returns one record (16 KiB); a client that resumes after the peer
// filled its socket must get far larger chunks.
func TestCoalescedReads(t *testing.T) {
	pki := testPKI(t, t.TempDir())
	run(t, fmt.Sprintf("PKI = %s\n", pyDict(pki))+`
import ssl

SIZE = 4 << 20

class Sink(asyncio.Protocol):
    def __init__(self):
        self.sizes, self.done = [], asyncio.get_running_loop().create_future()
    def connection_made(self, transport):
        transport.pause_reading()  # let the peer fill the socket first
        asyncio.get_running_loop().call_later(0.3, transport.resume_reading)
    def data_received(self, data):
        self.sizes.append(len(data))
        if sum(self.sizes) >= SIZE:
            self.done.set_result(None)

async def coalesced():
    sctx = ssl.create_default_context(ssl.Purpose.CLIENT_AUTH)
    sctx.load_cert_chain(PKI["server"], PKI["server_key"])
    async def serve(reader, writer):
        writer.write(b"x" * SIZE)
        await writer.drain()
        await reader.read()
        writer.close()
    server = await asyncio.start_server(serve, "127.0.0.1", 0, ssl=sctx)
    port = server.sockets[0].getsockname()[1]
    loop = asyncio.get_running_loop()
    transport, sink = await loop.create_connection(
        Sink, "127.0.0.1", port, ssl=ssl.create_default_context(cafile=PKI["ca"]), server_hostname="localhost")
    assert type(transport).__name__ == "_GoTLSTransport", type(transport)
    await asyncio.wait_for(sink.done, 20)
    transport.close()
    server.close()
    return sink.sizes

sizes = asyncio.run(coalesced(), loop_factory=goaio.GoEventLoop)
assert sum(sizes) == SIZE, sum(sizes)
assert max(sizes) >= 64 << 10, ("largest chunk", max(sizes), "chunks", len(sizes))
`)
}
