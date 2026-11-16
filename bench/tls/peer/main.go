// Command peer is the TLS server bench/tls/run.sh measures Python clients against: the
// same Go crypto/tls peer for every client configuration, on its own cores. It makes a
// throwaway CA and a localhost certificate (ECDSA P-256), writes the CA for the client,
// and serves one request per connection by its first byte:
//
//	'B' + 8-byte big-endian size: send that many bytes, then close (bulk download)
//	'E': echo everything back (request/response)
//	anything else, or nothing: close (handshake only)
//
// The same service without TLS listens on the next port, for the plaintext ceiling.
//
//	peer -addr 127.0.0.1:18600 -ca /path/ca.pem
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"flag"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"strconv"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18600", "listen address")
	caPath := flag.String("ca", "", "where to write the CA certificate (PEM)")
	flag.Parse()
	cert, caPEM := certificates()
	if err := os.WriteFile(*caPath, caPEM, 0o644); err != nil {
		log.Fatal(err)
	}
	ln, err := tls.Listen("tcp", *addr, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		log.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(*addr)
	p, _ := strconv.Atoi(port)
	plain, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p+1)))
	if err != nil {
		log.Fatal(err)
	}
	go serve(plain)
	serve(ln)
}

func serve(ln net.Listener) {
	payload := make([]byte, 256<<10)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			defer c.Close()
			var mode [1]byte
			if _, err := io.ReadFull(c, mode[:]); err != nil {
				return
			}
			switch mode[0] {
			case 'B':
				var n [8]byte
				if _, err := io.ReadFull(c, n[:]); err != nil {
					return
				}
				for left := int64(binary.BigEndian.Uint64(n[:])); left > 0; {
					chunk := payload[:min(left, int64(len(payload)))]
					if _, err := c.Write(chunk); err != nil {
						return
					}
					left -= int64(len(chunk))
				}
			case 'E':
				io.Copy(c, c)
			}
		}()
	}
}

func certificates() (tls.Certificate, []byte) {
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			log.Fatal(err)
		}
		return k
	}
	now := time.Now()
	caKey := key()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "bench CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		log.Fatal(err)
	}
	ca, _ = x509.ParseCertificate(caDER)
	leafKey := key()
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		log.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}
