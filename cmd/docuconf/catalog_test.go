package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCatalog writes an organization's catalog with one postgres entry.
func writeCatalog(t *testing.T) string {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "catalog.yaml"), []byte("entries:\n  - repository: docker.io/library/postgres\n    versions: \"16\"\n    contract: pg.cue\n"), 0o644)
	pg := strings.Replace(read(t, "internal/catalog/community/postgres/contract.cue"),
		`values: ["scram-sha-256", "md5", "password", "reject"]`, `values: ["scram-sha-256", "trust"]`, 1)
	os.WriteFile(filepath.Join(dir, "pg.cue"), []byte(pg), 0o644)
	return dir
}

// TestCatalog checks catalog:IMAGE (spec/SPEC.md section 8.1): an
// organization's catalog wins over the community one, for the versions
// it covers.
func TestCatalog(t *testing.T) {
	t.Setenv("DOCUCONF_CATALOG", writeCatalog(t))
	values := write(t, "values.yaml", "POSTGRES_PASSWORD: {secretKeyRef: {name: pg, key: password}}\nPOSTGRES_HOST_AUTH_METHOD: trust\n")
	if out, errOut, code := docuconf(t, "vet", "-contract", "catalog:postgres:16.4", "-values", values); code != 0 || out != "postgres: ok\n" {
		t.Fatalf("the organization's contract was not used: exit %d\n%s%s", code, out, errOut)
	}
	out, _, code := docuconf(t, "vet", "-contract", "catalog:postgres:17", "-values", values)
	requireLines(t, out, code, `POSTGRES_HOST_AUTH_METHOD: "trust" is not one of scram-sha-256, md5, password, reject`)

	out, _, code = docuconf(t, "catalog", "resolve", "docker.io/library/postgres:17-alpine")
	if code != 0 || !strings.Contains(out, "postgres/contract.cue in catalog community") {
		t.Errorf("exit %d: %s", code, out)
	}
	if _, errOut, code := docuconf(t, "catalog", "resolve", "redis:7"); code != 2 || !strings.Contains(errOut, "no catalog has a contract for docker.io/library/redis") {
		t.Errorf("exit %d: %s", code, errOut)
	}
	if out, errOut, code := docuconf(t, "terraform", "-contract", "catalog:postgres:16", "-out", t.TempDir()); code != 0 {
		t.Errorf("terraform from a catalog contract: exit %d: %s%s", code, out, errOut)
	}
}

func TestCatalogCheck(t *testing.T) {
	if out, errOut, code := docuconf(t, "catalog", "check", "internal/catalog/community"); code != 0 {
		t.Fatalf("the community catalog: exit %d: %s%s", code, out, errOut)
	}
	dir := writeCatalog(t)
	os.WriteFile(filepath.Join(dir, "pg.cue"), []byte(strings.Replace(read(t, filepath.Join(dir, "pg.cue")), `type:        "enum"`, `type:        "choice"`, 1)), 0o644)
	if _, errOut, code := docuconf(t, "catalog", "check", dir); code != 2 || !strings.Contains(errOut, "pg.cue (docker.io/library/postgres)") {
		t.Errorf("a broken contract passed: exit %d: %s", code, errOut)
	}
}

// TestCatalogPush attaches a catalog contract to a mirror's copy of the
// image, which then reads back with oci://.
func TestCatalogPush(t *testing.T) {
	host, reg := testRegistry(t, true)
	pushImage(t, reg, "library/postgres", "16", nil)
	mirror := host + "/library/postgres:16"
	if _, errOut, code := docuconf(t, "catalog", "push", mirror); code != 2 || !strings.Contains(errOut, "no catalog has a contract for "+host+"/library/postgres") {
		t.Fatalf("a mirror matched without -for: exit %d: %s", code, errOut)
	}
	out, errOut, code := docuconf(t, "catalog", "push", "-for", "postgres:16", mirror)
	if code != 0 || !strings.HasPrefix(out, "oci://"+host+"/library/postgres@sha256:") {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
	values := write(t, "values.yaml", "POSTGRES_PASSWORD: {secretKeyRef: {name: pg, key: password}}\n")
	if out, errOut, code := docuconf(t, "vet", "-contract", "oci://"+mirror, "-values", values); code != 0 || out != "postgres: ok\n" {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
}
