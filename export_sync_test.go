package docuconf

import (
	"bytes"
	"os"
	"testing"
)

// TestExportCommandsInSync checks that the docuconf CLI (its own module,
// with CUE) and the dependency-free docuconf-export command (in this
// module) run the same export code.
func TestExportCommandsInSync(t *testing.T) {
	a, err := os.ReadFile("cmd/docuconf/export.go")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("cmd/docuconf-export/export.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("cmd/docuconf/export.go and cmd/docuconf-export/export.go differ; copy one over the other")
	}
}
