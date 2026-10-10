package panel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/store"
)

// certificate loads the panel's certificate from dir, creating a
// self-signed ECDSA P-256 one on first use. The certificate never changes
// afterwards, so a fingerprint the owner checked once stays valid. hosts
// (addresses or names) become its subject alternative names.
func certificate(dir string, hosts []string) (tls.Certificate, string, error) {
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if errors.Is(err, fs.ErrNotExist) {
		if err := createCertificate(certPath, keyPath, hosts); err != nil {
			return tls.Certificate{}, "", err
		}
		cert, err = tls.LoadX509KeyPair(certPath, keyPath)
	}
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("panel certificate: %w", err)
	}
	return cert, Fingerprint(cert.Certificate[0]), nil
}

// Fingerprint is the SHA-256 of a DER certificate as browsers show it.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

func createCertificate(certPath, keyPath string, hosts []string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "ThothDock Web Panel", Organization: []string{"ThothDock"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(825 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range append([]string{"127.0.0.1", "localhost"}, hosts...) {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := store.WriteFileAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	if err := store.WriteFileAtomic(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		os.Remove(keyPath)
		return err
	}
	return nil
}
