// Command gencert writes a self-signed cert.pem / key.pem for the relay. The
// certificate's SAN is the fixed proto.ServerName, so clients pin to it and the
// relay's public IP can change freely. Copy cert.pem to the agent and client;
// keep key.pem only on the relay.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"minitunnel/internal/proto"
)

func main() {
	outDir := flag.String("out", ".", "output directory for cert.pem and key.pem")
	years := flag.Int("years", 5, "validity in years")
	flag.Parse()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		log.Fatalf("serial: %v", err)
	}

	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: proto.ServerName},
		DNSNames:              []string{proto.ServerName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(*years, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatalf("create certificate: %v", err)
	}

	certPath := filepath.Join(*outDir, "cert.pem")
	keyPath := filepath.Join(*outDir, "key.pem")

	certOut, err := os.Create(certPath)
	if err != nil {
		log.Fatalf("create %s: %v", certPath, err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		log.Fatalf("write cert: %v", err)
	}
	certOut.Close()

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		log.Fatalf("marshal key: %v", err)
	}
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		log.Fatalf("create %s: %v", keyPath, err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		log.Fatalf("write key: %v", err)
	}
	keyOut.Close()

	log.Printf("wrote %s and %s (SAN=%s, valid %d years)", certPath, keyPath, proto.ServerName, *years)
}
