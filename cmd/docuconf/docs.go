package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/docs"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

const docsUsage = `Usage: docuconf docs <contract.cue | contract.json | docs.json> [--format model|markdown|agents] [-o file | --check file]

Generates documentation from a contract (SPEC §14). The contract becomes a
docs model, a versioned JSON document (--format model); the Markdown and
agents renderers read only that model, so a docs.json may be given instead
of a contract for them.

  --format markdown  reference for developers, such as CONFIG.md (default)
  --format agents    rules and per-input facts for AI agents, such as CONFIG.agents.md
  --format model     the docs model itself, such as docs.json

--check compares with a committed file instead of writing, and exits 1 with
a diff when it is out of date.

`

// runDocs implements docuconf docs: contract → model → renderer.
func runDocs(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("docs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, docsUsage)
		fs.PrintDefaults()
	}
	contract := fs.String("contract", "", "the contract (or docs model); may also be given as the first argument")
	format := fs.String("format", "markdown", "what to write: model, markdown or agents")
	out := fs.String("o", "", "write to this file instead of standard output")
	check := fs.String("check", "", "compare with this file instead of writing; exit 1 with a diff if it is out of date")
	// Flags may come before or after the contract.
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	switch {
	case len(positional) > 1:
		return fmt.Errorf("one contract at a time, got %s", strings.Join(positional, ", "))
	case len(positional) == 1 && *contract != "":
		return errors.New("give the contract as an argument or with -contract, not both")
	case len(positional) == 1:
		*contract = positional[0]
	case *contract == "":
		fs.Usage()
		return errors.New("no contract given")
	}
	if *out != "" && *check != "" {
		return errors.New("-o and --check cannot be used together")
	}
	render, ok := map[string]func(*docs.Model) ([]byte, error){
		"model":    docs.Encode,
		"markdown": func(m *docs.Model) ([]byte, error) { return docs.Markdown(m), nil },
		"agents":   func(m *docs.Model) ([]byte, error) { return docs.Agents(m), nil },
	}[*format]
	if !ok {
		return fmt.Errorf("unknown --format %q: want model, markdown or agents", *format)
	}

	m, err := loadDocsModel(*contract)
	if err != nil {
		return err
	}
	data, err := render(m)
	if err != nil {
		return err
	}

	switch {
	case *check != "":
		have, err := os.ReadFile(*check)
		if err != nil {
			return err
		}
		if bytes.Equal(have, data) {
			return nil
		}
		fmt.Fprintf(stderr, "%s is out of date (%s); regenerate it with: docuconf docs %s --format %s -o %s\n",
			*check, diffSummary(string(have), string(data)), *contract, *format, *check)
		writeDiff(stderr, *check, "generated now", string(have), string(data))
		return errStale
	case *out != "":
		return os.WriteFile(*out, data, 0o644)
	}
	_, err = stdout.Write(data)
	return err
}

// loadDocsModel reads a contract and builds its docs model, or reads a
// docs model. Either way the model is checked against #DocsModel, so the
// renderers only ever see a valid model.
func loadDocsModel(file string) (*docs.Model, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	p, err := platform.New()
	if err != nil {
		return nil, err
	}
	var data []byte
	if docs.IsModel(src) {
		data = src
	} else {
		c, err := p.ParseContract(file, src)
		if err != nil {
			return nil, err
		}
		contractJSON, err := p.ContractJSON(c)
		if err != nil {
			return nil, err
		}
		m, err := docs.Build(contractJSON)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if data, err = docs.Encode(m); err != nil {
			return nil, err
		}
	}
	if err := p.ValidateDocsModel(file, data); err != nil {
		return nil, err
	}
	return docs.Decode(data)
}

// diffSummary counts the lines that differ, for the first line of a
// --check failure.
func diffSummary(have, want string) string {
	count := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.SplitAfter(s, "\n") {
			m[l]++
		}
		return m
	}
	h, w := count(have), count(want)
	added, removed := 0, 0
	for l, n := range w {
		added += max(0, n-h[l])
	}
	for l, n := range h {
		removed += max(0, n-w[l])
	}
	return fmt.Sprintf("%s added, %s removed", lines(added), lines(removed))
}

func lines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}
