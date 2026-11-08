package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// docuconf diff on the orders contract and edited copies of it.
func TestDiff(t *testing.T) {
	src, err := os.ReadFile(ordersContract)
	if err != nil {
		t.Fatal(err)
	}
	edit := func(pairs ...string) string {
		s := string(src)
		for i := 0; i < len(pairs); i += 2 {
			if !strings.Contains(s, pairs[i]) {
				t.Fatalf("the orders contract has no %q", pairs[i])
			}
			s = strings.Replace(s, pairs[i], pairs[i+1], 1)
		}
		return write(t, "contract.cue", s)
	}

	if out, _, code := docuconf(t, "diff", ordersContract, ordersContract); code != 0 || out != "no breaking or notable changes\n" {
		t.Errorf("same contract: exit %d, %q", code, out)
	}

	tightened := edit("max:         65535", "max:         1024", `default:     "info"`, `default:     "warn"`)
	out, stderr, code := docuconf(t, "diff", ordersContract, tightened)
	if code != 1 {
		t.Errorf("breaking change: exit %d, want 1", code)
	}
	for _, w := range []string{
		"breaking: PORT: its max falls from 65535 to 1024: values in between no longer validate\n",
		`notable: LOG_LEVEL: its default changes from "info" to "warn"`,
	} {
		if !strings.Contains(out, w) {
			t.Errorf("no %q in:\n%s", w, out)
		}
	}
	if !strings.Contains(stderr, "1 breaking change; land the platform's values first, then acknowledge with -accept") {
		t.Errorf("stderr: %s", stderr)
	}
	// Acknowledged, it passes, and says so.
	out, _, code = docuconf(t, "diff", ordersContract, tightened, "-accept", "PORT")
	if code != 0 || !strings.Contains(out, "breaking (accepted): PORT:") {
		t.Errorf("-accept PORT: exit %d:\n%s", code, out)
	}
	if _, _, code := docuconf(t, "diff", "-accept", "*", ordersContract, tightened); code != 0 {
		t.Errorf("-accept *: exit %d", code)
	}
	// Reversed, the change loosens: compatible, shown with -all.
	out, _, code = docuconf(t, "diff", tightened, ordersContract, "-all")
	if code != 0 || !strings.Contains(out, "compatible: PORT: its max rises from 1024 to 65535") {
		t.Errorf("loosened: exit %d:\n%s", code, out)
	}

	out, _, code = docuconf(t, "diff", ordersContract, tightened, "-format", "json")
	var doc struct {
		Changes []struct {
			Class, Kind, Input, Field, Message string
			Accepted                           bool
		}
		UnacceptedBreaking int
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if code != 1 || doc.UnacceptedBreaking != 1 || len(doc.Changes) != 2 || doc.Changes[0].Class != "breaking" || doc.Changes[0].Field != "max" {
		t.Errorf("json: exit %d: %+v", code, doc)
	}

	// A contract from an image compares like its file.
	host, reg := testRegistry(t, true)
	pushImage(t, reg, "orders-api", "2.3.0", nil)
	if _, stderr, code := docuconf(t, "push", "-contract", ordersContract, host+"/orders-api:2.3.0"); code != 0 {
		t.Fatal(stderr)
	}
	if out, stderr, code := docuconf(t, "diff", "oci://"+host+"/orders-api:2.3.0", tightened); code != 1 || !strings.Contains(out, "breaking: PORT: its max falls") {
		t.Errorf("oci://: exit %d:\n%s%s", code, out, stderr)
	}

	if _, _, code := docuconf(t, "diff", ordersContract); code != 0 && code != 2 {
		t.Errorf("one contract: exit %d", code)
	}
	if _, stderr, code := docuconf(t, "diff", ordersContract, write(t, "bad.cue", "nope: 1\n")); code != 2 || stderr == "" {
		t.Errorf("an invalid contract: exit %d", code)
	}
}
