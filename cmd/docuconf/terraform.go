package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/terraform"
)

// runTerraform generates a Terraform or OpenTofu module from a contract
// (spec/IAC.md 4). With -check it compares instead of writing, and exits 1
// if the module is out of date.
func runTerraform(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("terraform", flag.ContinueOnError)
	fs.SetOutput(stderr)
	contract := fs.String("contract", "contract.cue", "the service's contract")
	target := fs.String("target", "kubernetes", "the workload kind to render for: kubernetes, ecs or cloudrun")
	out := fs.String("out", "", "the module directory to write into")
	check := fs.Bool("check", false, "compare with the module in -out instead of writing; exit 1 if it is out of date (generator.version is ignored)")
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
	files, err := terraform.Generate(tc, terraform.Options{Targets: strings.Split(*target, ","), GeneratorVersion: version,
		Warn: func(w string) { fmt.Fprintf(stderr, "docuconf terraform: warning: %s\n", w) }})
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	extra, err := staleGenerated(*out, files)
	if err != nil {
		return err
	}
	if *check {
		var stale []string
		for _, n := range names {
			path := filepath.Join(*out, n)
			have, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			want := files[n]
			if bytes.Equal(terraform.MaskVersion(n, have), terraform.MaskVersion(n, want)) {
				continue
			}
			stale = append(stale, n)
			writeDiff(stderr, path, "generated now", string(have), string(want))
		}
		for _, n := range extra {
			stale = append(stale, n+" (no longer generated)")
		}
		if len(stale) > 0 {
			fmt.Fprintf(stderr, "docuconf terraform: %s is out of date: %s; regenerate it with: docuconf terraform -contract %s -target %s -out %s\n",
				*out, strings.Join(stale, ", "), *contract, *target, *out)
			return errStale
		}
		return nil
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for _, n := range names {
		path := filepath.Join(*out, n)
		if err := os.WriteFile(path, files[n], 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s\n", path)
	}
	for _, n := range extra {
		path := filepath.Join(*out, n)
		if err := os.Remove(path); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed %s\n", path)
	}
	return nil
}

// staleGenerated lists the files in dir that docuconf generated, by their
// header, but would not write now.
func staleGenerated(dir string, files map[string][]byte) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || files[e.Name()] != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if bytes.HasPrefix(b, []byte(terraform.Header)) || bytes.HasPrefix(b, []byte(terraform.MarkdownHeader)) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}
