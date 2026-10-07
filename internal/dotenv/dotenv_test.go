package dotenv

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	got, err := Parse("# c\nexport A=1\nB = two # note\nC=\"x\\ny\"\nD='$lit'\n\nE=\n", ".env")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "two", "C": "x\ny", "D": "$lit", "E": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseErrors(t *testing.T) {
	for _, s := range []string{"NOEQUALS\n", "=v\n", "A=\"open\n"} {
		if _, err := Parse(s, ".env"); err == nil {
			t.Errorf("%q: no error", s)
		}
	}
}
