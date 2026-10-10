// Command gen writes the certificate, key and keystore fixtures that the
// conformance cases in conformance/load/files_tls.yaml read (SPEC §12).
//
//	go run ./conformance/gen -o conformance/fixtures
//
// The output is deterministic: keys are derived from fixed seeds, ECDSA
// signatures follow RFC 6979, Ed25519 signatures are deterministic by
// design, and PKCS#12 salts and IVs come from a fixed stream. Running it
// again writes the same bytes, which TestFixturesAreGenerated checks.
//
// Certificates meant to be valid run for 50 years from 2026-01-01;
// TestValidFixturesDoNotExpireSoon fails 60 days before any of them
// expires. The expired and not-yet-valid ones use fixed dates far in the
// past and the future.
package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

// KeystorePassword is the password of keystore.p12.
const KeystorePassword = "conformance-password"

var (
	validFrom  = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	validUntil = validFrom.AddDate(50, 0, 0)
)

func main() {
	out := flag.String("o", "conformance/fixtures", "the directory to write the fixtures to")
	flag.Parse()
	files, err := Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	for _, name := range sortedKeys(files) {
		if err := os.WriteFile(filepath.Join(*out, name), files[name], 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("wrote %d fixtures to %s\n", len(files), *out)
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Generate returns every fixture by file name.
func Generate() (map[string][]byte, error) {
	files := map[string][]byte{}
	var serial int64
	next := func() *big.Int { serial++; return big.NewInt(serial) }

	ca, err := newCA("ca", "docuconf conformance CA", next())
	if err != nil {
		return nil, err
	}
	other, err := newCA("other-ca", "docuconf conformance other CA", next())
	if err != nil {
		return nil, err
	}
	files["ca.crt"] = certPEM(ca.cert)
	files["other-ca.crt"] = certPEM(other.cert)
	files["ca-bundle.pem"] = append(certPEM(ca.cert), certPEM(other.cert)...)

	leaves := []struct {
		name     string
		issuer   *authority
		key      crypto.Signer
		dnsName  string
		from, to time.Time
	}{
		{"valid", ca, ecKey("valid"), "app.example.test", validFrom, validUntil},
		{"other-name", ca, ecKey("other-name"), "other.example.test", validFrom, validUntil},
		{"ed25519", ca, edKey("ed25519"), "app.example.test", validFrom, validUntil},
		{"other-ca-leaf", other, ecKey("other-ca-leaf"), "app.example.test", validFrom, validUntil},
		{"expired", ca, ecKey("expired"), "app.example.test", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"not-yet-valid", ca, ecKey("not-yet-valid"), "app.example.test", time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2201, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	certs := map[string]*x509.Certificate{}
	keys := map[string]crypto.Signer{}
	for _, l := range leaves {
		tmpl := &x509.Certificate{
			SerialNumber:   next(),
			Subject:        pkix.Name{CommonName: l.dnsName},
			DNSNames:       []string{l.dnsName},
			NotBefore:      l.from,
			NotAfter:       l.to,
			KeyUsage:       x509.KeyUsageDigitalSignature,
			ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
			SubjectKeyId:   keyID(l.key.Public()),
			AuthorityKeyId: l.issuer.cert.SubjectKeyId,
		}
		if _, ok := l.key.(ed25519.PrivateKey); !ok {
			tmpl.KeyUsage |= x509.KeyUsageKeyEncipherment
		}
		cert, err := sign(tmpl, l.issuer.cert, l.key.Public(), l.issuer.key)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l.name, err)
		}
		keyPEM, err := privateKeyPEM(l.key)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l.name, err)
		}
		files[l.name+".crt"] = certPEM(cert)
		files[l.name+".key"] = keyPEM
		certs[l.name], keys[l.name] = cert, l.key
	}

	for _, ks := range []struct{ name, password string }{
		{"keystore.p12", KeystorePassword},
		{"keystore-no-password.p12", ""},
	} {
		enc := pkcs12.Modern2023.WithRand(newStream("pkcs12 " + ks.name))
		data, err := enc.Encode(keys["valid"], certs["valid"], []*x509.Certificate{ca.cert}, ks.password)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ks.name, err)
		}
		files[ks.name] = data
	}
	return files, nil
}

type authority struct {
	cert *x509.Certificate
	key  crypto.Signer
}

func newCA(seed, name string, serial *big.Int) (*authority, error) {
	key := ecKey(seed)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             validFrom,
		NotAfter:              validUntil,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SubjectKeyId:          keyID(key.Public()),
	}
	cert, err := sign(tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &authority{cert: cert, key: key}, nil
}

// sign creates a certificate. A nil random source makes ECDSA signatures
// deterministic (RFC 6979); Ed25519 signatures always are.
func sign(tmpl, parent *x509.Certificate, pub crypto.PublicKey, key crypto.Signer) (*x509.Certificate, error) {
	der, err := x509.CreateCertificate(nil, tmpl, parent, pub, key)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// keyID is a subject key identifier, set explicitly so that it does not
// depend on the Go version's default (SHA-1 before Go 1.25).
func keyID(pub crypto.PublicKey) []byte {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(der)
	return sum[:20]
}

func seed(label string) []byte {
	sum := sha256.Sum256([]byte("docuconf conformance fixture " + label))
	return sum[:]
}

// ecKey derives a P-256 key from a label.
func ecKey(label string) *ecdsa.PrivateKey {
	curve := elliptic.P256()
	n := new(big.Int).Sub(curve.Params().N, big.NewInt(1))
	d := new(big.Int).SetBytes(seed(label))
	d.Mod(d, n).Add(d, big.NewInt(1))
	x, y := curve.ScalarBaseMult(d.FillBytes(make([]byte, 32)))
	return &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: d}
}

// edKey derives an Ed25519 key from a label.
func edKey(label string) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(seed(label))
}

func certPEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

func privateKeyPEM(key crypto.Signer) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// stream is a deterministic byte stream: SHA-256 of a label and a counter.
type stream struct {
	label string
	n     uint64
	buf   bytes.Buffer
}

func newStream(label string) *stream { return &stream{label: label} }

func (s *stream) Read(p []byte) (int, error) {
	for s.buf.Len() < len(p) {
		var ctr [8]byte
		binary.BigEndian.PutUint64(ctr[:], s.n)
		s.n++
		sum := sha256.Sum256(append([]byte(s.label), ctr[:]...))
		s.buf.Write(sum[:])
	}
	return s.buf.Read(p)
}
