package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/pulumi"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/terraform"
)

// runPulumi generates a Pulumi component from a contract (spec/IAC.md 6).
// With -check it compares instead of writing, and exits 1 if the component
// is out of date.
func runPulumi(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "policy" {
		return runPulumiPolicy(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("pulumi", flag.ContinueOnError)
	fs.SetOutput(stderr)
	contract := fs.String("contract", "contract.cue", "the service's contract")
	lang := fs.String("lang", "typescript", "the component's language: typescript, go or python")
	pkg := fs.String("package", "", "a Go component's package name (default: the contract's name, then config); a Python one's is the -out directory's name")
	target := fs.String("target", "kubernetes", "the workload kind to render for: kubernetes, ecs, cloudrun, containerapps or lambda")
	out := fs.String("out", "", "the component directory to write into")
	check := fs.Bool("check", false, "compare with the component in -out instead of writing; exit 1 if it is out of date (generator.version is ignored)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	p, err := platform.New()
	if err != nil {
		return err
	}
	c, err := p.LoadContract(*contract)
	if err != nil {
		return err
	}
	doc, err := p.ContractJSON(c)
	if err != nil {
		return err
	}
	tc, err := terraform.Parse(doc)
	if err != nil {
		return err
	}
	if *lang == "python" && *pkg == "" {
		// A Python package is imported by its directory's name.
		*pkg = filepath.Base(filepath.Clean(*out))
	}
	files, err := pulumi.Generate(tc, pulumi.Options{Lang: *lang, Package: *pkg, Target: *target, GeneratorVersion: version,
		Warn: func(w string) { fmt.Fprintf(stderr, "docuconf pulumi: warning: %s\n", w) }})
	if err != nil {
		return err
	}
	regen := fmt.Sprintf("docuconf pulumi -contract %s -lang %s -target %s -out %s", *contract, *lang, *target, *out)
	return writeGenerated("docuconf pulumi", *out, files, *check, regen, terraform.MaskVersion, stdout, stderr)
}

// runPulumiPolicy generates a CrossGuard policy pack from a contract
// (spec/IAC.md 6.3). With -check it compares instead of writing.
func runPulumiPolicy(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pulumi policy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	contract := fs.String("contract", "contract.cue", "the service's contract")
	target := fs.String("target", "kubernetes", "the workload kind to check: kubernetes, ecs, cloudrun, containerapps or lambda")
	out := fs.String("out", "", "the policy pack directory to write into")
	check := fs.Bool("check", false, "compare with the policy pack in -out instead of writing; exit 1 if it is out of date (generator.version is ignored)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	p, err := platform.New()
	if err != nil {
		return err
	}
	c, err := p.LoadContract(*contract)
	if err != nil {
		return err
	}
	doc, err := p.ContractJSON(c)
	if err != nil {
		return err
	}
	tc, err := terraform.Parse(doc)
	if err != nil {
		return err
	}
	files, err := pulumi.GeneratePolicy(tc, pulumi.PolicyOptions{Target: *target, GeneratorVersion: version})
	if err != nil {
		return err
	}
	regen := fmt.Sprintf("docuconf pulumi policy -contract %s -target %s -out %s", *contract, *target, *out)
	return writeGenerated("docuconf pulumi policy", *out, files, *check, regen, terraform.MaskVersion, stdout, stderr)
}
