package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"cuelang.org/go/cue"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/lint"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/terraform"
)

// runLint checks a Terraform or OpenTofu plan against the contracts of the
// docuconf modules it uses (spec/IAC.md section 7).
//
//	docuconf lint terraform plan.json [-dir .] [-policy prod.cue] [-contract contract.cue] [-explain] [-format text|json|sarif]
func runLint(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "terraform" && args[0] != "tofu") {
		fmt.Fprint(stderr, "usage: docuconf lint terraform <plan.json> [-dir .] [-policy policy.cue] [-contract contract.cue] [-explain] [-format text|json|sarif]\n")
		return flag.ErrHelp
	}
	fs := flag.NewFlagSet("lint terraform", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", ".", "the Terraform root directory, where terraform init ran (for .terraform/modules/modules.json)")
	policyFile := fs.String("policy", "", "an environment policy, as for docuconf vet")
	contractFile := fs.String("contract", "", "the contract the modules should have been generated from")
	explain := fs.Bool("explain", false, "print how each input is bound")
	format := fs.String("format", "text", "text, json or sarif")
	// Flags may come before or after the plan file.
	var plans []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		plans = append(plans, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(plans) != 1 {
		return fmt.Errorf("give one plan file: terraform show -json plan.out > plan.json")
	}
	switch *format {
	case "text", "json", "sarif":
	default:
		return fmt.Errorf("-format %q: want text, json or sarif", *format)
	}
	plan, err := lint.Read(plans[0], *dir)
	if err != nil {
		return err
	}
	if len(plan.Modules) == 0 {
		return fmt.Errorf("%s uses no docuconf module; run docuconf lint where terraform init ran, or give -dir", plans[0])
	}
	p, err := platform.New()
	if err != nil {
		return err
	}
	findings, docs := lint.Check(plan)
	findings = append(findings, staleModules(plan)...)
	if *contractFile != "" {
		f, err := contractMismatch(p, plan, *contractFile)
		if err != nil {
			return err
		}
		findings = append(findings, f...)
	}
	var policy cue.Value
	if *policyFile != "" {
		if policy, err = p.LoadData(*policyFile); err != nil {
			return err
		}
	}
	for _, d := range docs {
		f, err := vetDocs(p, d, policy, *policyFile)
		if err != nil {
			return err
		}
		findings = append(findings, f...)
	}
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if levelRank[a.Level] != levelRank[b.Level] {
			return levelRank[a.Level] < levelRank[b.Level]
		}
		if a.Module != b.Module {
			return a.Module < b.Module
		}
		return a.Where < b.Where
	})
	switch *format {
	case "json":
		if findings == nil {
			findings = []lint.Finding{}
		}
		b, _ := json.MarshalIndent(map[string]any{"findings": findings}, "", "  ")
		fmt.Fprintf(stdout, "%s\n", b)
	case "sarif":
		writeSARIF(stdout, findings)
	default:
		if *explain {
			writeExplain(stdout, docs)
		}
		for _, f := range findings {
			fmt.Fprintln(stdout, textLine(f))
		}
	}
	for _, f := range findings {
		if f.Level == lint.Error {
			return errProblems
		}
	}
	return nil
}

var levelRank = map[string]int{lint.Error: 0, lint.Warning: 1, lint.Notice: 2}

func textLine(f lint.Finding) string {
	where := f.Module
	if f.Where != "" {
		where += " → " + f.Where
	}
	return fmt.Sprintf("%s: %s: %s (%s)", f.Level, where, f.Message, f.Code)
}

// vetDocs checks the documents the linter rebuilt with vet's engine: the
// contract's constraints, which HCL cannot all express, and the policy.
func vetDocs(p *platform.Platform, d lint.Docs, policy cue.Value, policyFile string) ([]lint.Finding, error) {
	c, err := p.ParseContract("contract.json", d.Module.Meta.Contract)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", d.Module.Addr, err)
	}
	doc := func(name string, v map[string]any) (cue.Value, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return cue.Value{}, err
		}
		return p.CompileJSON(name, b)
	}
	values, err := doc("values", d.Values)
	if err != nil {
		return nil, err
	}
	files, err := doc("files", d.Files)
	if err != nil {
		return nil, err
	}
	overlays, err := doc("overlays", d.Overlays)
	if err != nil {
		return nil, err
	}
	empty, _ := doc("policy", map[string]any{})
	base, warnings, err := p.ValidateWarn(c, values, files, overlays, empty)
	if err != nil {
		return nil, err
	}
	withPolicy := base
	if policy.Exists() {
		if withPolicy, _, err = p.ValidateWarn(c, values, files, overlays, policy); err != nil {
			return nil, err
		}
	}
	inBase := map[string]bool{}
	for _, l := range base {
		inBase[l] = true
	}
	var out []lint.Finding
	for _, l := range withPolicy {
		name, _, _ := strings.Cut(l, ":")
		if d.Deferred[name] {
			continue // its value is unknown; a deferred notice says so
		}
		f := lint.Finding{Code: "invalid_value", Level: lint.Error, Module: d.Module.Addr, Where: d.Where, Input: name, Message: l}
		switch {
		case !inBase[l]:
			f.Code = "policy_mismatch"
			f.Message = fmt.Sprintf("%s (policy %s)", l, filepath.Base(policyFile))
		case strings.Contains(l, "is required, and set neither") || strings.Contains(l, "is a required file input"):
			f.Code = "missing_required"
		}
		out = append(out, f)
	}
	for _, w := range warnings {
		out = append(out, lint.Finding{Code: "deprecated_set", Level: lint.Warning, Module: d.Module.Addr, Where: d.Where,
			Message: strings.TrimPrefix(w, "warning: ")})
	}
	return out, nil
}

// staleModules regenerates each module from the contract its docuconf.json
// holds: a module that differs was edited by hand, or generated by a
// docuconf that wrote it differently.
func staleModules(plan *lint.Plan) []lint.Finding {
	var out []lint.Finding
	for _, m := range plan.Modules {
		files, err := terraform.Generate(m.Contract, terraform.Options{Targets: m.Meta.Targets, GeneratorVersion: m.Meta.Generator.Version})
		if err != nil {
			out = append(out, lint.Finding{Code: "stale_module", Level: lint.Error, Module: m.Addr, Message: fmt.Sprintf("its contract no longer generates a module: %v", err)})
			continue
		}
		var differ []string
		for _, n := range sortedFileNames(files) {
			have, err := os.ReadFile(filepath.Join(m.Dir, n))
			if err != nil || !bytes.Equal(terraform.MaskVersion(n, have), terraform.MaskVersion(n, files[n])) {
				differ = append(differ, n)
			}
		}
		if len(differ) > 0 {
			out = append(out, lint.Finding{Code: "stale_module", Level: lint.Error, Module: m.Addr,
				Message: fmt.Sprintf("%s differs from what docuconf terraform generates from its docuconf.json (%s); regenerate it rather than edit it", m.Dir, strings.Join(differ, ", "))})
		}
	}
	return out
}

func sortedFileNames(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// contractMismatch compares each module generated for this contract's
// service with the contract itself.
func contractMismatch(p *platform.Platform, plan *lint.Plan, file string) ([]lint.Finding, error) {
	c, err := p.LoadContract(file)
	if err != nil {
		return nil, err
	}
	doc, err := p.ContractJSON(c)
	if err != nil {
		return nil, err
	}
	tc, err := terraform.Parse(doc)
	if err != nil {
		return nil, err
	}
	digest := terraform.Digest(doc)
	var out []lint.Finding
	found := false
	for _, m := range plan.Modules {
		if m.Contract.Name != tc.Name {
			continue
		}
		found = true
		if m.Meta.ContractDigest != digest {
			out = append(out, lint.Finding{Code: "stale_module", Level: lint.Error, Module: m.Addr,
				Message: fmt.Sprintf("was generated from another version of the %s contract; regenerate it: docuconf terraform -contract %s -out %s", tc.Name, file, m.Dir)})
		}
	}
	if !found {
		out = append(out, lint.Finding{Code: "stale_module", Level: lint.Error,
			Message: fmt.Sprintf("no module in the plan was generated from the %s contract (%s)", tc.Name, file)})
	}
	return out, nil
}

func writeExplain(w io.Writer, docs []lint.Docs) {
	for _, d := range docs {
		fmt.Fprintf(w, "%s → %s\n", d.Module.Addr, d.Where)
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "INPUT\tSOURCE\tORIGIN\tAT PLAN")
		for _, b := range d.Bindings {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.Input, b.Source, b.Origin, b.AtPlan)
		}
		tw.Flush()
		fmt.Fprintln(w)
	}
}

// writeSARIF writes the findings as SARIF 2.1.0, for code-scanning UIs.
func writeSARIF(w io.Writer, findings []lint.Finding) {
	level := map[string]string{lint.Error: "error", lint.Warning: "warning", lint.Notice: "note"}
	rules := map[string]bool{}
	var results []map[string]any
	for _, f := range findings {
		rules[f.Code] = true
		msg := f.Message
		if f.Where != "" {
			msg = f.Where + ": " + msg
		}
		results = append(results, map[string]any{
			"ruleId":  f.Code,
			"level":   level[f.Level],
			"message": map[string]any{"text": msg},
			"locations": []any{map[string]any{"logicalLocations": []any{map[string]any{
				"fullyQualifiedName": f.Module, "kind": "module",
			}}}},
		})
	}
	var ruleList []map[string]any
	for _, r := range sortedKeys(rules) {
		ruleList = append(ruleList, map[string]any{"id": r, "helpUri": "https://github.com/Docuconf/docuconf-go/blob/main/spec/IAC.md#72-rules"})
	}
	if results == nil {
		results = []map[string]any{}
	}
	b, _ := json.MarshalIndent(map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs": []any{map[string]any{
			"tool":    map[string]any{"driver": map[string]any{"name": "docuconf lint", "version": version, "rules": ruleList}},
			"results": results,
		}},
	}, "", "  ")
	fmt.Fprintf(w, "%s\n", b)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
