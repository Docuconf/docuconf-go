package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	sdk "github.com/docuconf/docuconf-go"
	"github.com/docuconf/docuconf-go/internal/dotenv"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// errExec means the program could not be started; the reason has been
// printed already. It exits 127, as a shell does for a missing command.
var errExec = errors.New("cannot start the program")

type bootFlags struct {
	contract   string
	dotEnv     stringList
	noDefaults bool
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func (b *bootFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&b.contract, "contract", "contract.cue", "the service's contract")
	fs.Var(&b.dotEnv, "env-file", "a .env file to read, for local runs (repeatable; the environment wins over it, and exec passes its values to the program)")
}

// booted is what boot validated: the contract and the environment it
// checked, which exec hands to the program.
type booted struct {
	p       *platform.Platform
	c       *platform.Contract
	doc     []byte
	environ map[string]string
}

// boot validates the environment and the contract's file inputs with the
// Go SDK's contract-first loader (sdk.LoadContract), so a program in any
// language gets the checks an SDK would run at startup. The environment
// is the process environment plus the -env-file files, which fill only
// the variables the process environment does not set; exactly this
// environment is validated, and exec passes it on. boot prints every
// violation, never a secret value, and writes them to the termination log
// (DOCUCONF_TERMINATION_LOG, else /dev/termination-log when it exists).
// File paths honour pathEnv and DOCUCONF_FILE_ROOT.
func boot(b *bootFlags, stderr io.Writer) (*booted, error) {
	p, err := platform.New()
	if err != nil {
		return nil, err
	}
	c, err := p.LoadContract(b.contract)
	if err != nil {
		return nil, err
	}
	doc, err := p.ContractJSON(c)
	if err != nil {
		return nil, err
	}
	environ := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			if _, dup := environ[k]; !dup {
				environ[k] = v
			}
		}
	}
	for _, f := range b.dotEnv {
		vals, err := dotenv.Read(f)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("-env-file %s: no such file", f)
		}
		if err != nil {
			return nil, fmt.Errorf("-env-file %w", err)
		}
		for k, v := range vals {
			if _, set := environ[k]; !set {
				environ[k] = v
			}
		}
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	_, err = sdk.LoadContract(doc, sdk.Options{Environment: environ, Logger: logger})
	var verr *sdk.ValidationError
	if errors.As(err, &verr) {
		fmt.Fprintln(stderr, verr.Error())
		return nil, errProblems
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.contract, err)
	}
	return &booted{p: p, c: c, doc: doc, environ: environ}, nil
}

// applyDefaults sets every unset variable that has a default to that
// default, in the variable's wire encoding (a list in its csv separator,
// json or indexed form; a duration as go, iso8601, seconds or timespan),
// rendered by the same #Render the platform uses. A file input whose
// pathEnv is unset gets the path that was checked. A program without an
// SDK then sees the configuration the contract describes, without
// repeating every default.
func (bt *booted) applyDefaults() error {
	var contract struct {
		Vars map[string]struct {
			Type     string          `json:"type"`
			Encoding string          `json:"encoding"`
			Default  json.RawMessage `json:"default"`
		} `json:"vars"`
		Files map[string]struct {
			Path    string `json:"path"`
			PathEnv string `json:"pathEnv"`
		} `json:"files"`
	}
	if err := json.Unmarshal(bt.doc, &contract); err != nil {
		return err
	}
	defaults := map[string]json.RawMessage{}
	for name, v := range contract.Vars {
		if v.Default == nil || bt.isSet(name, v.Type, v.Encoding) {
			continue
		}
		defaults[name] = v.Default
	}
	if len(defaults) > 0 {
		data, err := json.Marshal(defaults)
		if err != nil {
			return err
		}
		values, err := bt.p.CompileJSON("defaults.json", data)
		if err != nil {
			return err
		}
		env, err := bt.p.RenderEnv(bt.c, values)
		if err != nil {
			return err
		}
		for _, e := range env {
			if e.Value != nil {
				// The renderer doubles $ for Kubernetes, which reduces $$ to $.
				bt.environ[e.Name] = strings.ReplaceAll(*e.Value, "$$", "$")
			}
		}
	}
	root := bt.environ[sdk.EnvFileRoot]
	for _, f := range contract.Files {
		if f.PathEnv == "" || bt.environ[f.PathEnv] != "" {
			continue
		}
		bt.environ[f.PathEnv] = filepath.Join(root, f.Path)
	}
	return nil
}

// isSet reports whether the environment sets a variable, as SPEC §5 has
// it: an empty value is unset for every type but string, and an indexed
// list is set when any NAME__<n> is.
func (bt *booted) isSet(name, typ, encoding string) bool {
	if v, ok := bt.environ[name]; ok && (v != "" || typ == "string") {
		return true
	}
	if (typ == "list" || typ == "keySet") && encoding == "indexed" {
		for k, v := range bt.environ {
			if n, ok := strings.CutPrefix(k, name+"__"); ok && v != "" && isIndex(n) {
				return true
			}
		}
	}
	return false
}

func isIndex(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (bt *booted) env() []string {
	out := make([]string, 0, len(bt.environ))
	for k, v := range bt.environ {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// runCheck validates and exits: 0 when the configuration is valid, 1 when
// it is not. For init containers and CI.
func runCheck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var b bootFlags
	b.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q; use docuconf exec to start a program", fs.Args())
	}
	bt, err := boot(&b, stderr)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: ok\n", bt.c.Name)
	return nil
}

// runExec validates, then replaces itself with the program, so the
// program gets docuconf's PID and every signal sent to the container.
// The program's environment is the one that was validated: the process
// environment, the -env-file values it does not set, and, unless
// -no-defaults, the contract's defaults for the variables still unset.
func runExec(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var b bootFlags
	b.register(fs)
	fs.BoolVar(&b.noDefaults, "no-defaults", false, "do not export the contract's defaults for unset variables")
	if err := fs.Parse(args); err != nil {
		return err
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return errors.New("no program given; usage: docuconf exec -contract contract.cue -- program [args...]")
	}
	bt, err := boot(&b, stderr)
	if err != nil {
		return err
	}
	if !b.noDefaults {
		if err := bt.applyDefaults(); err != nil {
			return fmt.Errorf("%s: applying defaults: %w", b.contract, err)
		}
	}
	if err := execProgram(argv, bt.env()); err != nil {
		fmt.Fprintf(stderr, "docuconf exec: cannot start %s: %s\n", argv[0], startProblem(err))
		return errExec
	}
	return nil
}

// startProblem says why a program could not be started, in words rather
// than Go's wrapped errors.
func startProblem(err error) string {
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return "not found in PATH"
	case errors.Is(err, fs.ErrNotExist):
		return "no such file"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied (is it executable?)"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	return err.Error()
}
