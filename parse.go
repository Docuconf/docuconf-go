package docuconf

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/docuconf/docuconf-go/internal/dotenv"
)

// Environment variables that configure docuconf itself.
const (
	// EnvFileRoot remaps every file input's path under a directory, for
	// local development and tests: with DOCUCONF_FILE_ROOT=./dev, the
	// input at /etc/app/tls is read from ./dev/etc/app/tls.
	EnvFileRoot = "DOCUCONF_FILE_ROOT"
	// EnvTerminationLog overrides where boot violations are written. By
	// default they go to /dev/termination-log when it exists, so
	// `kubectl describe pod` shows why the container stopped.
	EnvTerminationLog = "DOCUCONF_TERMINATION_LOG"
)

const defaultTerminationLog = "/dev/termination-log"

// Options configure Parse. The zero value reads the process environment.
type Options struct {
	// Environment replaces the process environment, for tests.
	Environment map[string]string
	// DotEnv lists .env files to read, for local development. Variables
	// already in the environment win over the files, and earlier files
	// win over later ones. Missing files are skipped, with a debug log,
	// unless DotEnvRequired is set.
	DotEnv []string
	// DotEnvRequired makes a missing DotEnv file an error, so that a typo
	// in its name does not silently load nothing.
	DotEnvRequired bool
	// Prefix and FuncMap are passed to caarlos0/env. Prefer returning them
	// from a DocuconfOptions method on the configuration struct, so that
	// docuconf export sees them too.
	Prefix  string
	FuncMap map[reflect.Type]env.ParserFunc
	// FileRoot overrides DOCUCONF_FILE_ROOT.
	FileRoot string
	// TerminationLog overrides DOCUCONF_TERMINATION_LOG. "-" disables it.
	TerminationLog string
	// Now replaces time.Now for certificate checks, for tests.
	Now func() time.Time
	// WatchInterval is how often a file input with reload:"watch" checks
	// for changes when it is read. The default is 10 seconds.
	WatchInterval time.Duration
	// Logger receives warnings (deprecated variables, rejected reloads).
	// The default is slog.Default().
	Logger *slog.Logger
}

// OptionsProvider is implemented by a configuration struct that owns the
// options it is parsed with. Parse, Validate, Export, Redacted and the
// docuconf export command all call it, so the app and its exported
// contract cannot drift apart:
//
//	func (Config) DocuconfOptions() docuconf.Options {
//		return docuconf.Options{Prefix: "APP_", FuncMap: parsers}
//	}
//
// Options passed to ParseWithOptions take precedence field by field: a
// test's Options{Environment: ...} keeps the struct's Prefix and FuncMap.
type OptionsProvider interface {
	DocuconfOptions() Options
}

// typeOptions returns the options configuration struct type t declares
// with a DocuconfOptions method, on the value or the pointer.
func typeOptions(t reflect.Type) Options {
	if t == nil {
		return Options{}
	}
	if p, ok := reflect.Zero(t).Interface().(OptionsProvider); ok {
		return p.DocuconfOptions()
	}
	if p, ok := reflect.New(t).Interface().(OptionsProvider); ok {
		return p.DocuconfOptions()
	}
	return Options{}
}

// withTypeOptions fills every zero field of opts from the type's own
// DocuconfOptions.
func withTypeOptions(t reflect.Type, opts Options) Options {
	base := typeOptions(t)
	if opts.Environment == nil {
		opts.Environment = base.Environment
	}
	if opts.DotEnv == nil {
		opts.DotEnv = base.DotEnv
	}
	opts.DotEnvRequired = opts.DotEnvRequired || base.DotEnvRequired
	if opts.Prefix == "" {
		opts.Prefix = base.Prefix
	}
	if opts.FuncMap == nil {
		opts.FuncMap = base.FuncMap
	}
	if opts.FileRoot == "" {
		opts.FileRoot = base.FileRoot
	}
	if opts.TerminationLog == "" {
		opts.TerminationLog = base.TerminationLog
	}
	if opts.Now == nil {
		opts.Now = base.Now
	}
	if opts.WatchInterval == 0 {
		opts.WatchInterval = base.WatchInterval
	}
	if opts.Logger == nil {
		opts.Logger = base.Logger
	}
	return opts
}

// Parse reads configuration struct T from the process environment and
// its file inputs. It is caarlos0/env's ParseAs plus docuconf's checks:
// every violation is reported together in a *ValidationError, which is
// also written to the container's termination log. On error it returns
// the zero T, never a partly filled one.
//
// Most programs want ParseOrExit, which prints the violations and exits.
func Parse[T any]() (T, error) {
	return ParseWithOptions[T](Options{})
}

// ParseWithOptions is Parse with options. Tests pass
// Options{Environment: ...} to load from a map instead of the process
// environment, which it never reads or changes then.
func ParseWithOptions[T any](opts Options) (T, error) {
	var cfg, zero T
	if t := reflect.TypeFor[T](); t.Kind() != reflect.Struct {
		return zero, errNotStruct("Parse", t)
	}
	if err := ParseInto(&cfg, opts); err != nil {
		return zero, err
	}
	return cfg, nil
}

// ParseOrExit is Parse for a program's main function. When the
// configuration is invalid, it prints every problem to stderr, one per
// line, and exits with status 1:
//
//	docuconf: 2 configuration problems:
//	  DATABASE_URL: is required but not set (missing_required)
//	  PORT: 70000 is above max 65535 (out_of_range)
//
// The problems also go to the termination log, as with Parse.
//
//	func main() {
//		cfg := docuconf.ParseOrExit[config.Config]()
//		...
//	}
func ParseOrExit[T any]() T {
	return ParseOrExitWithOptions[T](Options{})
}

// ParseOrExitWithOptions is ParseOrExit with options.
func ParseOrExitWithOptions[T any](opts Options) T {
	cfg, err := ParseWithOptions[T](opts)
	if err != nil {
		var verr *ValidationError
		if !errors.As(err, &verr) {
			// Parse writes violations to the termination log itself; a
			// declaration or I/O error is the reason the container stopped too.
			o := withTypeOptions(reflect.TypeFor[T](), opts)
			environ := o.Environment
			if environ == nil {
				environ = map[string]string{EnvTerminationLog: os.Getenv(EnvTerminationLog)}
			}
			writeTerminationMessage(o.TerminationLog, environ, err.Error(), loggerOf(o))
		}
		fmt.Fprintln(exitStderr, err)
		exitFunc(1)
	}
	return cfg
}

// exitFunc and exitStderr are replaced in tests.
var (
	exitFunc             = os.Exit
	exitStderr io.Writer = os.Stderr
)

func loggerOf(opts Options) *slog.Logger {
	if opts.Logger != nil {
		return opts.Logger
	}
	return slog.Default()
}

// ParseInto is Parse for an existing struct pointer.
func ParseInto(ptr any, opts Options) error {
	return load(ptr, opts, true)
}

// Validate checks a struct that the app has already parsed with
// caarlos0/env: it applies docuconf's constraints to the same environment
// and loads the struct's file inputs. It is for teams that keep calling
// env.Parse themselves.
func Validate(ptr any, opts Options) error {
	return load(ptr, opts, false)
}

func load(ptr any, opts Options, parse bool) error {
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("docuconf: expected a non-nil pointer to a struct, got %T", ptr)
	}
	opts = withTypeOptions(rv.Elem().Type(), opts)
	d, err := declare(rv.Elem().Type(), declOptions{prefix: opts.Prefix, funcMap: opts.FuncMap})
	if err != nil {
		return err
	}
	logger := loggerOf(opts)
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	interval := opts.WatchInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}

	environ, err := loadEnvironment(opts, logger)
	if err != nil {
		return err
	}
	warnUndeclared(d, environ, opts.Prefix, logger)
	res := checkVars(d.vars, environ, logger)
	viols, flagged, values := res.viols, res.flagged, res.raw

	if parse {
		err := env.ParseWithOptions(ptr, env.Options{Environment: environ, Prefix: opts.Prefix, FuncMap: opts.FuncMap})
		var agg env.AggregateError
		if errors.As(err, &agg) {
			for _, e := range agg.Errors {
				if v, ok := explainHostError(d, e, flagged); ok {
					viols = append(viols, v)
				}
			}
		} else if err != nil {
			return fmt.Errorf("docuconf: %w", err)
		}
	}

	root := opts.FileRoot
	if root == "" {
		root = environ[EnvFileRoot]
	}
	elem := rv.Elem()
	for _, f := range d.files {
		p := f.path
		if f.pathEnv != "" && environ[f.pathEnv] != "" {
			p = environ[f.pathEnv]
		}
		if root != "" {
			p = filepath.Join(root, p)
		}
		field, err := elem.FieldByIndexErr(f.index)
		if err != nil {
			return fmt.Errorf("docuconf: %s: %w", f.goPath, err)
		}
		b := &fileBinding{
			decl:     f,
			path:     p,
			now:      now,
			interval: interval,
			logger:   logger,
			password: func() (string, bool) {
				s, ok := values[f.passwordVar]
				return s, ok
			},
		}
		fv := field.Addr().Interface().(fileInput).bind(b)
		viols = append(viols, fv...)
		if f.deprecated != "" && len(fv) == 0 && field.Addr().Interface().(interface{ Present() bool }).Present() {
			logger.Warn("docuconf: deprecated file input is present", "input", f.name, "message", f.deprecated)
		}
	}

	if len(viols) == 0 {
		return nil
	}
	verr := &ValidationError{Violations: viols}
	writeTerminationMessage(opts.TerminationLog, environ, verr.Error(), logger)
	return verr
}

// environment returns a copy of the environment to load from: the
// process environment or Options.Environment, then the .env files.
func environment(opts Options) (map[string]string, error) {
	return loadEnvironment(opts, loggerOf(opts))
}

func loadEnvironment(opts Options, logger *slog.Logger) (map[string]string, error) {
	environ := opts.Environment
	if environ == nil {
		environ = env.ToMap(os.Environ())
	}
	environ = copyMap(environ)
	for _, p := range opts.DotEnv {
		vals, err := dotenv.Read(p)
		if errors.Is(err, fs.ErrNotExist) && !opts.DotEnvRequired {
			logger.Debug("docuconf: .env file not found, skipped", "path", p)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("docuconf: reading %s: %w", p, err)
		}
		for k, v := range vals {
			if _, set := environ[k]; !set {
				environ[k] = v
			}
		}
	}
	return environ, nil
}

// varResults is what checkVars found.
type varResults struct {
	viols   []Violation
	flagged map[string]bool   // names with a violation
	raw     map[string]string // raw values of the variables that are set
	typed   map[string]any    // typed values of the set variables without violations
}

// checkVars checks every variable that environ sets, and reports the
// required ones it does not. It first adapts environ to the spec, in
// place, before caarlos0/env reads it: an empty value is unset for every
// type but string, and bools accept true and false in any case.
func checkVars(vars []*varDecl, environ map[string]string, logger *slog.Logger) varResults {
	return checkLayeredVars(vars, environ, nil, logger)
}

// checkLayeredVars is checkVars with values from below the environment:
// a variable the environment does not set takes its layer's value (a
// profile default, or an overlay value checked like an env value), which
// also satisfies required.
func checkLayeredVars(vars []*varDecl, environ map[string]string, layers map[string]layer, logger *slog.Logger) varResults {
	for _, v := range vars {
		raw, ok := environ[v.name]
		if !ok {
			continue
		}
		if raw == "" && v.typ != typeString {
			delete(environ, v.name)
			continue
		}
		if v.typ == typeBool && (strings.EqualFold(raw, "true") || strings.EqualFold(raw, "false")) {
			environ[v.name] = strings.ToLower(raw)
		}
	}

	res := varResults{flagged: map[string]bool{}, raw: map[string]string{}, typed: map[string]any{}}
	fail := func(v *varDecl, code Code, msg string) {
		res.viols = append(res.viols, Violation{Input: v.name, Code: code, Message: msg})
		res.flagged[v.name] = true
	}
	for _, v := range vars {
		var raw string
		var items []string
		var ok bool
		indexed := (v.typ == typeList || v.typ == typeKeySet) && v.listEncoding == encIndexed
		if indexed {
			var missing int
			items, ok, missing = indexedItems(environ, v.name)
			if ok && missing >= 0 {
				fail(v, CodeInvalidType, fmt.Sprintf("items must be numbered from %s__0 with no gap, but %s__%d is not set", v.name, v.name, missing))
				continue
			}
		} else {
			raw, ok = environ[v.name]
		}
		if ok && v.expand {
			raw = os.Expand(raw, func(k string) string { return environ[k] })
		}
		l, layered := layers[v.name]
		if ok && layered && !l.fixed && !l.bad {
			logger.Warn("docuconf: variable is set in the environment and in an overlay; the environment wins", "name", v.name, "source", l.source)
		}
		if !ok && layered {
			if l.bad {
				res.flagged[v.name] = true
				continue
			}
			if l.fixed {
				res.typed[v.name] = l.typed
				continue
			}
			if v.deprecated != "" {
				logger.Warn("docuconf: deprecated variable is set", "name", v.name, "message", v.deprecated, "source", l.source)
			}
			var val any
			var vs []Violation
			if l.isList {
				val, vs = v.parseItems(l.items)
			} else {
				res.raw[v.name] = l.raw
				val, vs = v.parse(l.raw)
			}
			if len(vs) > 0 {
				res.viols = append(res.viols, vs...)
				res.flagged[v.name] = true
				continue
			}
			res.typed[v.name] = val
			continue
		}
		if !ok {
			if v.required {
				fail(v, CodeMissingRequired, "is required but not set")
			}
			continue
		}
		if v.deprecated != "" {
			logger.Warn("docuconf: deprecated variable is set", "name", v.name, "message", v.deprecated)
		}
		if v.secret {
			scheme := injectorScheme(raw)
			for _, item := range items {
				if scheme == "" {
					scheme = injectorScheme(item)
				}
			}
			if scheme != "" {
				// The injector should have replaced the reference before the
				// process started (SPEC §4.5.1). Never print the reference.
				fail(v, CodeInvalidType, fmt.Sprintf(
					"holds an unresolved %s reference; the injector that should resolve it did not run", scheme))
				continue
			}
		}
		if v.loadFile {
			data, err := os.ReadFile(raw)
			if err != nil {
				code := CodeFileMissing
				if errors.Is(err, fs.ErrPermission) {
					code = CodeFileUnreadable
				}
				fail(v, code, fmt.Sprintf("cannot read the file it names: %v", err))
				continue
			}
			raw = string(data)
		}
		if v.notEmpty && raw == "" {
			fail(v, CodeMissingRequired, "is set but empty")
			continue
		}
		res.raw[v.name] = raw
		var val any
		var vs []Violation
		if indexed {
			val, vs = v.parseItems(items)
		} else {
			val, vs = v.parse(raw)
		}
		if len(vs) > 0 {
			res.viols = append(res.viols, vs...)
			res.flagged[v.name] = true
			continue
		}
		res.typed[v.name] = val
		if !indexed && !v.loadFile && !v.expand {
			if canon, ok := hostForm(v, raw); ok {
				environ[v.name] = canon
			}
		}
	}
	return res
}

// hostForm returns a valid integer, or csv list of integers, in the form
// strconv parses for every Go kind. The spec's integers take a sign and
// leading zeros (SPEC §5), which strconv.ParseUint rejects, so caarlos0/env
// is given the canonical form of a value docuconf has already accepted.
func hostForm(v *varDecl, raw string) (string, bool) {
	canon := func(s string) string {
		n, _ := new(big.Int).SetString(s, 10)
		return n.String()
	}
	switch {
	case v.typ == typeInt:
		return canon(raw), true
	case v.typ == typeList && v.items == "int" && (v.listEncoding == encCSV || v.listEncoding == ""):
		items := strings.Split(raw, v.separator)
		for i, item := range items {
			items[i] = canon(item)
		}
		return strings.Join(items, v.separator), true
	}
	return "", false
}

// explainHostError turns an error from caarlos0/env into a violation,
// unless docuconf already reported the variable. It never includes the
// host's message for a secret, since parser errors quote the value.
func explainHostError(d *declaration, err error, flagged map[string]bool) (Violation, bool) {
	var notSet env.VarIsNotSetError
	var empty env.EmptyVarError
	var loadFile env.LoadFileContentError
	var parseErr env.ParseError
	switch {
	case errors.As(err, &notSet):
		if flagged[notSet.Key] {
			return Violation{}, false
		}
		return Violation{Input: notSet.Key, Code: CodeMissingRequired, Message: "is required but not set"}, true
	case errors.As(err, &empty):
		if flagged[empty.Key] {
			return Violation{}, false
		}
		return Violation{Input: empty.Key, Code: CodeMissingRequired, Message: "is set but empty"}, true
	case errors.As(err, &loadFile):
		if flagged[loadFile.Key] {
			return Violation{}, false
		}
		return Violation{Input: loadFile.Key, Code: CodeFileMissing, Message: "cannot read the file it names"}, true
	case errors.As(err, &parseErr):
		for _, v := range d.vars {
			if v.field == parseErr.Name && flagged[v.name] {
				return Violation{}, false
			}
		}
		for _, v := range d.vars {
			if v.field == parseErr.Name {
				msg := fmt.Sprintf("cannot be parsed as %v", parseErr.Type)
				if !v.secret {
					msg += ": " + parseErr.Err.Error()
				}
				return Violation{Input: v.name, Code: CodeInvalidType, Message: msg}, true
			}
		}
		return Violation{Input: parseErr.Name, Code: CodeInvalidType, Message: fmt.Sprintf("cannot be parsed as %v", parseErr.Type)}, true
	}
	return Violation{Input: "env", Code: CodeInvalidType, Message: err.Error()}, true
}

func writeTerminationLog(override string, environ map[string]string, verr *ValidationError, logger *slog.Logger) {
	writeTerminationMessage(override, environ, verr.Error(), logger)
}

// writeTerminationMessage writes the violations where Kubernetes reads a
// container's termination message.
func writeTerminationMessage(override string, environ map[string]string, msg string, logger *slog.Logger) {
	p := override
	if p == "" {
		p = environ[EnvTerminationLog]
	}
	if p == "-" {
		return
	}
	if p == "" {
		if _, err := os.Stat(defaultTerminationLog); err != nil {
			return
		}
		p = defaultTerminationLog
	}
	if len(msg) > 4096 { // Kubernetes keeps at most 4096 bytes
		msg = msg[:4093] + "..."
	}
	if err := os.WriteFile(p, []byte(msg+"\n"), 0o644); err != nil {
		logger.Warn("docuconf: cannot write termination log", "path", p, "error", err)
	}
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// injectorSchemes are the prefixes of references that an injector
// (Bank-Vaults vault-env, op run, vals) resolves before the app starts.
var injectorSchemes = []string{"vault:", "op://", "ref+"}

// injectorScheme returns the injector reference scheme raw starts with, or
// "" when raw is not such a reference.
func injectorScheme(raw string) string {
	for _, s := range injectorSchemes {
		if strings.HasPrefix(raw, s) {
			return s
		}
	}
	return ""
}

// warnUndeclared logs a warning for each set variable that is not
// declared but is a likely typo of a declared one, such as DATABSE_URL
// for DATABASE_URL. caarlos0/env ignores undeclared variables, so the
// typo would otherwise go unnoticed until the default surprises someone.
// It never logs a value. With a prefix, only variables with that prefix
// are considered.
func warnUndeclared(d *declaration, environ map[string]string, prefix string, logger *slog.Logger) {
	declared := map[string]bool{}
	var names []string
	for _, v := range d.vars {
		declared[v.name] = true
		names = append(names, strings.TrimPrefix(v.name, prefix))
	}
	for _, f := range d.files {
		if f.pathEnv != "" {
			declared[f.pathEnv] = true
		}
	}
	if len(names) == 0 {
		return
	}
	keys := make([]string, 0, len(environ))
	for k := range environ {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if declared[k] || strings.HasPrefix(k, "DOCUCONF_") || !strings.HasPrefix(k, prefix) {
			continue
		}
		if i := strings.LastIndex(k, "__"); i > 0 && declared[k[:i]] {
			continue // an item of an indexed list
		}
		if hint := closest(strings.TrimPrefix(k, prefix), names, 2); hint != "" {
			logger.Warn(fmt.Sprintf("docuconf: %s is set but not declared; did you mean %s?", k, prefix+hint))
		}
	}
}
