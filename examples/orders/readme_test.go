package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var fenceRe = regexp.MustCompile("(?ms)^```(\\w*)\\n(.*?)^```\\n")

// TestREADMESnippets keeps the SDK README honest: every Go snippet in it
// must be lines, in order, of a Go file in this example (which CI builds
// and tests), every YAML snippet lines of a file in deploy/ (which CI
// vets), and every shell command a line that CI runs, in smoke.sh or the
// examples workflow. Install commands are exempt.
func TestREADMESnippets(t *testing.T) {
	readme := read(t, "../../README.md")
	sources := map[string][]string{} // by fence language
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch {
		case strings.HasSuffix(path, ".go"):
			sources["go"] = append(sources["go"], path)
		case strings.HasPrefix(path, "deploy/") && strings.HasSuffix(path, ".yaml"):
			sources["yaml"] = append(sources["yaml"], path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ci := lines(read(t, "smoke.sh") + read(t, "../../.github/workflows/examples.yml"))
	for i := range ci {
		ci[i] = strings.TrimSpace(ci[i])
	}

	blocks := fenceRe.FindAllStringSubmatch(readme, -1)
	if len(blocks) < 10 {
		t.Fatalf("found only %d code blocks in the README", len(blocks))
	}
	for _, b := range blocks {
		lang, body := b[1], b[2]
		switch lang {
		case "go", "yaml":
			want := lines(body)
			found := false
			for _, f := range sources[lang] {
				if isSubsequence(want, lines(read(t, f))) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("this %s snippet is not in any file of examples/orders (as lines, in order):\n%s", lang, body)
			}
		case "sh":
			if strings.Contains(body, "go get ") || strings.Contains(body, "go install ") ||
				strings.Contains(body, "go mod edit ") || strings.Contains(body, "go run ") {
				continue
			}
			for _, l := range lines(body) {
				if !slices.Contains(ci, strings.TrimSpace(l)) {
					t.Errorf("README command %q is not run by smoke.sh or the examples workflow", l)
				}
			}
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

func isSubsequence(want, have []string) bool {
	i := 0
	for _, h := range have {
		if i < len(want) && h == want[i] {
			i++
		}
	}
	return i == len(want)
}
