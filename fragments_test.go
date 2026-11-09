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

type workerConfig struct {
	_ docuconf.Fragments `include:"go-runtime@1,otel@1"`
	// Queue the worker reads jobs from.
	QueueURL string `env:"QUEUE_URL,required" schemes:"amqp,amqps"`
	// Soft memory limit for the Go runtime.
	MemLimit string `env:"GOMEMLIMIT" envDefault:"900MiB"`
}

// TestFragmentsExport checks that an included fragment's variables are in
// the exported contract, inline, with includes naming the fragments, and
// that the contract passes the meta-schema (SPEC §4.8).
func TestFragmentsExport(t *testing.T) {
	out, err := docuconf.Export[workerConfig](docuconf.Meta{Name: "worker"})
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, "includes: [\"go-runtime@1\", \"otel@1\"]")
	for _, n := range []string{"GOMAXPROCS:", "GOGC:", "GODEBUG:", "OTEL_EXPORTER_OTLP_ENDPOINT:", "OTEL_SDK_DISABLED:"} {
		require.Contains(t, s, n)
	}
	// The struct's own GOMEMLIMIT replaces the fragment's.
	require.Contains(t, s, `default:     "900MiB"`)
	require.Equal(t, 1, strings.Count(s, "GOMEMLIMIT:"))
	require.Contains(t, s, "secret:      true") // OTEL_EXPORTER_OTLP_HEADERS

	cue := cueBinary(t)
	dir := t.TempDir()
	copyDir(t, "spec/cue/cue.mod", filepath.Join(dir, "cue.mod"))
	copyDir(t, "spec/cue/contract", filepath.Join(dir, "contract"))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "worker"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker", "contract.cue"), out, 0o644))
	cmd := exec.Command(cue, "vet", "-c", "./worker/contract.cue")
	cmd.Dir = dir
	msg, err := cmd.CombinedOutput()
	require.NoError(t, err, "cue vet failed:\n%s", msg)
}

// TestFragmentsAtBoot checks the fragment's variables at boot, without
// setting any field.
func TestFragmentsAtBoot(t *testing.T) {
	parse := func(env map[string]string) error {
		_, err := docuconf.ParseWithOptions[workerConfig](docuconf.Options{Environment: env})
		return err
	}
	require.NoError(t, parse(map[string]string{"QUEUE_URL": "amqps://mq/jobs", "GOMAXPROCS": "4", "OTEL_TRACES_SAMPLER": "always_on"}))
	err := parse(map[string]string{"QUEUE_URL": "amqps://mq/jobs", "GOMAXPROCS": "0", "OTEL_EXPORTER_OTLP_PROTOCOL": "udp", "OTEL_EXPORTER_OTLP_HEADERS": "x"})
	require.ElementsMatch(t, []string{"GOMAXPROCS", "OTEL_EXPORTER_OTLP_PROTOCOL"}, inputs(t, err))
}

func TestFragmentsDeclarationErrors(t *testing.T) {
	type unknown struct {
		_    docuconf.Fragments `include:"kafka@1"`
		Port int                `env:"PORT" envDefault:"8080" desc:"Port to listen on"`
	}
	_, err := docuconf.ParseWithOptions[unknown](docuconf.Options{Environment: map[string]string{}})
	var de *docuconf.DeclarationError
	require.True(t, errors.As(err, &de), "%v", err)
	require.Contains(t, strings.Join(de.Problems, "\n"), `"kafka@1" is not a well-known fragment; the fragments are [aws-sdk@1 dotnet-runtime@1 go-runtime@1 node@1 otel@1 rails@1]`)

	type empty struct {
		_    docuconf.Fragments
		Port int `env:"PORT" envDefault:"8080" desc:"Port to listen on"`
	}
	_, err = docuconf.ParseWithOptions[empty](docuconf.Options{Environment: map[string]string{}})
	require.ErrorContains(t, err, "a docuconf.Fragments field needs an include tag")
}

// TestFragmentsContract checks that contract-first mode reads a contract
// with includes: its variables are ordinary variables.
func TestFragmentsContract(t *testing.T) {
	contract := `{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "worker", "generator": {"language": "go", "sdk": "x", "version": "1"}},
		"includes": ["go-runtime@1"],
		"vars": {"GOMAXPROCS": {"type": "int", "description": "Max threads running Go code", "min": 1}}}`
	vals, err := docuconf.LoadContract([]byte(contract), docuconf.Options{Environment: map[string]string{"GOMAXPROCS": "2"}})
	require.NoError(t, err)
	require.EqualValues(t, 2, vals["GOMAXPROCS"])
}
