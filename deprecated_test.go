package docuconf_test

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go"
	"github.com/stretchr/testify/require"
)

type deprecatedConfig struct {
	// Port to listen on.
	Port int `env:"PORT" envDefault:"8080"`
	// Old name of the listen port.
	OldPort int `env:"OLD_PORT" deprecated:"Use PORT"`
	// Password of the old billing API.
	OldToken docuconf.Secret `env:"OLD_TOKEN" deprecated:"The billing API no longer takes a token"`
	// Licence key file, no longer checked.
	Licence docuconf.TextFile `file:"licence" path:"/etc/app/licence/licence.txt" deprecated:"Licences are checked online now"`
}

// TestDeprecatedWarnsAtBoot checks the boot warning for a deprecated
// input that is set (SPEC §11.2): it names the input and the message,
// never the value.
func TestDeprecatedWarnsAtBoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/app/licence"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "etc/app/licence/licence.txt"), []byte("ABC"), 0o644))

	var logged strings.Builder
	logger := slog.New(slog.NewTextHandler(&logged, nil))
	cfg, err := docuconf.ParseWithOptions[deprecatedConfig](docuconf.Options{
		Environment: map[string]string{"OLD_PORT": "9090", "OLD_TOKEN": "tok-s3cr3t"},
		FileRoot:    root,
		Logger:      logger,
	})
	require.NoError(t, err)
	require.Equal(t, 9090, cfg.OldPort)
	out := logged.String()
	require.Contains(t, out, `msg="docuconf: deprecated variable is set" name=OLD_PORT message="Use PORT"`)
	require.Contains(t, out, `name=OLD_TOKEN message="The billing API no longer takes a token"`)
	require.Contains(t, out, `msg="docuconf: deprecated file input is present" input=licence message="Licences are checked online now"`)
	require.NotContains(t, out, "9090")
	require.NotContains(t, out, "s3cr3t")

	// Unset, nothing is logged.
	logged.Reset()
	_, err = docuconf.ParseWithOptions[deprecatedConfig](docuconf.Options{Environment: map[string]string{}, FileRoot: t.TempDir(), Logger: logger})
	require.NoError(t, err)
	require.NotContains(t, logged.String(), "deprecated")
}

func TestDeprecatedExport(t *testing.T) {
	out, err := docuconf.Export[deprecatedConfig](docuconf.Meta{Name: "svc"})
	require.NoError(t, err)
	require.Contains(t, string(out), "deprecated: {\n\t\t\t\tmessage: \"Use PORT\"\n\t\t\t}")
	require.Contains(t, string(out), "deprecated: {\n\t\t\t\tmessage: \"Licences are checked online now\"\n\t\t\t}")
}

func TestDeprecatedDeclarationErrors(t *testing.T) {
	type bad struct {
		// A required variable cannot be deprecated.
		Required int `env:"REQUIRED,required" deprecated:"Use PORT" desc:"Old required port"`
		// A deprecation says what to use instead, or why.
		Blank int `env:"BLANK" deprecated:" " desc:"Old blank port"`
		// At most 500 characters.
		Long int `env:"LONG" deprecated:"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" desc:"Old long port"`
		// A required file input cannot be deprecated either.
		Licence docuconf.TextFile `file:"licence,required" path:"/etc/app/licence/l.txt" deprecated:"Gone" desc:"Old licence file"`
	}
	_, err := docuconf.ParseWithOptions[bad](docuconf.Options{Environment: map[string]string{"REQUIRED": "1"}})
	var de *docuconf.DeclarationError
	require.True(t, errors.As(err, &de), "%v", err)
	all := strings.Join(de.Problems, "\n")
	for _, want := range []string{
		"REQUIRED (bad.Required): a required variable cannot be deprecated",
		"BLANK (bad.Blank): deprecated must say what to use instead, or why the input is going away",
		"LONG (bad.Long): deprecated must be at most 500 characters",
		"a required file input cannot be deprecated",
	} {
		require.Contains(t, all, want)
	}

	contract := `{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "svc", "generator": {"language": "go", "sdk": "x", "version": "1"}},
		"vars": {"OLD": {"type": "int", "description": "Old listen port", %s}}}`
	for field, want := range map[string]string{
		`"required": true, "deprecated": {"message": "Use PORT"}`:                        "a required variable cannot be deprecated",
		`"deprecated": {"message": ""}`:                                                  "deprecated must say what to use instead",
		fmt.Sprintf(`"deprecated": {"message": "%s"}`, strings.Repeat("é", 501)):         "deprecated must be at most 500 characters",
		`"deprecated": {"message": "Use PORT", "replacedBy": "PORT"}, "required": false`: "",
	} {
		_, err := docuconf.LoadContract([]byte(fmt.Sprintf(contract, field)), docuconf.Options{Environment: map[string]string{}})
		if want == "" {
			require.NoError(t, err)
			continue
		}
		require.ErrorContains(t, err, want)
	}
}
