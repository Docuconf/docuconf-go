package upstream_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docuconf/docuconf-go"
	"github.com/docuconf/docuconf-go/examples/orders/internal/upstream"
)

type config struct {
	// CAs that sign the upstream's certificate.
	UpstreamCA docuconf.CABundle `file:"upstream-ca,required" path:"/etc/orders/upstream-ca/ca.pem" reload:"watch"`
}

// TestClientFollowsTheBundle: the client trusts a CA added to the bundle
// after boot, without a restart.
func TestClientFollowsTheBundle(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	root := t.TempDir()
	p := filepath.Join(root, "etc/orders/upstream-ca/ca.pem")
	write := func(der []byte) {
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	write(selfSigned(t)) // not the upstream's CA yet

	cfg, err := docuconf.ParseWithOptions[config](docuconf.Options{
		Environment: map[string]string{}, FileRoot: root, TerminationLog: "-", WatchInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := upstream.New(cfg.UpstreamCA)
	defer c.Close()
	if _, err := c.Get(srv.URL); err == nil {
		t.Fatal("trusted an upstream before its CA was in the bundle")
	}

	write(srv.Certificate().Raw)
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := c.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the client never trusted the new CA: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

// selfSigned returns a CA certificate that signs nothing the test serves.
func selfSigned(t *testing.T) []byte {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "other CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
