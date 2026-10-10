package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math/big"
	"slices"
	"strings"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// runConformanceExport compares an SDK's export of the shared fixture
// with the golden contract, as data (SPEC §11.2 item 3, §12):
//
//	docuconf conformance export --golden conformance/export/golden.cue exported.cue
//
// Both contracts are unified with the meta-schema, so a field left at its
// default compares equal to one written out. metadata.generator, every
// encoding and every configKey are the SDK's own and are ignored, and so is
// a list's or key set's separator unless both contracts use the csv
// encoding. Numbers
// compare by value (1 equals 1.0), and a JSON Schema compares without its
// annotations (title, $schema, $id, $comment, examples) and with its
// required lists as sets. Everything else, including the order of lists
// such as values and dnsNames, must match.
func runConformanceExport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("conformance export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	golden := fs.String("golden", "conformance/export/golden.cue", "the golden contract")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: docuconf conformance export [--golden golden.cue] exported.cue")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("give the exported contract")
	}
	p, err := platform.New()
	if err != nil {
		return err
	}
	load := func(file string) (any, error) {
		c, err := p.LoadContract(file)
		if err != nil {
			return nil, err
		}
		data, err := p.ContractJSON(c)
		if err != nil {
			return nil, err
		}
		var v any
		return v, decodeNumbers(data, &v)
	}
	want, err := load(*golden)
	if err != nil {
		return err
	}
	got, err := load(fs.Arg(0))
	if err != nil {
		return err
	}
	diffs := compareExport(want, got)
	if len(diffs) > 0 {
		for _, d := range diffs {
			fmt.Fprintln(stdout, d)
		}
		return fmt.Errorf("%s does not match %s: %d differences", fs.Arg(0), *golden, len(diffs))
	}
	fmt.Fprintf(stdout, "%s matches %s\n", fs.Arg(0), *golden)
	return nil
}

// compareExport lists the differences between the golden contract and an
// exported one, both decoded from the meta-schema's JSON.
func compareExport(want, got any) []string {
	// A separator only means something in the csv encoding.
	csv := func(c any, n string) bool {
		m, _ := c.(map[string]any)
		vars, _ := m["vars"].(map[string]any)
		v, _ := vars[n].(map[string]any)
		return v["encoding"] == "csv"
	}
	keepSeparator := map[string]bool{}
	if m, ok := want.(map[string]any); ok {
		vars, _ := m["vars"].(map[string]any)
		for n := range vars {
			keepSeparator[n] = csv(want, n) && csv(got, n)
		}
	}
	want, got = normalizeExport(want, keepSeparator), normalizeExport(got, keepSeparator)
	var out []string
	diffJSON("", want, got, &out)
	return out
}

// normalizeExport drops what the comparison ignores from a contract.
func normalizeExport(c any, keepSeparator map[string]bool) any {
	m, _ := c.(map[string]any)
	if m == nil {
		return c
	}
	if md, ok := m["metadata"].(map[string]any); ok {
		delete(md, "generator")
	}
	if vars, ok := m["vars"].(map[string]any); ok {
		for n, x := range vars {
			v, ok := x.(map[string]any)
			if !ok {
				continue
			}
			if !keepSeparator[n] {
				delete(v, "separator")
			}
			delete(v, "encoding")
			// configKey is the host's own binding key (Spring's relaxed
			// name, .NET's configuration path), so SDKs differ on it.
			delete(v, "configKey")
			if s, ok := v["schema"]; ok {
				v["schema"] = normalizeSchema(s)
			}
		}
	}
	if files, ok := m["files"].(map[string]any); ok {
		for _, x := range files {
			if f, ok := x.(map[string]any); ok {
				if s, ok := f["schema"]; ok {
					f["schema"] = normalizeSchema(s)
				}
			}
		}
	}
	return m
}

var schemaAnnotations = []string{"title", "$schema", "$id", "$comment", "examples"}

// normalizeSchema drops a JSON Schema's annotations and sorts its
// required lists, at every level.
func normalizeSchema(s any) any {
	switch x := s.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			if slices.Contains(schemaAnnotations, k) {
				continue
			}
			if k == "required" {
				if arr, ok := v.([]any); ok {
					sorted := slices.Clone(arr)
					slices.SortFunc(sorted, func(a, b any) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
					out[k] = sorted
					continue
				}
			}
			if k == "properties" || k == "$defs" || k == "definitions" || k == "patternProperties" {
				// A map of names to schemas: a property named "title" is not
				// an annotation.
				if props, ok := v.(map[string]any); ok {
					np := map[string]any{}
					for pk, pv := range props {
						np[pk] = normalizeSchema(pv)
					}
					out[k] = np
					continue
				}
			}
			out[k] = normalizeSchema(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = normalizeSchema(v)
		}
		return out
	}
	return s
}

// diffJSON appends one line per difference, by path.
func diffJSON(path string, want, got any, out *[]string) {
	show := func(v any) string {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
		return strings.TrimSpace(buf.String())
	}
	at := func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: golden %s, exported %s", path, show(want), show(got)))
			return
		}
		keys := map[string]bool{}
		for k := range w {
			keys[k] = true
		}
		for k := range g {
			keys[k] = true
		}
		for _, k := range slices.Sorted(maps.Keys(keys)) {
			wv, wok := w[k]
			gv, gok := g[k]
			switch {
			case !gok:
				*out = append(*out, fmt.Sprintf("%s: missing (golden %s)", at(k), show(wv)))
			case !wok:
				*out = append(*out, fmt.Sprintf("%s: not in the golden contract (exported %s)", at(k), show(gv)))
			default:
				diffJSON(at(k), wv, gv, out)
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			*out = append(*out, fmt.Sprintf("%s: golden %s, exported %s", path, show(want), show(got)))
			return
		}
		for i := range w {
			diffJSON(fmt.Sprintf("%s[%d]", path, i), w[i], g[i], out)
		}
	case json.Number:
		g, ok := got.(json.Number)
		wr, wok := new(big.Rat).SetString(string(w))
		var gr *big.Rat
		if ok {
			gr, ok = new(big.Rat).SetString(string(g))
		}
		if !ok || !wok || wr.Cmp(gr) != 0 {
			*out = append(*out, fmt.Sprintf("%s: golden %s, exported %s", path, show(want), show(got)))
		}
	default:
		if want != got {
			*out = append(*out, fmt.Sprintf("%s: golden %s, exported %s", path, show(want), show(got)))
		}
	}
}
