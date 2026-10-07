package docuconf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/caarlos0/env/v11"
)

// noLog is Options that never touch the process environment or the
// termination log.
func noLog(environ map[string]string) Options {
	return Options{Environment: environ, TerminationLog: "-"}
}

func declProblems(t *testing.T, err error) string {
	t.Helper()
	var derr *DeclarationError
	if !errors.As(err, &derr) {
		t.Fatalf("want a *DeclarationError, got %v", err)
	}
	return strings.Join(derr.Problems, "\n")
}

type typoTags struct {
	// HTTP listen port.
	Port int `env:"PORT" mni:"1" maxx:"10"`
	// Primary database password.
	Password string `env:"PASSWORD" secrte:"true" json:"password" yaml:"password" validate:"required"`
}

type typoFileTag struct {
	// Certificate the service serves HTTPS with.
	TLS TLSKeyPair `file:"serving-tls" path:"/etc/app/tls" dnsName:"api.example.com"`
}

type hostTagsOnly struct {
	// HTTP listen port.
	Port int `env:"PORT" json:"port" yaml:"port" mapstructure:"port" default:"8080" required:"false" toml:"port"`
}

func TestTypoTagsAreDeclarationErrors(t *testing.T) {
	_, err := ParseWithOptions[typoTags](noLog(nil))
	msg := declProblems(t, err)
	for _, want := range []string{
		`PORT (typoTags.Port): unknown tag "mni"; did you mean "min"?`,
		`PORT (typoTags.Port): unknown tag "maxx"; did you mean "max"?`,
		`PASSWORD (typoTags.Password): unknown tag "secrte"; did you mean "secret"?`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "json") || strings.Contains(msg, "validate") {
		t.Errorf("flagged another library's tag:\n%s", msg)
	}
	if _, err := Export[typoTags](Meta{Name: "svc"}); err == nil || !strings.Contains(err.Error(), `did you mean "secret"`) {
		t.Errorf("Export: want the typo reported, got %v", err)
	}

	_, err = ParseWithOptions[typoFileTag](noLog(nil))
	if msg := declProblems(t, err); !strings.Contains(msg, `unknown tag "dnsName"; did you mean "dnsNames"?`) {
		t.Errorf("file tag typo not reported:\n%s", msg)
	}

	if _, err := ParseWithOptions[hostTagsOnly](noLog(nil)); err != nil {
		t.Errorf("tags of other libraries must not be reported: %v", err)
	}
}

func TestTagKeys(t *testing.T) {
	got := tagKeys(`env:"A,required" json:"a" secrte:"x\"y" bad`)
	want := []string{"env", "json", "secrte"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tagKeys = %v, want %v", got, want)
	}
	if d := editDistance("mni", "min"); d != 1 {
		t.Errorf("editDistance(mni, min) = %d, want 1", d)
	}
}

func TestParseWantsAStructType(t *testing.T) {
	_, err := Parse[*hostTagsOnly]()
	if err == nil || err.Error() != "docuconf.Parse[T]: T must be a struct type, got *docuconf.hostTagsOnly; use Parse[docuconf.hostTagsOnly]" {
		t.Errorf("got %v", err)
	}
	if _, err := Export[*hostTagsOnly](Meta{Name: "svc"}); err == nil || !strings.Contains(err.Error(), "use Export[docuconf.hostTagsOnly]") {
		t.Errorf("Export: got %v", err)
	}
}

type enumCfg struct {
	// Minimum log level emitted.
	LogLevel string `env:"LOG_LEVEL" envDefault:"info" values:"debug,info"`
	// HTTP listen port.
	Port int `env:"PORT" envDefault:"8080"`
}

func TestParseReturnsZeroOnError(t *testing.T) {
	cfg, err := ParseWithOptions[enumCfg](noLog(map[string]string{"LOG_LEVEL": "verbose", "PORT": "9090"}))
	if err == nil {
		t.Fatal("want an error")
	}
	if cfg != (enumCfg{}) {
		t.Errorf("want the zero value on error, got %+v", cfg)
	}
}

type wrongTagCfg struct {
	// HTTP listen port.
	Port int `env:"PORT" pattern:"^[0-9]+$"`
}

func TestDeclarationGrammar(t *testing.T) {
	_, err := ParseWithOptions[wrongTagCfg](noLog(nil))
	if msg := declProblems(t, err); !strings.Contains(msg, "tag pattern does not apply to an int variable") {
		t.Errorf("got %s", msg)
	}
}

type manyProblems struct {
	Port int `env:"PORT" min:"x"`
}

func TestExportReportsEveryProblemInOnePass(t *testing.T) {
	_, err := Export[manyProblems](Meta{Name: "Billing"})
	msg := declProblems(t, err)
	for _, want := range []string{"min must be an integer", `service name "Billing"`, "needs a description"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
}

// hostPort is parsed from one variable by a FuncMap entry.
type hostPort struct{ Host, Port string }

var hostPortParsers = map[reflect.Type]env.ParserFunc{
	reflect.TypeOf(hostPort{}): func(v string) (any, error) {
		h, p, ok := strings.Cut(v, ":")
		if !ok {
			return nil, fmt.Errorf("%q is not host:port", v)
		}
		return hostPort{h, p}, nil
	},
}

type ownOptions struct {
	// Upstream service address, as host:port.
	Upstream hostPort `env:"UPSTREAM,required"`
	// Upstream service token.
	Token Secret `env:"TOKEN"`
	// Secret upstream address, as host:port.
	Hidden hostPort `env:"HIDDEN" secret:"true"`
}

func (ownOptions) DocuconfOptions() Options {
	return Options{Prefix: "APP_", FuncMap: hostPortParsers}
}

type noOwnOptions struct {
	// Upstream service address, as host:port.
	Upstream hostPort `env:"UPSTREAM,required"`
}

func TestDocuconfOptions(t *testing.T) {
	cfg, err := ParseWithOptions[ownOptions](noLog(map[string]string{"APP_UPSTREAM": "db:5432"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Upstream != (hostPort{"db", "5432"}) {
		t.Errorf("Upstream = %+v", cfg.Upstream)
	}
	out, err := Export[ownOptions](Meta{Name: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("APP_UPSTREAM: {")) {
		t.Errorf("Export did not use the struct's options:\n%s", out)
	}

	_, err = ParseWithOptions[noOwnOptions](noLog(nil))
	msg := declProblems(t, err)
	if !strings.Contains(msg, "register a parser for it in FuncMap") || !strings.Contains(msg, "DocuconfOptions") {
		t.Errorf("got %s", msg)
	}
}

func TestSecretType(t *testing.T) {
	const pw = "hunter2"
	cfg := ownOptions{Upstream: hostPort{"db", "1"}, Token: Secret(pw)}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		if s := fmt.Sprintf(format, cfg); strings.Contains(s, pw) || strings.Contains(s, fmt.Sprintf("%x", pw)) {
			t.Errorf("%s printed the secret: %s", format, s)
		}
	}
	if got := fmt.Sprintf("%#v", Secret(pw)); got != `docuconf.Secret("***")` {
		t.Errorf("%%#v = %s", got)
	}
	b, _ := json.Marshal(cfg)
	if bytes.Contains(b, []byte(pw)) {
		t.Errorf("json printed the secret: %s", b)
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("cfg", "token", cfg.Token, "cfg", cfg)
	if strings.Contains(buf.String(), pw) {
		t.Errorf("slog printed the secret: %s", buf.String())
	}
	if cfg.Token.Reveal() != pw {
		t.Error("Reveal lost the value")
	}

	// A Secret field is secret without a tag.
	out, err := Export[ownOptions](Meta{Name: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("APP_TOKEN: {\n\t\t\ttype:        \"string\"")) || !bytes.Contains(out, []byte("secret:      true")) {
		t.Errorf("Secret field not exported as secret:\n%s", out)
	}

	type contradicts struct {
		// Upstream service token.
		Token Secret `env:"TOKEN" secret:"false"`
	}
	if _, err := ParseWithOptions[contradicts](noLog(nil)); err == nil {
		t.Error(`secret:"false" on a Secret field must be an error`)
	}
}

// pinCode fails to parse with an error that quotes its input.
type pinCode string

func (p *pinCode) UnmarshalText(b []byte) error {
	if len(b) != 4 {
		return fmt.Errorf("pin %q must have 4 digits", b)
	}
	*p = pinCode(b)
	return nil
}

type userValidators struct {
	// Secret upstream address, as host:port.
	Hidden hostPort `env:"HIDDEN" secret:"true"`
	// Secret PIN code.
	PIN pinCode `env:"PIN" secret:"true"`
	// Public upstream address, as host:port.
	Shown hostPort `env:"SHOWN"`
}

func TestUserValidatorErrorsNeverShowSecrets(t *testing.T) {
	opts := noLog(map[string]string{"HIDDEN": "hunter2-hidden", "PIN": "hunter2-pin", "SHOWN": "public-value"})
	opts.FuncMap = hostPortParsers
	_, err := ParseWithOptions[userValidators](opts)
	var verr *ValidationError
	if !errors.As(err, &verr) || len(verr.Violations) != 3 {
		t.Fatalf("want 3 violations, got %v", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "hunter2") {
		t.Errorf("a validator's error leaked a secret:\n%s", msg)
	}
	if !strings.Contains(msg, "public-value") {
		t.Errorf("a non-secret validator's error should explain itself:\n%s", msg)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Error("boot", "err", verr)
	if strings.Contains(buf.String(), "hunter2") || !strings.Contains(buf.String(), `"count":3`) {
		t.Errorf("ValidationError.LogValue: %s", buf.String())
	}
}

type redactMe struct {
	// HTTP listen port.
	Port int `env:"PORT" envDefault:"8080"`
	// Primary database password.
	Password string `env:"PASSWORD" secret:"true"`
	// Upstream service token.
	Token Secret `env:"TOKEN"`
	// Optional feature flag.
	Flag *bool `env:"FLAG"`
}

func TestRedacted(t *testing.T) {
	cfg := redactMe{Port: 80, Password: "hunter2", Token: "hunter3"}
	got := Redacted(&cfg)
	want := map[string]any{"PORT": 80, "PASSWORD": "***", "TOKEN": "***", "FLAG": nil}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Redacted = %#v, want %#v", got, want)
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("loaded", "config", LogValue(cfg))
	if strings.Contains(buf.String(), "hunter") || !strings.Contains(buf.String(), "config.PORT=80") {
		t.Errorf("LogValue: %s", buf.String())
	}
	if Redacted(42) != nil {
		t.Error("Redacted of a non-struct should be nil")
	}
}

func TestParseOrExit(t *testing.T) {
	var out bytes.Buffer
	code := -1
	exitFunc, exitStderr = func(c int) { code = c; panic("exit") }, &out
	defer func() { exitFunc, exitStderr = os.Exit, os.Stderr }()

	tlog := filepath.Join(t.TempDir(), "termination-log")
	func() {
		defer func() { recover() }()
		ParseOrExitWithOptions[enumCfg](Options{Environment: map[string]string{"LOG_LEVEL": "verbose", "PORT": "x"}, TerminationLog: tlog})
	}()
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	want := "docuconf: 2 configuration problems:\n  LOG_LEVEL: \"verbose\" is not one of debug, info (not_in_enum)\n  PORT: \"x\" is not an integer (invalid_type)\n"
	if out.String() != want {
		t.Errorf("stderr:\n%s\nwant:\n%s", out.String(), want)
	}
	if b, _ := os.ReadFile(tlog); string(b) != strings.TrimSuffix(want, "\n")+"\n" {
		t.Errorf("termination log: %q", b)
	}

	// A declaration error is printed the same way, and logged too.
	out.Reset()
	code = -1
	func() {
		defer func() { recover() }()
		ParseOrExitWithOptions[typoTags](Options{Environment: map[string]string{}, TerminationLog: tlog})
	}()
	if code != 1 || !strings.HasPrefix(out.String(), "docuconf: invalid declaration:") {
		t.Errorf("exit %d, stderr %q", code, out.String())
	}
	if b, _ := os.ReadFile(tlog); !strings.Contains(string(b), "secrte") {
		t.Errorf("termination log: %q", b)
	}

	// Valid configuration: no exit.
	code = -1
	cfg := ParseOrExitWithOptions[enumCfg](noLog(map[string]string{}))
	if code != -1 || cfg.Port != 8080 {
		t.Errorf("exit %d, cfg %+v", code, cfg)
	}
}

type typoEnvCfg struct {
	// Primary Postgres connection string.
	DatabaseURL string `env:"DATABASE_URL"`
	// HTTP listen port.
	Port int `env:"PORT" envDefault:"8080"`
	// Minimum log level emitted.
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
	// Upstream servers.
	Hosts []string `env:"HOSTS"`
}

func TestTypoEnvNameWarning(t *testing.T) {
	var buf bytes.Buffer
	opts := noLog(map[string]string{
		"DATABSE_URL": "postgres://u:hunter2@db/x", // typo of DATABASE_URL
		"LOG_LEVL":    "debug",                     // typo of LOG_LEVEL
		"HOME":        "/root",                     // short names need a closer match
		"TERM":        "xterm",                     // not close to anything declared
		"PATH":        "/usr/bin",
		"DOCUCONF_X":  "1",
	})
	opts.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := ParseWithOptions[typoEnvCfg](opts); err != nil {
		t.Fatal(err)
	}
	log := buf.String()
	for _, want := range []string{
		"docuconf: DATABSE_URL is set but not declared; did you mean DATABASE_URL?",
		"docuconf: LOG_LEVL is set but not declared; did you mean LOG_LEVEL?",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q in:\n%s", want, log)
		}
	}
	if strings.Contains(log, "hunter2") || strings.Contains(log, "HOME") || strings.Contains(log, "TERM") || strings.Contains(log, "PATH") {
		t.Errorf("unexpected warning:\n%s", log)
	}

	// With a prefix, only prefixed variables are considered.
	buf.Reset()
	type prefixed struct {
		// HTTP listen port.
		Port int `env:"PORT" envDefault:"8080"`
	}
	opts = noLog(map[string]string{"APP_PROT": "1", "PROT": "1"})
	opts.Prefix = "APP_"
	opts.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	if _, err := ParseWithOptions[prefixed](opts); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "APP_PROT is set but not declared; did you mean APP_PORT?") || strings.Contains(got, " PROT is set") {
		t.Errorf("got:\n%s", got)
	}
}

func TestDotEnvRequired(t *testing.T) {
	opts := noLog(map[string]string{})
	opts.DotEnv = []string{filepath.Join(t.TempDir(), ".evn")}
	if _, err := ParseWithOptions[enumCfg](opts); err != nil {
		t.Fatalf("a missing .env file is skipped by default: %v", err)
	}
	opts.DotEnvRequired = true
	if _, err := ParseWithOptions[enumCfg](opts); err == nil || !strings.Contains(err.Error(), ".evn") {
		t.Errorf("want an error naming the file, got %v", err)
	}
}

func TestParseNeverTouchesTheProcessEnvironment(t *testing.T) {
	t.Setenv("PORT", "1234")
	before := os.Environ()
	environ := map[string]string{"LOG_LEVEL": "debug", "PORT": ""}
	cfg, err := ParseWithOptions[enumCfg](noLog(environ))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 {
		t.Errorf("read PORT from the process environment: %d", cfg.Port)
	}
	if !reflect.DeepEqual(environ, map[string]string{"LOG_LEVEL": "debug", "PORT": ""}) {
		t.Errorf("changed Options.Environment: %v", environ)
	}
	if !reflect.DeepEqual(before, os.Environ()) {
		t.Error("changed the process environment")
	}
}
