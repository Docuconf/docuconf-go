package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the want sections of testdata/diff")

// contractOf wraps a fragment of vars, files, overlays and profiles in a
// contract.
func contractOf(fragment string) string {
	return `package svc

import "docuconf.dev/contract"

contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "svc", generator: {language: "go", sdk: "docuconf-go", version: "0.0.0"}}
	vars: {}
` + fragment + `
}
`
}

// TestDiffCases runs testdata/diff/*.txtar ("go test -run TestDiffCases
// -update" rewrites what they want). Each archive's comment is
// "exit N"; it holds the old and new contract fragments and the text
// output wanted. One case per row of SPEC §9 (row01..row16), and edge
// cases.
func TestDiffCases(t *testing.T) {
	files, err := filepath.Glob("testdata/diff/*.txtar")
	if err != nil || len(files) == 0 {
		t.Fatalf("no cases: %v", err)
	}
	for _, file := range files {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".txtar"), func(t *testing.T) {
			comment, sections := parseCase(t, file)
			oldPath := write(t, "old.cue", contractOf(sections["old"]))
			newPath := write(t, "new.cue", contractOf(sections["new"]))
			out, errOut, code := docuconf(t, "diff", oldPath, newPath)
			if errOut != "" {
				t.Fatalf("stderr:\n%s", errOut)
			}
			if *update {
				data := fmt.Sprintf("exit %d\n-- old --\n%s-- new --\n%s-- want --\n%s", code, sections["old"], sections["new"], out)
				if err := os.WriteFile(file, []byte(data), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			var wantCode int
			fmt.Sscanf(comment, "exit %d", &wantCode)
			if code != wantCode || out != sections["want"] {
				t.Fatalf("exit %d, want %d\ngot:\n%s\nwant:\n%s", code, wantCode, out, sections["want"])
			}
		})
	}
}

// parseCase reads a txtar archive: a comment, then "-- name --" sections.
func parseCase(t *testing.T, file string) (comment string, sections map[string]string) {
	t.Helper()
	sections = map[string]string{}
	name := ""
	for _, line := range strings.SplitAfter(read(t, file), "\n") {
		if h := strings.TrimSpace(line); strings.HasPrefix(h, "-- ") && strings.HasSuffix(h, " --") {
			name = strings.TrimSuffix(strings.TrimPrefix(h, "-- "), " --")
			sections[name] = ""
			continue
		}
		if name == "" {
			comment += line
		} else {
			sections[name] += line
		}
	}
	return comment, sections
}

const ordersContract = "../../examples/orders/contract.cue"

// ordersModified is the orders example with a lowered max, a removed
// enum value, a new required variable and a new required DNS name.
func ordersModified(t *testing.T) string {
	src := read(t, ordersContract)
	for _, r := range [][2]string{
		{`max:         64`, `max:         32`},
		{`values: ["debug", "info", "warn", "error"]`, `values: ["info", "warn", "error"]`},
		{`dnsNames: ["orders.example.com"]`, `dnsNames: ["orders.example.com", "orders.internal"]`},
		{`description: "Minimum log level emitted"`, `description: "Minimum level of log lines emitted"`},
	} {
		if !strings.Contains(src, r[0]) {
			t.Fatalf("orders contract has no %q", r[0])
		}
		src = strings.Replace(src, r[0], r[1], 1)
	}
	return write(t, "contract.cue", src)
}

func TestDiffOrders(t *testing.T) {
	newPath := ordersModified(t)
	out, _, code := docuconf(t, "diff", ordersContract, newPath)
	want := `ok        LOG_LEVEL: description changed (docs only) [description-changed]
BREAKING  LOG_LEVEL: enum values removed: "debug" [values-tightened]
BREAKING  WORKER_COUNT: max lowered from 64 to 32 [max-tightened]
BREAKING  serving-tls: dnsNames added: "orders.internal"; the existing certificate may not cover them [dnsNames-tightened]
orders-api: 3 breaking, 0 notable, 1 compatible
`
	if code != 1 || out != want {
		t.Fatalf("exit %d\n%s\nwant:\n%s", code, out, want)
	}

	// --allow-breaking still prints, but exits 0.
	out2, _, code := docuconf(t, "diff", "--allow-breaking", ordersContract, newPath)
	if code != 0 || out2 != out {
		t.Fatalf("--allow-breaking: exit %d\n%s", code, out2)
	}

	// JSON for bots.
	out, _, code = docuconf(t, "diff", ordersContract, newPath, "--format", "json")
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || code != 1 || len(got) != 4 {
		t.Fatalf("exit %d, %v:\n%s", code, err, out)
	}
	if g := got[2]; g["input"] != "WORKER_COUNT" || g["change"] != "max-tightened" || g["class"] != "breaking" ||
		g["reason"] != "max lowered from 64 to 32" {
		t.Fatalf("json change: %v", g)
	}

}

func TestDiffStdin(t *testing.T) {
	newPath := ordersModified(t)
	for _, src := range []string{read(t, ordersContract), ordersJSON(t)} {
		stdin = strings.NewReader(src)
		out, errOut, code := docuconf(t, "diff", "-", newPath)
		stdin = os.Stdin
		if code != 1 || !strings.Contains(out, "orders-api: 3 breaking, 0 notable, 1 compatible") {
			t.Fatalf("exit %d\n%s%s", code, out, errOut)
		}
	}
}

// ordersJSON is the orders contract as JSON, as docuconf helm writes it.
func ordersJSON(t *testing.T) string {
	dir := t.TempDir()
	if _, errOut, code := docuconf(t, "helm", "-contract", ordersContract, "-chart", dir); code != 0 {
		t.Fatal(errOut)
	}
	return read(t, filepath.Join(dir, "files", "docuconf", "contract.json"))
}

func TestDiffAck(t *testing.T) {
	newPath := ordersModified(t)
	all := write(t, "ack.txt", `# accepted in platform PR #142
LOG_LEVEL values-tightened
WORKER_COUNT    max-tightened
serving-tls dnsNames-tightened
`)
	out, errOut, code := docuconf(t, "diff", "--ack", all, ordersContract, newPath)
	if code != 0 || errOut != "" || !strings.Contains(out, "[max-tightened, acknowledged]") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}

	some := write(t, "ack.txt", "WORKER_COUNT max-tightened\nPORT var-removed\n")
	out, errOut, code = docuconf(t, "diff", "--ack", some, ordersContract, newPath)
	if code != 1 || !strings.Contains(errOut, "acknowledged PORT var-removed matches no breaking change") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}

	bad := write(t, "ack.txt", "WORKER_COUNT\n")
	if _, errOut, code := docuconf(t, "diff", "--ack", bad, ordersContract, newPath); code != 2 || !strings.Contains(errOut, "ack.txt:1: want <input> <change-id>") {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
}

func TestDiffErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"diff", ordersContract}, "want two contracts, old and new; got 1"},
		{[]string{"diff", "-", "-"}, "only one side can be read from standard input"},
		{[]string{"diff", "--format", "yaml", ordersContract, ordersContract}, `unknown --format "yaml"`},
		{[]string{"diff", ordersContract, "testdata/diff/missing.cue"}, "no such file"},
		{[]string{"diff", ordersContract, write(t, "bad.cue", contractOf(`vars: port: {type: "int", description: "lowercase"}`))}, "is not a valid contract"},
	} {
		_, errOut, code := docuconf(t, tc.args...)
		if code != 2 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: exit %d, stderr:\n%s\nwant %q", tc.args, code, errOut, tc.want)
		}
	}
}
