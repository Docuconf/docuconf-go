package metaschema

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestInSyncWithSpec checks that the embedded copy matches spec/cue.
func TestInSyncWithSpec(t *testing.T) {
	spec := filepath.Join("..", "..", "..", "..", "spec", "cue")
	if _, err := os.Stat(spec); err != nil {
		t.Skip("spec/cue not found (not running in the repository)")
	}
	n := 0
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		got, _ := fs.ReadFile(files, p)
		want, err := os.ReadFile(filepath.Join(spec, filepath.FromSlash(p)))
		if err != nil {
			t.Errorf("%s: %v", p, err)
			return nil
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from spec/cue/%s; run go generate ./internal/metaschema", p, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	specFiles, _ := filepath.Glob(filepath.Join(spec, "contract", "*.cue"))
	docsFiles, _ := filepath.Glob(filepath.Join(spec, "docs", "*.cue"))
	if n != len(specFiles)+len(docsFiles)+1 {
		t.Errorf("embedded %d files, spec has %d contract files, %d docs files and module.cue", n, len(specFiles), len(docsFiles))
	}
}
