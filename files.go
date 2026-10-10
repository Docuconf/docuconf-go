package docuconf

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

// fileInput is implemented by the docuconf file types. bind loads and
// checks the input at boot and stores the result in the field.
type fileInput interface {
	fileType() string
	bind(b *fileBinding) []Violation
}

// configInput is implemented by ConfigFile[T].
type configInput interface {
	fileInput
	configType() reflect.Type
}

var fileInputType = reflect.TypeOf((*fileInput)(nil)).Elem()

func isFileType(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && reflect.PointerTo(t).Implements(fileInputType)
}

// fileBinding is what a file input needs to load itself.
type fileBinding struct {
	decl     *fileDecl
	path     string // on disk, after pathEnv and DOCUCONF_FILE_ROOT
	now      func() time.Time
	interval time.Duration
	logger   *slog.Logger
	// password returns the keystore password variable's value.
	password func() (string, bool)
}

// missing reports a required file that is not there, naming the variable
// that moves it when the input has one.
func (b *fileBinding) missing(p string) Violation {
	if b.decl.pathEnv != "" {
		return b.violation(CodeFileMissing, "%s does not exist (mount it there, or set %s)", p, b.decl.pathEnv)
	}
	return b.violation(CodeFileMissing, "%s does not exist", p)
}

func (b *fileBinding) violation(code Code, format string, args ...any) Violation {
	return Violation{Input: b.decl.name, Code: code, Message: fmt.Sprintf(format, args...)}
}

// readFile reads one file of an input. absent is true, with no violation,
// when an optional input's file does not exist.
func (b *fileBinding) readFile(p string, required bool) (data []byte, absent bool, viols []Violation) {
	fi, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !required {
			return nil, true, nil
		}
		return nil, false, []Violation{b.missing(p)}
	case errors.Is(err, fs.ErrPermission):
		return nil, false, []Violation{b.unreadable(p)}
	case err != nil:
		return nil, false, []Violation{b.violation(CodeFileUnreadable, "%s: %v", p, err)}
	case fi.IsDir():
		return nil, false, []Violation{b.violation(CodeFileMalformed, "%s is a directory, not a file", p)}
	}
	if max := b.decl.maxSize; max != nil && fi.Size() > *max {
		return nil, false, []Violation{b.violation(CodeFileTooLarge, "%s is %d bytes, above maxSize %d", p, fi.Size(), *max)}
	}
	data, err = os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, false, []Violation{b.unreadable(p)}
		}
		return nil, false, []Violation{b.violation(CodeFileUnreadable, "%s: %v", p, err)}
	}
	return data, false, nil
}

func (b *fileBinding) unreadable(p string) Violation {
	return b.violation(CodeFileUnreadable,
		"%s exists but cannot be read (permission denied); a container running as non-root needs the pod's securityContext.fsGroup to read secret volumes", p)
}

// state common to every file type.
type fileState struct {
	path    string
	present bool
}

// ---------------------------------------------------------------------------
// TLSKeyPair

// TLSKeyPair is a TLS certificate and key in the kubernetes.io/tls layout:
// a directory holding tls.crt, tls.key and, optionally, ca.crt.
//
//	TLS docuconf.TLSKeyPair `file:"serving-tls,required" path:"/etc/app/tls" dnsNames:"app.internal" minRemaining:"720h" reload:"watch"`
//
// At boot docuconf checks that the certificate and key parse and match,
// that the certificate is valid with at least minRemaining left, covers
// every name in dnsNames, uses one of keyAlgorithms, and chains to ca.crt
// when requireCA is set. With reload:"watch", GetCertificate serves a
// renewed certificate without a restart.
type TLSKeyPair struct{ s *tlsState }

type tlsState struct {
	fileState
	r *reloader[*tlsMaterial]
}

type tlsMaterial struct {
	cert *tls.Certificate
	ca   *x509.CertPool
}

func (TLSKeyPair) fileType() string { return fileTLS }

func (k *TLSKeyPair) bind(b *fileBinding) []Violation {
	st := &tlsState{fileState: fileState{path: b.path}}
	k.s = st
	m, absent, viols := loadTLS(b, true)
	if absent || len(viols) > 0 {
		return viols
	}
	st.present = true
	paths := []string{filepath.Join(b.path, "tls.crt"), filepath.Join(b.path, "tls.key"), filepath.Join(b.path, "ca.crt")}
	st.r = newReloader(b, m, paths, func() (*tlsMaterial, []Violation) {
		m, _, v := loadTLS(b, false)
		return m, v
	})
	return nil
}

// Present reports whether the key pair was found at boot. A required
// input is always present once Parse has succeeded.
func (k TLSKeyPair) Present() bool { return k.s != nil && k.s.present }

// Dir is the directory holding the key pair.
func (k TLSKeyPair) Dir() string {
	if k.s == nil {
		return ""
	}
	return k.s.path
}

// CertFile is the path of tls.crt.
func (k TLSKeyPair) CertFile() string { return filepath.Join(k.Dir(), "tls.crt") }

// KeyFile is the path of tls.key.
func (k TLSKeyPair) KeyFile() string { return filepath.Join(k.Dir(), "tls.key") }

// CAFile is the path of ca.crt.
func (k TLSKeyPair) CAFile() string { return filepath.Join(k.Dir(), "ca.crt") }

// Current returns the current certificate, reloading it first if the
// input is watched and its files changed.
func (k TLSKeyPair) Current() (*tls.Certificate, error) {
	if !k.Present() {
		return nil, errors.New("docuconf: TLS key pair not loaded")
	}
	return k.s.r.get().cert, nil
}

// GetCertificate has the signature of tls.Config.GetCertificate.
func (k TLSKeyPair) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return k.Current()
}

// GetClientCertificate has the signature of tls.Config.GetClientCertificate,
// for using the key pair as an mTLS client certificate.
func (k TLSKeyPair) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return k.Current()
}

// CAPool returns the certificates in ca.crt, or nil if there is none.
func (k TLSKeyPair) CAPool() *x509.CertPool {
	if !k.Present() {
		return nil
	}
	return k.s.r.get().ca
}

// OnChange registers fn, which is called with the new certificate after
// a reload of a watched key pair is accepted, never after a rejected
// one. While a hook is registered the files are checked in the
// background every WatchInterval, so fn runs without a read. Hooks run
// one at a time, in the order they were registered; a hook that panics
// is logged and the others still run. The returned function unregisters
// fn. Use it to rebuild what was made from the certificate; a
// tls.Config that uses GetCertificate needs no hook.
func (k TLSKeyPair) OnChange(fn func(*tls.Certificate)) (cancel func()) {
	if !k.Present() {
		return nopCancel
	}
	return k.s.r.onChange(func(m *tlsMaterial) { fn(m.cert) })
}

// ReloadStatus returns the key pair's reload status.
func (k TLSKeyPair) ReloadStatus() ReloadStatus {
	if !k.Present() {
		return ReloadStatus{}
	}
	return k.s.r.reloadStatus()
}

// ---------------------------------------------------------------------------
// CABundle

// CABundle is a PEM file of one or more CA certificates, for trusting
// private CAs.
//
//	UpstreamCA docuconf.CABundle `file:"upstream-ca" path:"/etc/app/ca/bundle.pem" minCertificates:"1"`
type CABundle struct{ s *caState }

type caState struct {
	fileState
	r *reloader[[]*x509.Certificate]
}

func (CABundle) fileType() string { return fileCABundle }

func (c *CABundle) bind(b *fileBinding) []Violation {
	st := &caState{fileState: fileState{path: b.path}}
	c.s = st
	certs, absent, viols := loadCABundle(b)
	if absent || len(viols) > 0 {
		return viols
	}
	st.present = true
	st.r = newReloader(b, certs, []string{b.path}, func() ([]*x509.Certificate, []Violation) {
		certs, _, v := loadCABundle(b)
		return certs, v
	})
	return nil
}

// Present reports whether the bundle was found at boot.
func (c CABundle) Present() bool { return c.s != nil && c.s.present }

// Path is the bundle's path.
func (c CABundle) Path() string {
	if c.s == nil {
		return ""
	}
	return c.s.path
}

// Certificates returns the bundle's certificates, reloading them first if
// the input is watched and the file changed.
func (c CABundle) Certificates() []*x509.Certificate {
	if !c.Present() {
		return nil
	}
	return c.s.r.get()
}

// Load returns a new pool holding the bundle's current certificates.
func (c CABundle) Load() (*x509.CertPool, error) {
	if !c.Present() {
		return nil, errors.New("docuconf: CA bundle not loaded")
	}
	pool := x509.NewCertPool()
	for _, cert := range c.Certificates() {
		pool.AddCert(cert)
	}
	return pool, nil
}

// OnChange registers fn, which is called with the new certificates after
// a reload of a watched bundle is accepted. It works as
// TLSKeyPair.OnChange does: rebuild a pool or client made from the
// bundle in fn.
func (c CABundle) OnChange(fn func([]*x509.Certificate)) (cancel func()) {
	if !c.Present() {
		return nopCancel
	}
	return c.s.r.onChange(fn)
}

// ReloadStatus returns the bundle's reload status.
func (c CABundle) ReloadStatus() ReloadStatus {
	if !c.Present() {
		return ReloadStatus{}
	}
	return c.s.r.reloadStatus()
}

// ---------------------------------------------------------------------------
// Keystore

// Keystore is a PKCS#12 keystore holding a private key, its certificate
// and optional CA certificates. Its password comes from a secret variable
// named by passwordVar.
//
//	Partner docuconf.Keystore `file:"partner-keystore" path:"/etc/app/partner/keystore.p12" passwordVar:"PARTNER_KEYSTORE_PASSWORD"`
type Keystore struct{ s *keystoreState }

type keystoreState struct {
	fileState
	r *reloader[*keystoreContent]
}

type keystoreContent struct {
	cert tls.Certificate
	cas  []*x509.Certificate
}

func (Keystore) fileType() string { return fileKeystore }

func (k *Keystore) bind(b *fileBinding) []Violation {
	st := &keystoreState{fileState: fileState{path: b.path}}
	k.s = st
	c, absent, viols := loadKeystore(b)
	if absent || len(viols) > 0 {
		return viols
	}
	st.present = true
	st.r = newReloader(b, c, []string{b.path}, func() (*keystoreContent, []Violation) {
		c, _, v := loadKeystore(b)
		return c, v
	})
	return nil
}

// Present reports whether the keystore was found at boot.
func (k Keystore) Present() bool { return k.s != nil && k.s.present }

// Path is the keystore's path.
func (k Keystore) Path() string {
	if k.s == nil {
		return ""
	}
	return k.s.path
}

// Certificate returns the keystore's key and certificate chain, ready for
// tls.Config.Certificates.
func (k Keystore) Certificate() tls.Certificate {
	if !k.Present() {
		return tls.Certificate{}
	}
	return k.s.r.get().cert
}

// PrivateKey returns the keystore's private key.
func (k Keystore) PrivateKey() crypto.PrivateKey { return k.Certificate().PrivateKey }

// CACertificates returns the CA certificates stored in the keystore.
func (k Keystore) CACertificates() []*x509.Certificate {
	if !k.Present() {
		return nil
	}
	return k.s.r.get().cas
}

// OnChange registers fn, which is called with the new key and
// certificate chain after a reload of a watched keystore is accepted. It
// works as TLSKeyPair.OnChange does.
//
// A reload opens the new keystore with the password read at boot, since
// a process's environment does not change: rotating the password needs
// a rollout. A keystore that does not open with it is rejected as
// keystore_unreadable, and the previous content stays current.
func (k Keystore) OnChange(fn func(tls.Certificate)) (cancel func()) {
	if !k.Present() {
		return nopCancel
	}
	return k.s.r.onChange(func(c *keystoreContent) { fn(c.cert) })
}

// ReloadStatus returns the keystore's reload status.
func (k Keystore) ReloadStatus() ReloadStatus {
	if !k.Present() {
		return ReloadStatus{}
	}
	return k.s.r.reloadStatus()
}

// ---------------------------------------------------------------------------
// TextFile

// TextFile is a UTF-8 text file, such as a licence key, checked against
// pattern, minLength and maxLength.
//
//	License docuconf.TextFile `file:"license,required" path:"/etc/app/license/license.key" pattern:"^[A-Z0-9-]+\\n?$"`
type TextFile struct{ s *textState }

type textState struct {
	fileState
	r *reloader[string]
}

func (TextFile) fileType() string { return fileText }

func (t *TextFile) bind(b *fileBinding) []Violation {
	st := &textState{fileState: fileState{path: b.path}}
	t.s = st
	s, absent, viols := loadText(b)
	if absent || len(viols) > 0 {
		return viols
	}
	st.present = true
	st.r = newReloader(b, s, []string{b.path}, func() (string, []Violation) {
		s, _, v := loadText(b)
		return s, v
	})
	return nil
}

// Present reports whether the file was found at boot.
func (t TextFile) Present() bool { return t.s != nil && t.s.present }

// Path is the file's path.
func (t TextFile) Path() string {
	if t.s == nil {
		return ""
	}
	return t.s.path
}

// Content returns the file's text, reloading it first if the input is
// watched and the file changed.
func (t TextFile) Content() string {
	if !t.Present() {
		return ""
	}
	return t.s.r.get()
}

// OnChange registers fn, which is called with the new text after a
// reload of a watched file is accepted. It works as TLSKeyPair.OnChange
// does.
func (t TextFile) OnChange(fn func(string)) (cancel func()) {
	if !t.Present() {
		return nopCancel
	}
	return t.s.r.onChange(fn)
}

// ReloadStatus returns the file's reload status.
func (t TextFile) ReloadStatus() ReloadStatus {
	if !t.Present() {
		return ReloadStatus{}
	}
	return t.s.r.reloadStatus()
}

// ---------------------------------------------------------------------------
// BinaryFile

// BinaryFile is an opaque file, such as a GeoIP database. Only its
// presence and maxSize are checked; the app opens it itself.
//
//	GeoIP docuconf.BinaryFile `file:"geoip" path:"/data/geoip/GeoLite2-City.mmdb" maxSize:"128Mi"`
type BinaryFile struct{ s *fileState }

func (BinaryFile) fileType() string { return fileBinary }

func (f *BinaryFile) bind(b *fileBinding) []Violation {
	f.s = &fileState{path: b.path}
	fi, err := os.Stat(b.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if b.decl.required {
			return []Violation{b.missing(b.path)}
		}
		return nil
	case errors.Is(err, fs.ErrPermission):
		return []Violation{b.unreadable(b.path)}
	case err != nil:
		return []Violation{b.violation(CodeFileUnreadable, "%s: %v", b.path, err)}
	case fi.IsDir():
		return []Violation{b.violation(CodeFileMalformed, "%s is a directory, not a file", b.path)}
	case b.decl.maxSize != nil && fi.Size() > *b.decl.maxSize:
		return []Violation{b.violation(CodeFileTooLarge, "%s is %d bytes, above maxSize %d", b.path, fi.Size(), *b.decl.maxSize)}
	}
	fh, err := os.Open(b.path)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return []Violation{b.unreadable(b.path)}
		}
		return []Violation{b.violation(CodeFileUnreadable, "%s: %v", b.path, err)}
	}
	fh.Close()
	f.s.present = true
	return nil
}

// Present reports whether the file was found at boot.
func (f BinaryFile) Present() bool { return f.s != nil && f.s.present }

// Path is the file's path.
func (f BinaryFile) Path() string {
	if f.s == nil {
		return ""
	}
	return f.s.path
}

// Open opens the file for reading.
func (f BinaryFile) Open() (*os.File, error) { return os.Open(f.Path()) }

// ReadAll reads the whole file.
func (f BinaryFile) ReadAll() ([]byte, error) { return os.ReadFile(f.Path()) }

// ---------------------------------------------------------------------------
// ConfigFile

// ConfigFile is a structured JSON or YAML file bound to T. The contract
// carries a JSON Schema generated from T (json tags give names; fields
// without omitempty are required; unknown properties are rejected), so the
// platform checks the file against the type the app decodes into.
//
//	Routes docuconf.ConfigFile[Routes] `file:"routes,required" path:"/etc/app/routes/routes.yaml" reload:"watch"`
//
// T's fields may carry the constraint tags min, max, minLength, maxLength,
// pattern, values, minItems and maxItems, and a desc tag where no doc
// comment exists. T may implement Validate() error for further checks.
type ConfigFile[T any] struct{ s *configState[T] }

type configState[T any] struct {
	fileState
	r *reloader[T]
}

func (ConfigFile[T]) fileType() string { return fileConfig }

func (ConfigFile[T]) configType() reflect.Type { return reflect.TypeFor[T]() }

func (c *ConfigFile[T]) bind(b *fileBinding) []Violation {
	st := &configState[T]{fileState: fileState{path: b.path}}
	c.s = st
	v, absent, viols := loadConfig[T](b)
	if absent || len(viols) > 0 {
		return viols
	}
	st.present = true
	st.r = newReloader(b, v, []string{b.path}, func() (T, []Violation) {
		v, _, viols := loadConfig[T](b)
		return v, viols
	})
	return nil
}

// Present reports whether the file was found at boot.
func (c ConfigFile[T]) Present() bool { return c.s != nil && c.s.present }

// Path is the file's path.
func (c ConfigFile[T]) Path() string {
	if c.s == nil {
		return ""
	}
	return c.s.path
}

// Value returns the decoded file, reloading it first if the input is
// watched and the file changed. It returns T's zero value for an absent
// optional file.
func (c ConfigFile[T]) Value() T {
	if !c.Present() {
		var zero T
		return zero
	}
	return c.s.r.get()
}

// OnChange registers fn, which is called with the new value after a
// reload of a watched file is accepted. It works as TLSKeyPair.OnChange
// does.
func (c ConfigFile[T]) OnChange(fn func(T)) (cancel func()) {
	if !c.Present() {
		return nopCancel
	}
	return c.s.r.onChange(fn)
}

// ReloadStatus returns the file's reload status.
func (c ConfigFile[T]) ReloadStatus() ReloadStatus {
	if !c.Present() {
		return ReloadStatus{}
	}
	return c.s.r.reloadStatus()
}
