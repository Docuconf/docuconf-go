package docuconf

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	"software.sslmate.com/src/go-pkcs12"
)

// loadTLS reads and checks a kubernetes.io/tls directory. At boot every
// check applies; on reload, minRemaining is only logged, since a renewed
// certificate is never worse than the one it replaces.
func loadTLS(b *fileBinding, boot bool) (*tlsMaterial, bool, []Violation) {
	d := b.decl
	crtPEM, absent, viols := b.readFile(filepath.Join(b.path, "tls.crt"), d.required)
	if absent {
		return nil, true, nil
	}
	keyPEM, _, kv := b.readFile(filepath.Join(b.path, "tls.key"), true)
	viols = append(viols, kv...)
	if len(viols) > 0 {
		return nil, false, viols
	}

	var chain []*x509.Certificate
	for rest := crtPEM; ; {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, false, []Violation{b.violation(CodeCertificateInvalid, "tls.crt: certificate %d does not parse: %v", len(chain), err)}
		}
		chain = append(chain, c)
	}
	if len(chain) == 0 {
		return nil, false, []Violation{b.violation(CodeCertificateInvalid, "tls.crt holds no PEM certificate")}
	}
	pair, err := tls.X509KeyPair(crtPEM, keyPEM)
	if err != nil {
		if !privateKeyParses(keyPEM) {
			return nil, false, []Violation{b.violation(CodeFileMalformed, "tls.key holds no parseable PEM private key")}
		}
		return nil, false, []Violation{b.violation(CodeKeyMismatch, "tls.key does not match the certificate in tls.crt")}
	}
	leaf := chain[0]
	pair.Leaf = leaf

	now := b.now()
	switch {
	case now.Before(leaf.NotBefore):
		viols = append(viols, b.violation(CodeCertificateInvalid, "certificate is not valid until %s", leaf.NotBefore.UTC().Format(time.RFC3339)))
	case !now.Before(leaf.NotAfter):
		viols = append(viols, b.violation(CodeCertificateInvalid, "certificate expired at %s", leaf.NotAfter.UTC().Format(time.RFC3339)))
	case d.minRemaining != nil && leaf.NotAfter.Sub(now) < *d.minRemaining:
		left := leaf.NotAfter.Sub(now).Truncate(time.Minute)
		if boot {
			viols = append(viols, b.violation(CodeCertificateExpiring, "certificate expires at %s, in %s, less than minRemaining %s",
				leaf.NotAfter.UTC().Format(time.RFC3339), left, formatDuration(*d.minRemaining)))
		} else {
			b.logger.Warn("docuconf: reloaded certificate has less than minRemaining left", "input", d.name, "remaining", left.String())
		}
	}
	for _, name := range d.dnsNames {
		if err := leaf.VerifyHostname(name); err != nil {
			viols = append(viols, b.violation(CodeCertificateNameMismatch, "certificate does not cover %s", name))
		}
	}
	if len(d.keyAlgorithms) > 0 {
		alg := keyAlgorithm(leaf)
		if !slices.Contains(d.keyAlgorithms, alg) {
			viols = append(viols, b.violation(CodeCertificateInvalid, "certificate key algorithm %s is not one of %s", alg, strings.Join(d.keyAlgorithms, ", ")))
		}
	}

	m := &tlsMaterial{cert: &pair}
	caPEM, caAbsent, cv := b.readFile(filepath.Join(b.path, "ca.crt"), d.requireCA)
	viols = append(viols, cv...)
	if !caAbsent && len(cv) == 0 {
		m.ca = x509.NewCertPool()
		if !m.ca.AppendCertsFromPEM(caPEM) {
			viols = append(viols, b.violation(CodeCertificateInvalid, "ca.crt holds no parseable PEM certificate"))
		} else if d.requireCA {
			inter := x509.NewCertPool()
			for _, c := range chain[1:] {
				inter.AddCert(c)
			}
			_, err := leaf.Verify(x509.VerifyOptions{
				Roots:         m.ca,
				Intermediates: inter,
				CurrentTime:   now,
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
			})
			if err != nil {
				viols = append(viols, b.violation(CodeCertificateInvalid, "certificate does not chain to ca.crt: %v", err))
			}
		}
	}
	if len(viols) > 0 {
		return nil, false, viols
	}
	return m, false, nil
}

func privateKeyParses(keyPEM []byte) bool {
	for rest := keyPEM; ; {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			return false
		}
		if !strings.HasSuffix(blk.Type, "PRIVATE KEY") {
			continue
		}
		if _, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
			return true
		}
		if _, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
			return true
		}
		if _, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
			return true
		}
		return false
	}
}

func keyAlgorithm(c *x509.Certificate) string {
	switch c.PublicKey.(type) {
	case *rsa.PublicKey:
		return "RSA"
	case *ecdsa.PublicKey:
		return "ECDSA"
	case ed25519.PublicKey:
		return "Ed25519"
	}
	return c.PublicKeyAlgorithm.String()
}

func loadCABundle(b *fileBinding) ([]*x509.Certificate, bool, []Violation) {
	data, absent, viols := b.readFile(b.path, b.decl.required)
	if absent || len(viols) > 0 {
		return nil, absent, viols
	}
	var certs []*x509.Certificate
	for rest := data; ; {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, false, []Violation{b.violation(CodeCertificateInvalid, "certificate %d does not parse: %v", len(certs), err)}
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, false, []Violation{b.violation(CodeFileMalformed, "holds no PEM certificates")}
	}
	if len(certs) < b.decl.minCertificates {
		return nil, false, []Violation{b.violation(CodeCertificateInvalid, "holds %s, need at least %d", plural(len(certs), "certificate"), b.decl.minCertificates)}
	}
	return certs, false, nil
}

func loadKeystore(b *fileBinding) (*keystoreContent, bool, []Violation) {
	data, absent, viols := b.readFile(b.path, b.decl.required)
	if absent || len(viols) > 0 {
		return nil, absent, viols
	}
	password := ""
	if b.decl.passwordVar != "" {
		p, ok := b.password()
		if !ok {
			return nil, false, []Violation{b.violation(CodeKeystoreUnreadable, "cannot open: its password variable %s is not set", b.decl.passwordVar)}
		}
		password = p
	}
	key, leaf, cas, err := pkcs12.DecodeChain(data, password)
	if err != nil {
		return nil, false, []Violation{b.violation(CodeKeystoreUnreadable, "cannot open with the password from %s: %v", orNone(b.decl.passwordVar), err)}
	}
	cert := tls.Certificate{PrivateKey: key, Leaf: leaf, Certificate: [][]byte{leaf.Raw}}
	for _, c := range cas {
		cert.Certificate = append(cert.Certificate, c.Raw)
	}
	if b.now().After(leaf.NotAfter) {
		return nil, false, []Violation{b.violation(CodeCertificateInvalid, "keystore certificate expired at %s", leaf.NotAfter.UTC().Format(time.RFC3339))}
	}
	return &keystoreContent{cert: cert, cas: cas}, false, nil
}

func orNone(s string) string {
	if s == "" {
		return "(no password variable)"
	}
	return s
}

func loadText(b *fileBinding) (string, bool, []Violation) {
	d := b.decl
	data, absent, viols := b.readFile(b.path, d.required)
	if absent || len(viols) > 0 {
		return "", absent, viols
	}
	if !utf8.Valid(data) {
		return "", false, []Violation{b.violation(CodeFileMalformed, "is not valid UTF-8 text")}
	}
	s := string(data)
	n := utf8.RuneCountInString(s)
	if d.minLength != nil && n < *d.minLength {
		viols = append(viols, b.violation(CodeOutOfRange, "is %d characters, below minLength %d", n, *d.minLength))
	}
	if d.maxLength != nil && n > *d.maxLength {
		viols = append(viols, b.violation(CodeOutOfRange, "is %d characters, above maxLength %d", n, *d.maxLength))
	}
	if d.pattern != nil && !d.pattern.MatchString(s) {
		viols = append(viols, b.violation(CodePatternMismatch, "does not match pattern %s", d.pattern))
	}
	if len(viols) > 0 {
		return "", false, viols
	}
	return s, false, nil
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// loadConfig parses a config file, checks it against the schema generated
// from T, and decodes it into T.
func loadConfig[T any](b *fileBinding) (T, bool, []Violation) {
	var zero T
	d := b.decl
	data, absent, viols := b.readFile(b.path, d.required)
	if absent || len(viols) > 0 {
		return zero, absent, viols
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	reason := func(err error) string {
		if d.secret {
			return ""
		}
		return ": " + err.Error()
	}

	// Normalise to JSON, so YAML and JSON are checked the same way.
	if d.format == "yaml" {
		var v any
		if err := yaml.Unmarshal(data, &v); err != nil {
			return zero, false, []Violation{b.violation(CodeFileMalformed, "is not valid YAML%s", reason(err))}
		}
		j, err := json.Marshal(v)
		if err != nil {
			return zero, false, []Violation{b.violation(CodeFileMalformed, "cannot be represented as JSON%s", reason(err))}
		}
		data = j
	}
	doc, err := decodeJSON(data)
	if err != nil {
		return zero, false, []Violation{b.violation(CodeFileMalformed, "is not valid JSON%s", reason(err))}
	}
	for _, p := range d.schema.validate(doc) {
		viols = append(viols, b.violation(CodeSchemaMismatch, "%s", p))
	}
	if len(viols) > 0 {
		return zero, false, viols
	}
	var v T
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return zero, false, []Violation{b.violation(CodeSchemaMismatch, "does not bind to %v%s", reflectTypeName[T](), reason(err))}
	}
	if val, ok := any(&v).(validator); ok {
		if err := val.Validate(); err != nil {
			msg := "failed its Validate method"
			if !d.secret {
				msg = err.Error()
			}
			return zero, false, []Violation{b.violation(CodeSchemaMismatch, "%s", msg)}
		}
	}
	return v, false, nil
}

func reflectTypeName[T any]() string { return fmt.Sprintf("%T", *new(T)) }
