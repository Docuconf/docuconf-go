package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

const fixtures = "../fixtures"

// TestFixturesAreGenerated checks that the checked-in fixtures are what
// Generate writes, so they can always be regenerated, and that nothing
// else is in the directory.
func TestFixturesAreGenerated(t *testing.T) {
	want, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	again, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range want {
		if !bytes.Equal(data, again[name]) {
			t.Errorf("%s: Generate is not deterministic", name)
		}
		got, err := os.ReadFile(filepath.Join(fixtures, name))
		if err != nil {
			t.Errorf("%v; run go run ./conformance/gen", err)
			continue
		}
		if !bytes.Equal(got, data) {
			t.Errorf("%s differs from what the generator writes; run go run ./conformance/gen", name)
		}
	}
	entries, err := os.ReadDir(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, ok := want[e.Name()]; !ok {
			t.Errorf("%s is not a generated fixture", e.Name())
		}
	}
}

// TestValidFixturesDoNotExpireSoon fails 60 days before any certificate
// meant to be valid expires, so the fixtures are regenerated (with a new
// validFrom) well before the conformance suite starts failing everywhere.
// Only expired.crt and not-yet-valid.crt are meant to be invalid.
func TestValidFixturesDoNotExpireSoon(t *testing.T) {
	entries, err := os.ReadDir(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * 24 * time.Hour)
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "expired.") || strings.HasPrefix(name, "not-yet-valid.") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fixtures, name))
		if err != nil {
			t.Fatal(err)
		}
		var certs []*x509.Certificate
		switch filepath.Ext(name) {
		case ".crt", ".pem":
			for rest := data; ; {
				var blk *pem.Block
				blk, rest = pem.Decode(rest)
				if blk == nil {
					break
				}
				c, err := x509.ParseCertificate(blk.Bytes)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				certs = append(certs, c)
			}
		case ".p12":
			password := KeystorePassword
			if strings.Contains(name, "no-password") {
				password = ""
			}
			_, leaf, cas, err := pkcs12.DecodeChain(data, password)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			certs = append(cas, leaf)
		}
		for _, c := range certs {
			checked++
			if time.Now().Before(c.NotBefore) {
				t.Errorf("%s: %q is not valid until %s", name, c.Subject.CommonName, c.NotBefore)
			}
			if deadline.After(c.NotAfter) {
				t.Errorf("%s: %q expires at %s, within 60 days; regenerate the fixtures with a later validFrom",
					name, c.Subject.CommonName, c.NotAfter)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no certificates checked")
	}
}
