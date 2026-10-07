package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const batch = "testdata/exec/contract.cue"

// bootEnv sets a valid environment for the batch contract, with the
// orders file under a DOCUCONF_FILE_ROOT, and returns the root.
func bootEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "in.txt"), []byte("A-1 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCUCONF_FILE_ROOT", root)
	t.Setenv("DOCUCONF_TERMINATION_LOG", filepath.Join(root, "termination-log"))
	t.Setenv("ORDERS_FILE", "/data/in.txt")
	t.Setenv("DATABASE_URL", "postgres://orders:hunter2@db/orders")
	for _, k := range []string{"PORT", "ALLOWED_ORIGINS", "REQUEST_TIMEOUT"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return root
}

func TestCheckValid(t *testing.T) {
	bootEnv(t)
	out, errOut, code := docuconf(t, "check", "-contract", batch)
	if code != 0 || out != "orders-batch: ok\n" {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

func TestCheckReportsEveryProblem(t *testing.T) {
	root := bootEnv(t)
	t.Setenv("PORT", "0")
	t.Setenv("DATABASE_URL", "mysql://orders:hunter2@db/orders")
	t.Setenv("ALLOWED_ORIGINS", "")
	t.Setenv("REQUEST_TIMEOUT", "10m")
	t.Setenv("ORDERS_FILE", "/data/missing.txt")
	out, errOut, code := docuconf(t, "check", "--contract", batch)
	if code != 1 || out != "" {
		t.Fatalf("exit %d, want 1\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	want := `docuconf: 4 configuration problems:
  DATABASE_URL: scheme is not one of postgres (invalid_scheme)
  PORT: 0 is below min 1 (out_of_range)
  REQUEST_TIMEOUT: 10m is above max 5m (out_of_range)
  orders: ` + root + `/data/missing.txt does not exist (mount it there, or set ORDERS_FILE) (file_missing)
`
	if errOut != want {
		t.Fatalf("stderr:\n%s\nwant:\n%s", errOut, want)
	}
	if strings.Contains(errOut, "hunter2") {
		t.Fatal("the secret was printed")
	}
	if log := read(t, filepath.Join(root, "termination-log")); !strings.Contains(log, "PORT: 0 is below min 1 (out_of_range)") || strings.Contains(log, "hunter2") {
		t.Fatalf("termination log:\n%s", log)
	}
}

func TestCheckFileTooLarge(t *testing.T) {
	root := bootEnv(t)
	if err := os.WriteFile(filepath.Join(root, "data", "in.txt"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := docuconf(t, "check", "-contract", batch)
	if code != 1 || !strings.Contains(errOut, "(file_too_large)") {
		t.Fatalf("exit %d\nstderr:\n%s", code, errOut)
	}
}

func TestCheckEnvFile(t *testing.T) {
	bootEnv(t)
	os.Unsetenv("DATABASE_URL")
	dotEnv := write(t, ".env", "DATABASE_URL=postgres://orders:pw@db/orders\nPORT=9000\n")
	t.Setenv("PORT", "9100") // the environment wins over the file
	out, errOut, code := docuconf(t, "check", "-contract", batch, "-env-file", dotEnv)
	if code != 0 || out != "orders-batch: ok\n" {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

func TestCheckUsage(t *testing.T) {
	if _, errOut, code := docuconf(t, "check", "-contract", batch, "extra"); code != 2 || !strings.Contains(errOut, "use docuconf exec") {
		t.Fatalf("exit %d\nstderr:\n%s", code, errOut)
	}
	if _, errOut, code := docuconf(t, "check", "-contract", "testdata/exec/missing.cue"); code != 2 || !strings.Contains(errOut, "missing.cue") {
		t.Fatalf("exit %d\nstderr:\n%s", code, errOut)
	}
	if _, errOut, code := docuconf(t, "exec", "-contract", batch); code != 2 || !strings.Contains(errOut, "no program given") {
		t.Fatalf("exit %d\nstderr:\n%s", code, errOut)
	}
}

// docuconfProcess runs the CLI in a child process (this test binary,
// re-executed), since a successful exec replaces the process.
func docuconfProcess(t *testing.T, args ...string) (output string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "--")
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = append(os.Environ(), "DOCUCONF_HELPER_PROCESS=1")
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

// TestHelperProcess is the CLI in a child process, for docuconfProcess.
// It prints its PID first, so a test can see exec kept it.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("DOCUCONF_HELPER_PROCESS") != "1" {
		t.Skip("helper process")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Stdout.WriteString("pid " + strconv.Itoa(os.Getpid()) + "\n")
	os.Exit(run(args, os.Stdout, os.Stderr))
}

func TestExecReplacesTheProcess(t *testing.T) {
	bootEnv(t)
	out, code := docuconfProcess(t, "exec", "-contract", batch, "--", "sh", "-c", `echo "pid $$"; echo "db $DATABASE_URL"; exit 3`)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 3 || len(lines) != 3 {
		t.Fatalf("exit %d, want the program's 3\n%s", code, out)
	}
	// Same PID: the program replaced docuconf rather than running under it.
	if lines[0] != lines[1] {
		t.Fatalf("the program ran in another process:\n%s", out)
	}
	// The environment reaches the program unchanged.
	if lines[2] != "db postgres://orders:hunter2@db/orders" {
		t.Fatalf("environment not passed on:\n%s", out)
	}
}

func TestExecRefusesInvalidConfiguration(t *testing.T) {
	bootEnv(t)
	t.Setenv("PORT", "0")
	marker := filepath.Join(t.TempDir(), "ran")
	out, code := docuconfProcess(t, "exec", "-contract", batch, "--", "touch", marker)
	if code != 1 || !strings.Contains(out, "PORT: 0 is below min 1 (out_of_range)") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the program ran despite the problems")
	}
}

func TestExecMissingProgram(t *testing.T) {
	bootEnv(t)
	out, code := docuconfProcess(t, "exec", "-contract", batch, "--", "docuconf-no-such-program")
	if code != 127 || !strings.Contains(out, "docuconf exec: cannot start docuconf-no-such-program: not found in PATH\n") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	out, code = docuconfProcess(t, "exec", "-contract", batch, "--", "./nope")
	if code != 127 || !strings.Contains(out, "docuconf exec: cannot start ./nope: no such file\n") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	notExec := write(t, "data.txt", "not a program\n")
	out, code = docuconfProcess(t, "exec", "-contract", batch, "--", notExec)
	if code != 127 || !strings.Contains(out, "permission denied (is it executable?)") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// printEnv is a program that prints the variables exec sets.
const printEnv = `for v in DATABASE_URL PORT ALLOWED_ORIGINS REQUEST_TIMEOUT ORDERS_FILE; do eval "echo $v=\${$v-unset}"; done`

func TestExecPassesEnvFileValues(t *testing.T) {
	bootEnv(t)
	os.Unsetenv("DATABASE_URL")
	os.Unsetenv("ORDERS_FILE")
	dotEnv := write(t, ".env", "DATABASE_URL=postgres://orders:pw@db/orders\nPORT=9000\nORDERS_FILE=/data/in.txt\n")
	t.Setenv("PORT", "9100") // the environment wins over the file
	out, code := docuconfProcess(t, "exec", "-contract", batch, "-env-file", dotEnv, "-no-defaults", "--", "sh", "-c", printEnv)
	want := "DATABASE_URL=postgres://orders:pw@db/orders\nPORT=9100\nALLOWED_ORIGINS=unset\nREQUEST_TIMEOUT=unset\nORDERS_FILE=/data/in.txt\n"
	if code != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("exit %d\n%s\nwant suffix:\n%s", code, out, want)
	}
}

func TestExecMissingEnvFile(t *testing.T) {
	bootEnv(t)
	out, code := docuconfProcess(t, "exec", "-contract", batch, "-env-file", "no-such.env", "--", "true")
	if code != 2 || !strings.Contains(out, "-env-file no-such.env: no such file") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestExecAppliesDefaults(t *testing.T) {
	root := bootEnv(t)
	os.Unsetenv("ORDERS_FILE")
	if err := os.WriteFile(filepath.Join(root, "data", "orders.txt"), []byte("A-1 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REQUEST_TIMEOUT", "") // empty is unset for a duration
	out, code := docuconfProcess(t, "exec", "-contract", batch, "--", "sh", "-c", printEnv)
	want := "DATABASE_URL=postgres://orders:hunter2@db/orders\nPORT=8080\nALLOWED_ORIGINS=http://localhost:3000\nREQUEST_TIMEOUT=30s\nORDERS_FILE=" + filepath.Join(root, "data/orders.txt") + "\n"
	if code != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("exit %d\n%s\nwant suffix:\n%s", code, out, want)
	}

	// -no-defaults leaves them unset.
	out, code = docuconfProcess(t, "exec", "-contract", batch, "-no-defaults", "--", "sh", "-c", printEnv)
	want = "PORT=unset\nALLOWED_ORIGINS=unset\nREQUEST_TIMEOUT=\nORDERS_FILE=unset\n"
	if code != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("exit %d\n%s\nwant suffix:\n%s", code, out, want)
	}
}

// Defaults are exported in each variable's wire encoding.
func TestExecDefaultsUseTheWireEncoding(t *testing.T) {
	bootEnv(t)
	contract := write(t, "enc.cue", `package enc

import "docuconf.dev/contract"

contract.#Contract & {
	metadata: {name: "enc", generator: {language: "cobol", sdk: "docuconf-test", version: "1"}}
	vars: {
		ISO: {type: "duration", description: "An ISO duration", encoding: "iso8601", default: "1m30s"}
		SECS: {type: "duration", description: "Whole seconds", encoding: "seconds", default: "2m"}
		SEMI: {type: "list", description: "A semicolon list", items: "string", separator: ";", default: ["a", "b"]}
		JSONL: {type: "list", description: "A json list", items: "int", encoding: "json", default: [1, 2]}
		IDX: {type: "list", description: "An indexed list", items: "string", encoding: "indexed", default: ["x", "y"]}
		SET: {type: "list", description: "An indexed list that is set", items: "string", encoding: "indexed", default: ["d"]}
		PRICE: {type: "float", description: "A price in dollars", default: 0.5}
		MSG: {type: "string", description: "A message", default: "costs $5"}
		FLAG: {type: "bool", description: "A flag", default: true}
	}
}
`)
	t.Setenv("SET__0", "mine")
	out, code := docuconfProcess(t, "exec", "-contract", contract, "--", "sh", "-c",
		`for v in ISO SECS SEMI JSONL IDX__0 IDX__1 SET__0 PRICE MSG FLAG; do eval "echo $v=\${$v-unset}"; done`)
	want := "ISO=PT90S\nSECS=120\nSEMI=a;b\nJSONL=[1,2]\nIDX__0=x\nIDX__1=y\nSET__0=mine\nPRICE=0.5\nMSG=costs $5\nFLAG=true\n"
	if code != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("exit %d\n%s\nwant suffix:\n%s", code, out, want)
	}
}
