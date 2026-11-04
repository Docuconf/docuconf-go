package terraform

import (
	"strings"
	"testing"
)

func TestGoNanos(t *testing.T) {
	for d, want := range map[string]string{
		"1ns": "1", "1500ms": "1500000000", "1h30m": "5400000000000",
		"9999999h": "35999996400000000000", // beyond int64 nanoseconds
	} {
		if got := goNanos(d); got != want {
			t.Errorf("goNanos(%q) = %s, want %s", d, got, want)
		}
	}
}

func TestSafeMessage(t *testing.T) {
	for in, want := range map[string]string{
		`"X: ${var.X.value} is below min 1"`:        `"X: ${try(format("%v", var.X.value), "")} is below min 1"`,
		`"a ${[for i, x in v : "${i}" if x][0]} b"`: `"a ${try(format("%v", [for i, x in v : "${i}" if x][0]), "")} b"`,
		`"costs $$5, not $${x}"`:                    `"costs $$5, not $${x}"`,
	} {
		if got := safeMessage(in); got != want {
			t.Errorf("safeMessage(%s)\n got %s\nwant %s", in, got, want)
		}
	}
}

func TestHCLString(t *testing.T) {
	if got := hclString(`a "b" ${c} %{d} <e>`); got != `"a \"b\" $${c} %%{d} <e>"` {
		t.Errorf("hclString = %s", got)
	}
}

func TestParseKeepsOrder(t *testing.T) {
	c, err := Parse([]byte(`{"metadata":{"name":"x"},"vars":{"Z":{"type":"int","description":"zzzzz"},"A":{"type":"string","description":"aaaaa"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Vars[0].Name != "Z" || c.Vars[1].Name != "A" {
		t.Errorf("order = %s, %s", c.Vars[0].Name, c.Vars[1].Name)
	}
}

func TestGenerateRejects(t *testing.T) {
	c, _ := Parse([]byte(`{"metadata":{"name":"x"},"vars":{}}`))
	if _, err := Generate(c, Options{Targets: []string{"lambda"}}); err == nil || !strings.Contains(err.Error(), `target "lambda" is not supported yet`) {
		t.Errorf("err = %v", err)
	}
}
