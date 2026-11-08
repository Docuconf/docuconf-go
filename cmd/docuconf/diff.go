package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/diff"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

const diffUsage = `usage: docuconf diff <old contract> <new contract> [-accept INPUT,...] [-format text|json] [-all]

Classifies every change between two versions of a contract (spec/SPEC.md
section 9): breaking, when values or file sources the platform sets today
may no longer validate; notable, when nothing fails but the platform should
know; compatible otherwise. A contract is a file, or oci://IMAGE.

It exits 1 when there is a breaking change that -accept does not name, so
CI can block it until someone acknowledges it:

  git show origin/main:contract.cue > /tmp/base.cue
  docuconf diff /tmp/base.cue contract.cue
  docuconf diff /tmp/base.cue contract.cue -accept PORT,serving-tls

Flags:
`

// runDiff compares two contracts (spec/SPEC.md section 9).
func runDiff(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	accept := fs.String("accept", "", "inputs whose breaking changes are acknowledged, comma-separated; * for all")
	format := fs.String("format", "text", "text or json")
	all := fs.Bool("all", false, "also print compatible changes")
	fs.Usage = func() {
		fmt.Fprint(stderr, diffUsage)
		fs.PrintDefaults()
	}
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return flag.ErrHelp
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("-format %q: want text or json", *format)
	}
	p, err := platform.New()
	if err != nil {
		return err
	}
	load := func(ref string) ([]byte, error) {
		c, _, err := loadContract(p, ref)
		if err != nil {
			return nil, err
		}
		return p.ContractJSON(c)
	}
	oldJSON, err := load(fs.Arg(0))
	if err != nil {
		return err
	}
	newJSON, err := load(fs.Arg(1))
	if err != nil {
		return err
	}
	changes, err := diff.Contracts(oldJSON, newJSON)
	if err != nil {
		return err
	}
	accepted := map[string]bool{}
	for _, a := range strings.Split(*accept, ",") {
		if a = strings.TrimSpace(a); a != "" {
			accepted[a] = true
		}
	}
	isAccepted := func(c diff.Change) bool {
		if accepted["*"] {
			return true
		}
		key := c.Input
		if c.Kind == "contract" {
			key = c.Field
		}
		if c.Kind == "profile" && c.Input == "" {
			key = "profiles"
		}
		return accepted[key]
	}
	type row struct {
		diff.Change
		Accepted bool `json:"accepted,omitempty"`
	}
	var rows []row
	unaccepted := 0
	for _, c := range changes {
		r := row{Change: c}
		if c.Class == diff.Breaking {
			r.Accepted = isAccepted(c)
			if !r.Accepted {
				unaccepted++
			}
		}
		rows = append(rows, r)
	}
	switch *format {
	case "json":
		if rows == nil {
			rows = []row{}
		}
		b, _ := json.MarshalIndent(map[string]any{"changes": rows, "unacceptedBreaking": unaccepted}, "", "  ")
		fmt.Fprintf(stdout, "%s\n", b)
	default:
		shown := 0
		for _, r := range rows {
			if r.Class == diff.Compatible && !*all {
				continue
			}
			shown++
			who := r.Input
			switch {
			case r.Kind == "contract":
				who = "contract"
			case r.Kind == "profile" && who == "":
				who = "profiles"
			case r.Kind == "profile":
				who = "profile " + who
			case r.Kind == "overlay":
				who = "overlay " + who
			}
			class := r.Class
			if r.Accepted {
				class += " (accepted)"
			}
			fmt.Fprintf(stdout, "%s: %s: %s\n", class, who, r.Message)
		}
		if shown == 0 {
			fmt.Fprintln(stdout, "no breaking or notable changes")
		}
		if unaccepted > 0 {
			fmt.Fprintf(stderr, "docuconf diff: %d breaking change%s; land the platform's values first, then acknowledge with -accept\n", unaccepted, map[bool]string{true: "", false: "s"}[unaccepted == 1])
		}
	}
	if unaccepted > 0 {
		return errProblems
	}
	return nil
}
