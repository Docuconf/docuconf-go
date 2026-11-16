package main

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var fenceRe = regexp.MustCompile("(?ms)^```(\\w*)\\n(.*?)^```\\n")

// TestREADMESnippets keeps this example's README honest: every shell
// command in it is a line that CI runs, in smoke.sh or the examples
// workflow, and every row of its matrix is a case of TestMatrix.
func TestREADMESnippets(t *testing.T) {
	readme := read(t, "README.md")
	ci := lines(read(t, "smoke.sh") + read(t, "../../.github/workflows/examples.yml"))
	for i := range ci {
		ci[i] = strings.TrimSpace(ci[i])
	}
	n := 0
	for _, b := range fenceRe.FindAllStringSubmatch(readme, -1) {
		if b[1] != "sh" {
			continue
		}
		for _, l := range lines(b[2]) {
			n++
			if !slices.Contains(ci, strings.TrimSpace(l)) {
				t.Errorf("README command %q is not run by smoke.sh or the examples workflow", l)
			}
		}
	}
	if n < 5 {
		t.Fatalf("found only %d shell commands in the README", n)
	}

	tests := read(t, "main_test.go")
	rows := regexp.MustCompile(`(?m)^\| (\d+) \|`).FindAllStringSubmatch(readme, -1)
	if len(rows) != 11 {
		t.Fatalf("want 11 matrix rows in the README, found %d", len(rows))
	}
	for _, r := range rows {
		if !strings.Contains(tests, `{name: "`+r[1]+` `) {
			t.Errorf("README matrix row %s has no case in TestMatrix", r[1])
		}
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// lines returns the non-blank lines of s.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
