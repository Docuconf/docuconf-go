// Command docuconf exports, validates and renders docuconf configuration
// contracts.
//
//	docuconf export -pkg ./internal/config -type Config -name billing-api -o contract.cue
//	docuconf export -pkg ./internal/config -check contract.cue
//	docuconf vet    -contract contract.cue -values values.yaml [-files files.yaml] [-policy policy.cue]
//	docuconf render -contract contract.cue -values values.yaml [-files files.yaml]
//	docuconf helm   -contract contract.cue -chart ./chart
//	docuconf check  -contract contract.cue
//	docuconf exec   -contract contract.cue -- program [args...]
//	docuconf docs   contract.cue [--format model|markdown|agents] [-o file | --check file]
//	docuconf diff   old.cue new.cue [--format text|json] [--allow-breaking] [--ack file]
//
// diff classifies every change between two contracts as compatible,
// notable or breaking (spec section 9), and exits 1 on a breaking change
// that is not acknowledged.
//
// docs generates documentation from a contract: a docs model (a
// versioned JSON document, spec section 14), and from the model Markdown
// for developers or a rules file for AI agents.
//
// check and exec validate the process environment and file inputs at
// boot with the Go SDK's contract-first loader, for programs written in a
// language without a docuconf SDK. exec then replaces itself with the
// program, passing it the environment it validated: the process
// environment, -env-file values for what that leaves unset, and the
// contract's defaults for what is still unset (unless -no-defaults).
//
// docuconf conformance regenerates conformance/cases.json from
// conformance/load, for maintainers of the spec.
//
// vet prints one line per problem and exits 1 if there is any. It also
// prints a warning line for each deprecated input the values still set,
// which alone does not fail it. Secret values are never printed. The contract meta-schema is built in.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"cuelang.org/go/cue"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

const usage = `docuconf: typed configuration contracts between apps and the platform.

Usage:
  docuconf export -pkg <package> [-type Config] [-name <service>] [-o contract.cue | -check contract.cue]
  docuconf vet    -contract <contract.cue> [-values values.yaml] [-files files.yaml] [-overlays overlays.yaml] [-policy policy.cue]
  docuconf render -contract <contract.cue> [-values values.yaml] [-files files.yaml] [-overlays overlays.yaml]
  docuconf helm   -contract <contract.cue> -chart <chart directory>
  docuconf check  -contract <contract.cue> [-env-file .env]
  docuconf exec   -contract <contract.cue> [-env-file .env] [-no-defaults] -- <program> [args...]
  docuconf docs   <contract.cue | docs.json> [--format model|markdown|agents] [-o file | --check file]
  docuconf diff   <old.cue | -> <new.cue | -> [--format text|json] [--allow-breaking] [--ack file]

Run "docuconf <command> -h" for a command's flags.
`

// errProblems means vet found problems; they have been printed already.
var errProblems = errors.New("configuration problems found")

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes a command and returns the exit code: 0 on success, 1 when
// the configuration has problems (or diff finds a breaking change), 2 on
// usage or I/O errors, 127 when exec cannot start its program.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "export":
		err = runExport(args[1:], stdout, stderr)
	case "vet":
		err = runVet(args[1:], stdout, stderr)
	case "render":
		err = runRender(args[1:], stdout, stderr)
	case "helm":
		err = runHelm(args[1:], stdout, stderr)
	case "check":
		err = runCheck(args[1:], stdout, stderr)
	case "exec":
		err = runExec(args[1:], stdout, stderr)
	case "docs":
		err = runDocs(args[1:], stdout, stderr)
	case "diff":
		err = runDiff(args[1:], stdout, stderr)
	case "conformance":
		err = runConformance(args[1:], stdout, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "docuconf: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errProblems), errors.Is(err, errStale), errors.Is(err, errBreaking):
		return 1
	case errors.Is(err, errExec):
		return 127
	case errors.Is(err, flag.ErrHelp):
		return 0
	}
	fmt.Fprintf(stderr, "docuconf %s: %v\n", args[0], err)
	return 2
}

type platformFlags struct {
	contract, values, files, overlays, policy string
}

func (p *platformFlags) register(fs *flag.FlagSet, policy bool) {
	fs.StringVar(&p.contract, "contract", "contract.cue", "the service's contract")
	fs.StringVar(&p.values, "values", "", "variable values (YAML, JSON or CUE): a map of VAR: value,\nor VAR: {secretKeyRef: {name: <secret>, key: <key>}} for a secret")
	fs.StringVar(&p.files, "files", "", "where each file input comes from (YAML, JSON or CUE), by input name:\n<input>: {secret: {name}}, {configMap: {name, key}}, {inline: <content>},\n{certificate: {name, secretName, dnsNames, duration, renewBefore}} or {csi: {secretProviderClass}}")
	fs.StringVar(&p.overlays, "overlays", "", "values for config-file overlays, by overlay name (YAML, JSON or CUE)")
	if policy {
		fs.StringVar(&p.policy, "policy", "", "environment policy unified with the values (CUE)")
	}
}

type loaded struct {
	p                               *platform.Platform
	c                               *platform.Contract
	values, files, overlays, policy cue.Value
}

func (pf *platformFlags) load() (*loaded, error) {
	p, err := platform.New()
	if err != nil {
		return nil, err
	}
	l := &loaded{p: p}
	if l.c, err = p.LoadContract(pf.contract); err != nil {
		return nil, err
	}
	if l.values, err = p.LoadData(pf.values); err != nil {
		return nil, err
	}
	if l.files, err = p.LoadData(pf.files); err != nil {
		return nil, err
	}
	if l.overlays, err = p.LoadData(pf.overlays); err != nil {
		return nil, err
	}
	if pf.policy != "" {
		if l.policy, err = p.LoadData(pf.policy); err != nil {
			return nil, err
		}
	}
	return l, nil
}

func runVet(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("vet", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var pf platformFlags
	pf.register(fs, true)
	if err := fs.Parse(args); err != nil {
		return err
	}
	l, err := pf.load()
	if err != nil {
		return err
	}
	lines, warnings, err := l.p.ValidateWarn(l.c, l.values, l.files, l.overlays, l.policy)
	if err != nil {
		return err
	}
	// Warnings do not fail vet: a deprecated input still works, but the
	// platform should stop setting it before the app removes it.
	for _, w := range warnings {
		fmt.Fprintln(stdout, w)
	}
	if len(lines) == 0 {
		fmt.Fprintf(stdout, "%s: ok\n", l.c.Name)
		return nil
	}
	for _, line := range lines {
		fmt.Fprintln(stdout, line)
	}
	return errProblems
}

func runRender(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var pf platformFlags
	pf.register(fs, true)
	if err := fs.Parse(args); err != nil {
		return err
	}
	l, err := pf.load()
	if err != nil {
		return err
	}
	// Rendering invalid values would hand the cluster a broken pod.
	lines, err := l.p.Validate(l.c, l.values, l.files, l.overlays, l.policy)
	if err != nil {
		return err
	}
	if len(lines) > 0 {
		for _, line := range lines {
			fmt.Fprintln(stderr, line)
		}
		return errProblems
	}
	out, err := l.p.Render(l.c, l.values, l.files, l.overlays)
	if err != nil {
		return err
	}
	_, err = stdout.Write(out)
	return err
}

// runHelm writes what a chart using the docuconf library chart needs:
// files/docuconf/contract.json, which the library renders from, and
// values.schema.json, which Helm checks values against on every lint,
// template, install and upgrade.
func runHelm(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("helm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	contract := fs.String("contract", "contract.cue", "the service's contract")
	chart := fs.String("chart", ".", "the chart directory to write into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := platform.New()
	if err != nil {
		return err
	}
	c, err := p.LoadContract(*contract)
	if err != nil {
		return err
	}
	schema, err := p.HelmValuesSchema(c)
	if err != nil {
		return err
	}
	doc, err := p.ContractJSON(c)
	if err != nil {
		return err
	}
	for _, out := range []struct {
		name string
		data []byte
	}{
		{filepath.Join("files", "docuconf", "contract.json"), doc},
		{"values.schema.json", schema},
	} {
		path, data := filepath.Join(*chart, out.name), out.data
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s\n", path)
	}
	return nil
}
