package docuconf_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go"
	"github.com/stretchr/testify/require"
)

type shopConfig struct {
	_ docuconf.Roles `roles:"web,worker,migrate"`
	// Primary Postgres connection string.
	DatabaseURL docuconf.Secret `env:"DATABASE_URL,required"`
	// Origins the web server accepts browser requests from.
	Origins []string `env:"ALLOWED_ORIGINS,required" roles:"web"`
	// Port the web server listens on.
	Port int `env:"PORT" envDefault:"3000" roles:"web"`
	// Queues the worker takes jobs from.
	Queues []string `env:"QUEUES,required" roles:"worker"`
	// The robots.txt the web server serves.
	Robots docuconf.TextFile `file:"robots,required" path:"/etc/shop/robots/robots.txt" roles:"web"`
}

// TestRolesAtBoot checks one image's several processes (SPEC §4.9): each
// requires only the inputs its role uses.
func TestRolesAtBoot(t *testing.T) {
	parse := func(env map[string]string) (shopConfig, error) {
		return docuconf.ParseWithOptions[shopConfig](docuconf.Options{Environment: env, FileRoot: t.TempDir()})
	}
	cfg, err := parse(map[string]string{"DOCUCONF_ROLE": "worker", "DATABASE_URL": "postgres://db/shop", "QUEUES": "default,mail"})
	require.NoError(t, err)
	require.Equal(t, []string{"default", "mail"}, cfg.Queues)
	require.Empty(t, cfg.Origins)

	_, err = parse(map[string]string{"DOCUCONF_ROLE": "worker", "DATABASE_URL": "postgres://db/shop"})
	require.Equal(t, []string{"QUEUES missing_required: is required but not set"}, violations(t, err))

	_, err = parse(map[string]string{"DOCUCONF_ROLE": "migrate", "DATABASE_URL": "postgres://db/shop"})
	require.NoError(t, err)

	_, err = parse(map[string]string{"DOCUCONF_ROLE": "web", "DATABASE_URL": "postgres://db/shop", "ALLOWED_ORIGINS": "https://shop.example"})
	require.Equal(t, []string{"robots"}, inputs(t, err))

	_, err = parse(map[string]string{"DOCUCONF_ROLE": "cron", "DATABASE_URL": "postgres://db/shop"})
	require.Contains(t, violations(t, err), "DOCUCONF_ROLE not_in_enum: \"cron\" is not one of web, worker, migrate")
	_, err = parse(map[string]string{"DATABASE_URL": "postgres://db/shop"})
	require.Contains(t, violations(t, err), "DOCUCONF_ROLE missing_required: is required but not set")
}

func TestRolesExport(t *testing.T) {
	out, err := docuconf.Export[shopConfig](docuconf.Meta{Name: "shop"})
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, `roles: ["web", "worker", "migrate"]`)
	require.Contains(t, s, "DOCUCONF_ROLE: {")
	require.Contains(t, s, `roles: ["worker"]`)
	require.Equal(t, 3, strings.Count(s, `roles: ["web"]`), s)

	cue := cueBinary(t)
	dir := t.TempDir()
	copyDir(t, "spec/cue/cue.mod", filepath.Join(dir, "cue.mod"))
	copyDir(t, "spec/cue/contract", filepath.Join(dir, "contract"))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "shop"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shop", "contract.cue"), out, 0o644))
	cmd := exec.Command(cue, "vet", "-c", "./shop/contract.cue")
	cmd.Dir = dir
	msg, err := cmd.CombinedOutput()
	require.NoError(t, err, "cue vet failed:\n%s", msg)
}

func TestRolesDeclarationErrors(t *testing.T) {
	type noRoles struct {
		Port int `env:"PORT" envDefault:"80" roles:"web" desc:"Port to listen on"`
	}
	_, err := docuconf.ParseWithOptions[noRoles](docuconf.Options{Environment: map[string]string{}})
	require.ErrorContains(t, err, "has roles, but the struct declares none")

	type bad struct {
		_    docuconf.Roles `roles:"web,Web"`
		Port int            `env:"PORT" envDefault:"80" roles:"api" desc:"Port to listen on"`
		Role string         `env:"DOCUCONF_ROLE" desc:"The role it runs"`
	}
	_, err = docuconf.ParseWithOptions[bad](docuconf.Options{Environment: map[string]string{}})
	var de *docuconf.DeclarationError
	require.True(t, errors.As(err, &de), "%v", err)
	all := strings.Join(de.Problems, "\n")
	for _, want := range []string{`role "Web" must be a DNS label`, "PORT (bad.Port): role api is not one of the declared roles, web, Web", "DOCUCONF_ROLE (bad.Role): docuconf declares DOCUCONF_ROLE for the roles itself"} {
		require.Contains(t, all, want)
	}
}

func TestRolesContract(t *testing.T) {
	contract := `{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "shop", "generator": {"language": "go", "sdk": "x", "version": "1"}},
		"roles": ["web", "worker"],
		"vars": {
			"DOCUCONF_ROLE": {"type": "enum", "description": "The workload role", "required": true, "values": ["web", "worker"]},
			"QUEUES": {"type": "list", "items": "string", "description": "Queues to work", "required": true, "roles": ["worker"]}
		}}`
	load := func(env map[string]string) error {
		_, err := docuconf.LoadContract([]byte(contract), docuconf.Options{Environment: env})
		return err
	}
	require.NoError(t, load(map[string]string{"DOCUCONF_ROLE": "web"}))
	require.Equal(t, []string{"QUEUES"}, inputs(t, load(map[string]string{"DOCUCONF_ROLE": "worker"})))
}
