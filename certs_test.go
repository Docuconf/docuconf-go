package docuconf_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"software.sslmate.com/src/go-pkcs12"
)

// testCA is a throwaway certificate authority.
type testCA struct {
	cert *x509.Certificate
	key  crypto.Signer
}

var serial int64

func newCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serial++
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "docuconf test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCA{cert: cert, key: key}
}

type leafOpts struct {
	dnsNames  []string
	notBefore time.Time
	notAfter  time.Time
	rsa       bool
}

// issue returns a leaf certificate and its key.
func (ca *testCA) issue(t *testing.T, o leafOpts) (*x509.Certificate, crypto.Signer) {
	t.Helper()
	var key crypto.Signer
	var err error
	if o.rsa {
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	} else {
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	require.NoError(t, err)
	if o.notBefore.IsZero() {
		o.notBefore = time.Now().Add(-time.Hour)
	}
	if o.notAfter.IsZero() {
		o.notAfter = time.Now().Add(90 * 24 * time.Hour)
	}
	serial++
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "leaf"},
		DNSNames:     o.dnsNames,
		NotBefore:    o.notBefore,
		NotAfter:     o.notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, key.Public(), ca.key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert, key
}

func certPEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

func keyPEM(t *testing.T, k crypto.Signer) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// writeTLS writes a kubernetes.io/tls directory.
func writeTLS(t *testing.T, dir string, ca *testCA, cert *x509.Certificate, key crypto.Signer) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "tls.crt"), certPEM(cert))
	writeFile(t, filepath.Join(dir, "tls.key"), keyPEM(t, key))
	writeFile(t, filepath.Join(dir, "ca.crt"), certPEM(ca.cert))
}

func keystore(t *testing.T, ca *testCA, password string) []byte {
	cert, key := ca.issue(t, leafOpts{dnsNames: []string{"client.example.com"}})
	data, err := pkcs12.Modern.Encode(key, cert, []*x509.Certificate{ca.cert}, password)
	require.NoError(t, err)
	return data
}

func writeFile(t *testing.T, p string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, data, 0o644))
}
