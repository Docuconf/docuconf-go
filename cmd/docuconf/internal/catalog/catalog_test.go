package catalog

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestSplitImage(t *testing.T) {
	for in, want := range map[string][2]string{
		"postgres":                         {"docker.io/library/postgres", "latest"},
		"postgres:16.4-alpine":             {"docker.io/library/postgres", "16.4-alpine"},
		"bitnami/redis:7.2":                {"docker.io/bitnami/redis", "7.2"},
		"docker.io/nginx:1.27":             {"docker.io/library/nginx", "1.27"},
		"index.docker.io/library/nginx":    {"docker.io/library/nginx", "latest"},
		"ghcr.io/acme/app:v2.1.0@sha256:a": {"ghcr.io/acme/app", "v2.1.0"},
		"localhost:5000/app":               {"localhost:5000/app", "latest"},
		"localhost/app:3":                  {"localhost/app", "3"},
	} {
		repo, tag := SplitImage(in)
		if repo != want[0] || tag != want[1] {
			t.Errorf("%s: got %s %s, want %s %s", in, repo, tag, want[0], want[1])
		}
	}
}

func TestRanges(t *testing.T) {
	for _, tc := range []struct {
		rng, tag string
		want     bool
	}{
		{">=13 <19", "16", true},
		{">=13 <19", "16.4-alpine", true},
		{">=13 <19", "12.9", false},
		{">=13 <19", "19", false},
		{">=13 <19", "latest", false},
		{">=1.19 <2", "1.27.2", true},
		{">=1.19 <2", "1.18", false},
		{"16 || 17", "17.1", true},
		{"16 || 17", "18", false},
		{"=16.4", "16.4.2", true},
		{"=16.4", "16.5", false},
		{"*", "latest", true},
	} {
		r, err := parseRange(tc.rng)
		if err != nil {
			t.Fatal(err)
		}
		v, ok := tagVersion(tc.tag)
		if got := r.holds(v, ok); got != tc.want {
			t.Errorf("%q holds %q: got %v", tc.rng, tc.tag, got)
		}
	}
	for _, bad := range []string{"", "~16", ">=a", "16 ||"} {
		if _, err := parseRange(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestResolve(t *testing.T) {
	org, err := Load("org", fstest.MapFS{
		"catalog.yaml": {Data: []byte("entries:\n  - repository: docker.io/library/postgres\n    versions: \"16\"\n    contract: pg.cue\n")},
		"pg.cue":       {Data: []byte("org")},
	})
	if err != nil {
		t.Fatal(err)
	}
	cats := []*Catalog{org, Community()}
	m, err := Resolve(cats, "postgres:16.4")
	if err != nil || m.Catalog.Name != "org" {
		t.Fatalf("the organization's entry does not win: %v %v", m, err)
	}
	if m, err = Resolve(cats, "postgres:17"); err != nil || m.Catalog.Name != "community" {
		t.Fatalf("no fall back to the community catalog: %v %v", m, err)
	}
	if !strings.Contains(string(m.Contract), "POSTGRES_PASSWORD") {
		t.Errorf("wrong contract: %s", m.Contract)
	}
	for image, want := range map[string]string{
		"postgres":         `the tag "latest" names no version`,
		"postgres:12":      "no catalog has a contract for docker.io/library/postgres at version 12",
		"ghcr.io/acme/app": "no catalog has a contract for ghcr.io/acme/app",
	} {
		if _, err := Resolve(cats, image); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", image, err, want)
		}
	}
}

func TestLoadRejects(t *testing.T) {
	_, err := Load("bad", fstest.MapFS{
		"catalog.yaml": {Data: []byte("entries:\n  - repository: postgres\n    versions: \"~16\"\n    contract: ../pg.cue\n")},
	})
	for _, want := range []string{"repository must be written in full, docker.io/library/postgres", "versions:", "contract must be a path inside the catalog"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("no %q in %v", want, err)
		}
	}
}
