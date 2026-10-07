package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	sdk "github.com/docuconf/docuconf-go"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// errExec means the program could not be started; the reason has been
// printed already. It exits 127, as a shell does for a missing command.
var errExec = errors.New("cannot start the program")

type bootFlags struct {
	contract string
	dotEnv   stringList
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func (b *bootFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&b.contract, "contract", "contract.cue", "the service's contract")
	fs.Var(&b.dotEnv, "env-file", "a .env file to read, for local runs (repeatable; the environment wins)")
}

// boot validates the process environment and the contract's file inputs
// with the Go SDK's contract-first loader (sdk.LoadContract), so a
// program in any language gets the checks an SDK would run at startup.
// It prints every violation, never a secret value, and writes them to
// the termination log (DOCUCONF_TERMINATION_LOG, else
// /dev/termination-log when it exists). File paths honour pathEnv and
// DOCUCONF_FILE_ROOT.
func boot(b *bootFlags, stderr io.Writer) (name string, err error) {
	p, err := platform.New()
	if err != nil {
		return "", err
	}
	c, err := p.LoadContract(b.contract)
	if err != nil {
		return "", err
	}
	doc, err := p.ContractJSON(c)
	if err != nil {
		return "", err
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	_, err = sdk.LoadContract(doc, sdk.Options{DotEnv: b.dotEnv, Logger: logger})
	var verr *sdk.ValidationError
	if errors.As(err, &verr) {
		fmt.Fprintf(stderr, "docuconf: %s: %s\n", c.Name, strings.TrimPrefix(verr.Error(), "docuconf: "))
		return c.Name, errProblems
	}
	if err != nil {
		return c.Name, fmt.Errorf("%s: %w", b.contract, err)
	}
	return c.Name, nil
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
	name, err := boot(&b, stderr)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: ok\n", name)
	return nil
}

// runExec validates, then replaces itself with the program, so the
// program gets docuconf's PID and every signal sent to the container.
// The environment is passed on unchanged.
func runExec(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var b bootFlags
	b.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return errors.New("no program given; usage: docuconf exec -contract contract.cue -- program [args...]")
	}
	if _, err := boot(&b, stderr); err != nil {
		return err
	}
	if err := execProgram(argv); err != nil {
		fmt.Fprintf(stderr, "docuconf exec: %v\n", err)
		return errExec
	}
	return nil
}
