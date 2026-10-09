package docuconf_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docuconf/docuconf-go"
)

// supportedTags are the conformance capability tags the Go SDK has
// (conformance/README.md): it holds every 64-bit integer, validates json
// values against their schema, and knows the keySet type and deprecated
// inputs. It runs every case: TestConformance fails on a skip.
var supportedTags = map[string]bool{"int64": true, "json-schema": true, "key-set": true, "deprecated": true}

type conformanceCase struct {
	ID       string            `json:"id"`
	Source   string            `json:"source"`
	Requires []string          `json:"requires"`
	Contract json.RawMessage   `json:"contract"`
	Env      map[string]string `json:"env"`
	Expect   map[string]any    `json:"expect"`
	Errors   []struct {
		Var  string `json:"var"`
		Code string `json:"code"`
	} `json:"errors"`
}

// TestConformance runs the shared suite in conformance/cases.json (SPEC
// §12) through LoadContract. DOCUCONF_CONFORMANCE points at another
// cases.json.
func TestConformance(t *testing.T) {
	path := os.Getenv("DOCUCONF_CONFORMANCE")
	if path == "" {
		path = filepath.Join("conformance", "cases.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the conformance suite: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // expected ints are compared exactly
	var suite struct {
		Version int               `json:"version"`
		Cases   []conformanceCase `json:"cases"`
	}
	if err := dec.Decode(&suite); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	if suite.Version != 1 {
		t.Fatalf("%s: unsupported suite version %d", path, suite.Version)
	}
	if len(suite.Cases) == 0 {
		t.Fatalf("%s holds no cases", path)
	}

	skipped := map[string]int{}
	for _, c := range suite.Cases {
		t.Run(c.ID, func(t *testing.T) {
			for _, tag := range c.Requires {
				if !supportedTags[tag] {
					skipped[tag]++
					t.Skipf("requires %s", tag)
				}
			}
			if problems := runCase(t, c); len(problems) > 0 {
				t.Errorf("case %q (%s) failed:\n  %s", c.ID, c.Source, strings.Join(problems, "\n  "))
			}
		})
	}
	n := 0
	for _, k := range skipped {
		n += k
	}
	t.Logf("conformance: %d cases, %d skipped %v", len(suite.Cases), n, skipped)
	if n > 0 {
		t.Errorf("the Go SDK must run every case, but skipped %d: %v", n, skipped)
	}
}

func runCase(t *testing.T, c conformanceCase) []string {
	termLog := filepath.Join(t.TempDir(), "termination-log")
	env := c.Env
	if env == nil {
		env = map[string]string{}
	}
	vals, err := docuconf.LoadContract(c.Contract, docuconf.Options{Environment: env, TerminationLog: termLog})

	var problems []string
	if c.Expect != nil {
		if err != nil {
			return []string{fmt.Sprintf("expected success, got %v", err)}
		}
		for name, want := range c.Expect {
			got, ok := vals[name]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: missing from the result", name))
				continue
			}
			g, err := toJSONValue(got)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", name, err))
				continue
			}
			if !jsonEqual(want, g) {
				problems = append(problems, fmt.Sprintf("%s: got %s, want %s", name, show(g), show(want)))
			}
		}
		for name := range vals {
			if _, ok := c.Expect[name]; !ok {
				problems = append(problems, fmt.Sprintf("%s: in the result but not expected", name))
			}
		}
		return problems
	}

	var verr *docuconf.ValidationError
	if !errors.As(err, &verr) {
		return []string{fmt.Sprintf("expected a *ValidationError, got %v (values %v)", err, vals)}
	}
	want := map[string]bool{}
	for _, e := range c.Errors {
		want[e.Var+" "+e.Code] = true
	}
	got := map[string]bool{}
	for _, v := range verr.Violations {
		got[v.Input+" "+string(v.Code)] = true
	}
	for k := range want {
		if !got[k] {
			problems = append(problems, "missing error "+k)
		}
	}
	for k := range got {
		if !want[k] {
			problems = append(problems, "unexpected error "+k)
		}
	}
	if len(problems) > 0 {
		problems = append(problems, "errors were: "+verr.Error())
	}

	// No error output may contain a secret's raw value.
	logged, _ := os.ReadFile(termLog)
	for _, secret := range secretValues(t, c) {
		if strings.Contains(verr.Error(), secret) {
			problems = append(problems, fmt.Sprintf("the error contains the secret value %q", secret))
		}
		if bytes.Contains(logged, []byte(secret)) {
			problems = append(problems, fmt.Sprintf("the termination log contains the secret value %q", secret))
		}
	}
	return problems
}

// secretValues returns the raw environment values of the case's secret
// variables, including the items of indexed lists.
func secretValues(t *testing.T, c conformanceCase) []string {
	var contract struct {
		Vars map[string]struct {
			Secret bool `json:"secret"`
		} `json:"vars"`
	}
	if err := json.Unmarshal(c.Contract, &contract); err != nil {
		t.Fatalf("contract: %v", err)
	}
	var out []string
	for k, v := range c.Env {
		name, _, _ := strings.Cut(k, "__")
		if contract.Vars[k].Secret || contract.Vars[name].Secret {
			if v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// toJSONValue converts a typed value to its JSON form for comparison:
// durations in canonical Go form, numbers as json.Number.
func toJSONValue(v any) (any, error) {
	switch x := v.(type) {
	case nil, string, bool, json.Number:
		return x, nil
	case int64:
		return json.Number(strconv.FormatInt(x, 10)), nil
	case *big.Int:
		return json.Number(x.String()), nil
	case float64:
		return json.Number(strconv.FormatFloat(x, 'g', -1, 64)), nil
	case time.Duration:
		return canonicalDuration(x), nil
	case docuconf.KeySet:
		keys := make([]any, len(x))
		for i, k := range x.Keys() {
			keys[i] = k.Reveal()
		}
		return keys, nil
	case []string, []int64, []any:
		rv := reflect.ValueOf(x)
		out := make([]any, rv.Len())
		for i := range out {
			e, err := toJSONValue(rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			out[i] = e
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			j, err := toJSONValue(e)
			if err != nil {
				return nil, err
			}
			out[k] = j
		}
		return out, nil
	}
	return nil, fmt.Errorf("unexpected type %T", v)
}

// canonicalDuration writes the canonical form of SPEC §11.2 item 3,
// written independently of the SDK's own formatter.
func canonicalDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	var b strings.Builder
	if d < 0 {
		b.WriteByte('-')
		d = -d
	}
	for _, u := range []struct {
		d time.Duration
		s string
	}{{time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}, {time.Millisecond, "ms"}, {time.Microsecond, "us"}, {1, "ns"}} {
		if n := d / u.d; n > 0 {
			fmt.Fprintf(&b, "%d%s", n, u.s)
			d %= u.d
		}
	}
	return b.String()
}

// jsonEqual compares JSON values: integers exactly, other numbers
// numerically.
func jsonEqual(want, got any) bool {
	switch w := want.(type) {
	case json.Number:
		g, ok := got.(json.Number)
		if !ok {
			return false
		}
		wi, wok := new(big.Int).SetString(string(w), 10)
		gi, gok := new(big.Int).SetString(string(g), 10)
		if wok && gok {
			return wi.Cmp(gi) == 0
		}
		wf, err1 := w.Float64()
		gf, err2 := g.Float64()
		return err1 == nil && err2 == nil && wf == gf
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !jsonEqual(w[i], g[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, e := range w {
			if ge, ok := g[k]; !ok || !jsonEqual(e, ge) {
				return false
			}
		}
		return true
	}
	return want == got
}

func show(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
