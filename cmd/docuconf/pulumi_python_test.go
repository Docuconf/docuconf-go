package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The Python component is run with the Pulumi Python SDK's mocks: no
// Pulumi CLI, plugin or cloud. testdata/pulumi-python/requirements.txt
// pins what it runs with, installed into testdata/pulumi-python/.venv on
// first use. Without python3 the tests are skipped; set
// DOCUCONF_SKIP_PULUMI_PYTHON=1 to skip them.

var (
	pythonOnce sync.Once
	pythonBin  string
	pythonErr  string
)

// python returns the test virtualenv's interpreter, creating it once.
func python(t *testing.T) string {
	t.Helper()
	if os.Getenv("DOCUCONF_SKIP_PULUMI_PYTHON") != "" {
		t.Skip("DOCUCONF_SKIP_PULUMI_PYTHON is set")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	pythonOnce.Do(func() {
		dir, err := filepath.Abs(filepath.Join("testdata", "pulumi-python"))
		if err != nil {
			pythonErr = err.Error()
			return
		}
		bin := filepath.Join(dir, ".venv", "bin", "python")
		stamp := filepath.Join(dir, ".venv", "requirements.txt")
		want, _ := os.ReadFile(filepath.Join(dir, "requirements.txt"))
		if have, err := os.ReadFile(stamp); err != nil || !bytes.Equal(have, want) {
			for _, args := range [][]string{
				{"python3", "-m", "venv", "--clear", filepath.Join(dir, ".venv")},
				{bin, "-m", "pip", "install", "-q", "-r", filepath.Join(dir, "requirements.txt")},
			} {
				if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
					pythonErr = strings.Join(args, " ") + ": " + err.Error() + "\n" + string(out)
					return
				}
			}
			os.WriteFile(stamp, want, 0o644)
		}
		pythonBin = bin
	})
	if pythonErr != "" {
		t.Fatal(pythonErr)
	}
	return pythonBin
}

// pythonHarness runs each case through the generated component, and writes
// what it rendered in the Kubernetes API's (or the target's) camelCase, as
// the other languages' harnesses do.
const pythonHarness = `import json
import re
import sys

import pulumi


class Mocks(pulumi.runtime.Mocks):
    def new_resource(self, args):
        return [args.name, args.inputs]

    def call(self, args):
        return {}


pulumi.runtime.set_mocks(Mocks(), "test", "test", preview=False)

from kitchen_api_config import ConfigViolation, KitchenApiConfig  # noqa: E402


def camel(x, keep=False):
    """The provider's snake_case keys as the API writes them; names and data as they are."""
    if isinstance(x, list):
        return [camel(i) for i in x]
    if isinstance(x, dict):
        return {k if keep else re.sub(r"_([a-z])", lambda m: m.group(1).upper(), k): camel(v, k in ("volume_attributes",)) for k, v in x.items()}
    return x


cases = json.load(open(sys.argv[1]))
target = sys.argv[2]
results = {}
waits = []


@pulumi.runtime.test
def run():
    for c in cases:
        name = c["name"]
        try:
            comp = KitchenApiConfig(name, c["args"])
        except ConfigViolation as e:
            results[name] = {"error": str(e)}
            continue
        except Exception as e:  # a bug: report it as the case's result
            results[name] = {"error": f"{type(e).__name__}: {e}"}
            continue
        if target == "kubernetes":
            res = {"configMaps": comp.kubernetes_config_maps}
            results[name] = res
            outs = [comp.kubernetes_env, comp.kubernetes_volumes, comp.kubernetes_volume_mounts, comp.restart_triggers, comp.pod_annotations, comp.pod_labels, comp.checked]

            def put(xs, res=res):
                for k, x in zip(["env", "volumes", "volumeMounts", "restartTriggers"], xs[:4]):
                    res[k] = camel(x)
                res["podAnnotations"], res["podLabels"] = xs[4], xs[5]

            waits.append(pulumi.Output.all(*outs).apply(put))
        elif target == "ecs":
            res = {}
            results[name] = res
            waits.append(pulumi.Output.all(comp.ecs_environment, comp.ecs_secrets, comp.checked).apply(lambda xs, res=res: res.update(environment=xs[0], secrets=xs[1])))
        elif target == "lambda":
            res = {}
            results[name] = res
            waits.append(pulumi.Output.all(comp.lambda_environment, comp.checked).apply(lambda xs, res=res: res.update(lambdaEnvironment=xs[0])))
        elif target == "containerapps":
            res = {}
            results[name] = res
            outs = [comp.containerapp_env, comp.containerapp_secrets, comp.containerapp_volumes, comp.containerapp_volume_mounts, comp.checked]
            waits.append(
                pulumi.Output.all(*outs).apply(lambda xs, res=res: res.update(env=camel(xs[0]), appSecrets=camel(xs[1]), volumes=camel(xs[2]), mounts=camel(xs[3])))
            )
        else:
            res = {}
            results[name] = res
            waits.append(
                pulumi.Output.all(comp.cloudrun_env, comp.cloudrun_volumes, comp.cloudrun_volume_mounts, comp.checked).apply(
                    lambda xs, res=res: res.update(env=camel(xs[0]), volumes=camel(xs[1]), mounts=camel(xs[2]))
                )
            )
    return pulumi.Output.all(*waits)


run()
json.dump(results, open(sys.argv[3], "w"))
`

// runPython generates the kitchen component for a target, runs the cases
// through it, and returns the harness's output.
func runPython(t *testing.T, target string, cases []map[string]any) []byte {
	t.Helper()
	py := python(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"pulumi", "-contract", kitchen, "-lang", "python", "-target", target, "-out", filepath.Join(dir, "kitchen_api_config")}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf pulumi -lang python -target %s: %s", target, stderr.String())
	}
	writeFile(t, filepath.Join(dir, "harness.py"), pythonHarness)
	writeJSON(t, filepath.Join(dir, "cases.json"), cases)
	cmd := exec.Command(py, "harness.py", "cases.json", target, "out.json")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("harness: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "out.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var (
	pyResults     map[string]goResult
	pyResultsOnce sync.Once
)

func pythonComponentResults(t *testing.T) map[string]goResult {
	t.Helper()
	python(t)
	pyResultsOnce.Do(func() {
		var list []map[string]any
		for _, tc := range renderCases {
			list = append(list, map[string]any{"name": "render/" + tc.name, "args": pulumiArgs(tc.doc)})
		}
		for _, tc := range rejectCases {
			list = append(list, map[string]any{"name": "reject/" + tc.name, "args": pulumiArgs(tc.doc)})
		}
		b := runPython(t, "kubernetes", list)
		pyResults = map[string]goResult{}
		if err := json.Unmarshal(b, &pyResults); err != nil {
			t.Fatalf("harness output: %v\n%s", err, b)
		}
	})
	if pyResults == nil {
		t.Fatal("the Python harness did not run")
	}
	return pyResults
}

func TestPulumiPythonMatchesRender(t *testing.T) {
	checkMatchesRender(t, pythonComponentResults(t))
}

func TestPulumiPythonRejectsWhatVetRejects(t *testing.T) {
	checkRejects(t, pythonComponentResults(t))
}

func TestPulumiPythonTargets(t *testing.T) {
	for _, target := range []string{"ecs", "cloudrun", "containerapps", "lambda"} {
		t.Run(target, func(t *testing.T) {
			var list []map[string]any
			for _, c := range targetCases(target) {
				list = append(list, map[string]any{"name": c.name, "args": c.args})
			}
			got := map[string]targetResult{}
			b := runPython(t, target, list)
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("harness output: %v\n%s", err, b)
			}
			checkTarget(t, target, got)
		})
	}
}

// A type checker refuses a source an input does not accept, as tsc and
// the Go compiler do for the other languages.
func TestPulumiPythonTypeChecks(t *testing.T) {
	py := python(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"pulumi", "-contract", ordersContract, "-lang", "python", "-out", filepath.Join(dir, "orders_api_config")}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf pulumi -lang python: %s", stderr.String())
	}
	good := `from orders_api_config import OrdersApiConfig, OrdersApiConfigArgs

args: OrdersApiConfigArgs = {
    "DATABASE_URL": {"secretKeyRef": {"name": "orders-db", "key": "url"}},
    "LOG_LEVEL": {"value": "warn"},
    "WORKER_COUNT": {"value": 8},
    "ALLOWED_ORIGINS": {"value": ["https://shop.example.com"]},
    "discounts": {"inline": {"codes": {"WELCOME10": 10}}},
}
OrdersApiConfig("orders", args)
`
	bad := map[string]string{
		"secret_as_value": `{"DATABASE_URL": {"value": "postgres://u:p@db/orders"}}`,
		"enum":            `{"DATABASE_URL": {"secretKeyRef": {"name": "a", "key": "b"}}, "LOG_LEVEL": {"value": "loud"}}`,
		"missing":         `{"LOG_LEVEL": {"value": "warn"}}`,
	}
	mypy := func(name, src string) (string, error) {
		writeFile(t, filepath.Join(dir, name+".py"), src)
		cmd := exec.Command(py, "-m", "mypy", "--strict", "--no-incremental", "--follow-imports=silent", name+".py")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := mypy("good", good); err != nil {
		t.Errorf("mypy refuses a correct program: %v\n%s", err, out)
	}
	for name, args := range bad {
		src := "from orders_api_config import OrdersApiConfig, OrdersApiConfigArgs\n\nargs: OrdersApiConfigArgs = " + args + "\nOrdersApiConfig(\"orders\", args)\n"
		if out, err := mypy(name, src); err == nil {
			t.Errorf("%s: mypy accepts it:\n%s", name, out)
		}
	}
}
