package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/lint"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// The containerapps target: the module, applied, against docuconf render;
// its validations; and docuconf lint terraform on a plan made with the
// azurerm provider (from CI's registry, or TF_CLI_CONFIG_FILE's mirror)
// against fakeAzure.

const (
	kvToken = "https://kitchen-kv.vault.azure.net/secrets/api-token"
	kvDB    = "https://kitchen-kv.vault.azure.net/secrets/database-url/0123456789abcdef0123456789abcdef"
	uaiID   = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/kitchen/providers/Microsoft.ManagedIdentity/userAssignedIdentities/kitchen-app"
)

var containerappsBase = map[string]any{
	"API_TOKEN":    map[string]any{"secret": map[string]any{"key_vault_secret_id": kvToken}},
	"DATABASE_URL": map[string]any{"secret": map[string]any{"key_vault_secret_id": kvDB, "identity": uaiID}},
	"TENANT":       map[string]any{"value": "acme"},
}

func containerappsArgs(args map[string]any) map[string]any {
	out := map[string]any{"source": "./module"}
	for k, v := range containerappsBase {
		out[k] = v
	}
	for k, v := range args {
		out[k] = v
	}
	return escapeTemplates(out).(map[string]any)
}

func TestTerraformContainerAppsMatchesRender(t *testing.T) {
	bin := terraformBinary(t)
	dir := newTargetRoot(t, kitchen, "containerapps")
	p, err := platform.New()
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.LoadContract(kitchen)
	if err != nil {
		t.Fatal(err)
	}
	literal := map[string]bool{"literals": true, "durations": true, "whole_durations": true, "lists_and_json": true, "code_points": true}
	modules, outputs := map[string]any{}, map[string]any{}
	out := func(name string) map[string]any {
		return map[string]any{"value": map[string]any{
			"env":     "${module." + name + ".containerapp_env}",
			"secrets": "${module." + name + ".containerapp_secrets}",
			"volumes": "${module." + name + ".containerapp_volumes}",
			"mounts":  "${module." + name + ".containerapp_volume_mounts}",
		}}
	}
	for _, tc := range renderCases {
		if !literal[tc.name] {
			continue
		}
		args := map[string]any{}
		for k, v := range tc.values {
			args[k] = map[string]any{"value": v}
		}
		modules[tc.name] = containerappsArgs(args)
		outputs[tc.name] = out(tc.name)
	}
	modules["files"] = containerappsArgs(map[string]any{
		"settings": map[string]any{"secret": map[string]any{"key_vault_secret_id": "https://kitchen-kv.vault.azure.net/secrets/settings"}},
		"keystore": map[string]any{"secret": map[string]any{"key_vault_secret_id": "https://kitchen-kv.vault.azure.net/secrets/ks", "identity": uaiID}},
		"ca":       map[string]any{"injected": map[string]any{"provider": "trust-manager"}},
	})
	outputs["files"] = out("files")
	initRoot(t, bin, dir, map[string]any{"module": modules, "output": outputs})
	if out, err := tf(t, bin, filepath.Join(dir, "module"), "fmt", "-check", "-diff", "-no-color"); err != nil {
		t.Errorf("the generated module is not formatted: %v\n%s", err, out)
	}
	if out, err := tf(t, bin, dir, "apply", "-auto-approve", "-input=false", "-no-color"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	raw, err := tf(t, bin, dir, "output", "-json")
	if err != nil {
		t.Fatalf("output: %v\n%s", err, raw)
	}
	var got map[string]struct {
		Value struct {
			Env     []map[string]any `json:"env"`
			Secrets []map[string]any `json:"secrets"`
			Volumes []map[string]any `json:"volumes"`
			Mounts  []map[string]any `json:"mounts"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	secretEnv := []any{
		map[string]any{"name": "API_TOKEN", "secret_name": "dc-api-token"},
		map[string]any{"name": "DATABASE_URL", "secret_name": "dc-database-url"},
	}
	appSecrets := []any{
		map[string]any{"name": "dc-api-token", "key_vault_secret_id": kvToken, "identity": "System"},
		map[string]any{"name": "dc-database-url", "key_vault_secret_id": kvDB, "identity": uaiID},
	}
	for _, tc := range renderCases {
		if !literal[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			want := renderDoc(t, p, c, tc.doc)
			// Container Apps expands $(VAR) as the kubelet does, so the
			// module's values are docuconf render's, escaping included.
			var env []any
			for _, e := range want.Env {
				m := e.(map[string]any)
				if v, ok := m["value"].(string); ok {
					env = append(env, map[string]any{"name": m["name"], "value": v})
				}
			}
			var gotEnv, gotSecrets []any
			for _, e := range got[tc.name].Value.Env {
				if e["secret_name"] != nil {
					if e["value"] != nil {
						t.Errorf("%s has both a value and a secret_name", e["name"])
					}
					gotSecrets = append(gotSecrets, map[string]any{"name": e["name"], "secret_name": e["secret_name"]})
				} else {
					gotEnv = append(gotEnv, map[string]any{"name": e["name"], "value": e["value"]})
				}
			}
			if gj, wj := canon(t, gotEnv), canon(t, env); gj != wj {
				t.Errorf("containerapp_env values differ from docuconf render:\n module: %s\n render: %s", gj, wj)
			}
			if gj, wj := canon(t, gotSecrets), canon(t, secretEnv); gj != wj {
				t.Errorf("containerapp_env secrets: module %s, want %s", gj, wj)
			}
			if gj, wj := canon(t, got[tc.name].Value.Secrets), canon(t, appSecrets); gj != wj {
				t.Errorf("containerapp_secrets: module %s, want %s", gj, wj)
			}
			if len(got[tc.name].Value.Volumes) != 0 || len(got[tc.name].Value.Mounts) != 0 {
				t.Errorf("no file input is set, but the module renders volumes %v and mounts %v", got[tc.name].Value.Volumes, got[tc.name].Value.Mounts)
			}
		})
	}
	t.Run("files", func(t *testing.T) {
		g := got["files"].Value
		wantSecrets := append(append([]any{}, appSecrets...),
			map[string]any{"name": "dc-file-settings", "key_vault_secret_id": "https://kitchen-kv.vault.azure.net/secrets/settings", "identity": "System"},
			map[string]any{"name": "dc-file-keystore", "key_vault_secret_id": "https://kitchen-kv.vault.azure.net/secrets/ks", "identity": uaiID},
		)
		wantVols := []any{map[string]any{"name": "dc-secrets", "storage_type": "Secret"}}
		wantMounts := []any{
			map[string]any{"name": "dc-secrets", "path": "/etc/kitchen/settings/settings.yaml", "sub_path": "dc-file-settings"},
			map[string]any{"name": "dc-secrets", "path": "/etc/kitchen/ks/client.p12", "sub_path": "dc-file-keystore"},
		}
		if gj, wj := canon(t, g.Secrets), canon(t, wantSecrets); gj != wj {
			t.Errorf("containerapp_secrets:\n module %s\n want   %s", gj, wj)
		}
		if gj, wj := canon(t, g.Volumes), canon(t, wantVols); gj != wj {
			t.Errorf("containerapp_volumes:\n module %s\n want   %s", gj, wj)
		}
		if gj, wj := canon(t, g.Mounts), canon(t, wantMounts); gj != wj {
			t.Errorf("containerapp_volume_mounts:\n module %s\n want   %s", gj, wj)
		}
		found := false
		for _, e := range g.Env {
			found = found || (e["name"] == "SETTINGS_PATH" && e["value"] == "/etc/kitchen/settings/settings.yaml")
		}
		if !found {
			t.Errorf("no SETTINGS_PATH entry: %v", g.Env)
		}
	})
}

func TestTerraformContainerAppsRejects(t *testing.T) {
	bin := terraformBinary(t)
	dir := newTargetRoot(t, kitchen, "containerapps")
	initRoot(t, bin, dir, map[string]any{"module": map[string]any{"c": map[string]any{"source": "./module"}}})
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"secret_as_value", map[string]any{"API_TOKEN": map[string]any{"value": "hunter2"}},
			"API_TOKEN: set exactly one of secret or injected (a secret is never a literal value"},
		{"field", map[string]any{"NODE_NAME": map[string]any{"field": map[string]any{"field_path": "spec.nodeName"}}},
			"NODE_NAME: set exactly one of value or injected (Container Apps has no ConfigMaps or Downward API"},
		{"secret_id", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"key_vault_secret_id": "api-token"}}},
			`API_TOKEN: secret.key_vault_secret_id "api-token" is not a Key Vault secret ID`},
		{"identity", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"key_vault_secret_id": kvToken, "identity": "kitchen-app"}}},
			`API_TOKEN: secret.identity "kitchen-app" is not System or a user-assigned identity's resource ID`},
		{"file_inline", map[string]any{"settings": map[string]any{"inline": "a: 1"}},
			"settings: set exactly one of secret or injected (Container Apps mounts files from Key Vault secrets"},
		{"file_secret_id", map[string]any{"settings": map[string]any{"secret": map[string]any{"key_vault_secret_id": "https://kitchen-kv.vault.azure.net/keys/settings"}}},
			`settings: secret.key_vault_secret_id "https://kitchen-kv.vault.azure.net/keys/settings" is not a Key Vault secret ID`},
		{"value_checks", map[string]any{"WORKERS": map[string]any{"value": 0}}, "WORKERS: 0 is below min 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeJSON(t, filepath.Join(dir, "main.tf.json"), map[string]any{"module": map[string]any{"c": containerappsArgs(tc.args)}})
			out, err := tf(t, bin, dir, "plan", "-input=false", "-no-color", "-json")
			if err == nil {
				t.Fatalf("plan succeeded; want an error containing %q", tc.want)
			}
			for _, m := range diagnostics(out) {
				if strings.Contains(m, tc.want) {
					return
				}
			}
			t.Errorf("no error contains %q; got:\n  %s", tc.want, strings.Join(diagnostics(out), "\n  "))
		})
	}
}

func TestTerraformContainerAppsTLS(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", kitchen, "-target", "containerapps", "-out", t.TempDir()}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, want := range []string{"warning: serving-tls: a tls input is a directory of several files", "warning: overlay appjson: Container Apps has no ConfigMaps"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("no %q in:\n%s", want, stderr.String())
		}
	}
	dir := t.TempDir()
	src, _ := os.ReadFile(kitchen)
	file := filepath.Join(dir, "contract.cue")
	os.WriteFile(file, []byte(strings.Replace(string(src), `"serving-tls": {type: "tls",`, `"serving-tls": {type: "tls", required: true,`, 1)), 0o644)
	stderr.Reset()
	code := run([]string{"terraform", "-contract", file, "-target", "containerapps", "-out", filepath.Join(dir, "m")}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "the contract requires tls inputs serving-tls") {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
}

func TestTerraformContainerAppsSecretNames(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(kitchen)
	if err != nil {
		t.Fatal(err)
	}
	// API_TOKEN and API__TOKEN both fold to the Container Apps secret dc-api-token.
	const tok = `API_TOKEN: {type: "string", description: "Token for the upstream API", required: true, secret: true, configKey: "api.token"}`
	cue := strings.Replace(string(src), tok, tok+"\n\t\tAPI__TOKEN: {type: \"string\", description: \"Second token\", secret: true}", 1)
	if cue == string(src) {
		t.Fatal("kitchen contract changed; update this test")
	}
	file := filepath.Join(dir, "contract.cue")
	os.WriteFile(file, []byte(cue), 0o644)
	var stdout, stderr bytes.Buffer
	code := run([]string{"terraform", "-contract", file, "-target", "containerapps", "-out", filepath.Join(dir, "m")}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "inputs API_TOKEN and API__TOKEN would both be the Container Apps secret dc-api-token") {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
}

// fakeAzure serves the little of Azure Resource Manager that the azurerm
// provider asks for before it can plan: cloud metadata, a token and the
// resource providers. It returns the provider's metadata_host; the tests
// that plan trust its certificate after init, which downloads providers.
func fakeAzure(t *testing.T) (host, caFile string) {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/metadata/endpoints", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":                     "AzureCloud",
			"resourceManager":          srv.URL + "/",
			"microsoftGraphResourceId": srv.URL + "/",
			"authentication": map[string]any{
				"loginEndpoint":    srv.URL + "/",
				"audiences":        []string{srv.URL + "/"},
				"tenant":           "common",
				"identityProvider": "AAD",
			},
			"suffixes": map[string]any{"keyVaultDns": "vault.azure.net", "storage": "core.windows.net"},
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/providers") {
			w.Write([]byte(`{"value": []}`))
			return
		}
		// A token: the provider reads its claims, and nothing checks it.
		part := func(m map[string]any) string { b, _ := json.Marshal(m); return base64.RawURLEncoding.EncodeToString(b) }
		tok := part(map[string]any{"alg": "none", "typ": "JWT"}) + "." + part(map[string]any{"oid": "00000000-0000-0000-0000-000000000001", "tid": "00000000-0000-0000-0000-000000000000", "exp": 4102444800}) + ".x"
		json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 3600})
	})
	srv = httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	caFile = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(srv.URL, "https://"), caFile
}

func TestLintTerraformContainerApps(t *testing.T) {
	bin := terraformBinary(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", ordersContract, "-target", "containerapps", "-out", filepath.Join(dir, "modules", "orders-config")}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf terraform: %s", stderr.String())
	}
	host, ca := fakeAzure(t)
	app := func(name, extra string) string {
		return fmt.Sprintf(`
resource "azurerm_container_app" %[1]q {
  name                         = %[3]q
  container_app_environment_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/orders/providers/Microsoft.App/managedEnvironments/orders"
  resource_group_name          = "orders"
  revision_mode                = "Single"
  tags                         = { app = module.%[1]s.contract.name }
  identity {
    type = "SystemAssigned"
  }
  dynamic "secret" {
    for_each = module.%[1]s.containerapp_secrets
    content {
      name                = secret.value.name
      key_vault_secret_id = secret.value.key_vault_secret_id
      identity            = secret.value.identity
    }
  }
  template {
    container {
      name   = "orders-api"
      image  = "registry.example.com/orders-api:2.3.0"
      cpu    = 0.5
      memory = "1Gi"
      dynamic "env" {
        for_each = module.%[1]s.containerapp_env
        content {
          name        = env.value.name
          value       = env.value.value
          secret_name = env.value.secret_name
        }
      }%[2]s
    }
    dynamic "volume" {
      for_each = module.%[1]s.containerapp_volumes
      content {
        name         = volume.value.name
        storage_type = volume.value.storage_type
      }
    }
  }
}
`, name, extra, strings.ReplaceAll(name, "_", "-"))
	}
	mount := `
      dynamic "volume_mounts" {
        for_each = module.%s.containerapp_volume_mounts
        content {
          name     = volume_mounts.value.name
          path     = volume_mounts.value.path
          sub_path = volume_mounts.value.sub_path
        }
      }`
	args := `
  DATABASE_URL = { secret = { key_vault_secret_id = "https://orders-kv.vault.azure.net/secrets/orders-db/0123456789abcdef0123456789abcdef" } }
  WEBHOOK_KEYS = { secret = { key_vault_secret_id = "https://orders-kv.vault.azure.net/secrets/orders-webhook-keys" } }
  LOG_LEVEL    = { value = "warn" }
  discounts    = { secret = { key_vault_secret_id = "https://orders-kv.vault.azure.net/secrets/orders-discounts" } }
`
	scenarios := []struct {
		name, args, extra string
		want              [][2]string
	}{
		{"good", args, fmt.Sprintf(mount, "good"), nil},
		{"typo", args, fmt.Sprintf(mount, "typo") + `
      env {
        name  = "PORTT"
        value = "9090"
      }`, [][2]string{{"undeclared_input", "PORTT: is not declared in the contract, and the module did not render it; did you mean PORT?"}}},
		{"mounts", args, `
      volume_mounts {
        name     = "dc-secrets"
        path     = "/etc/orders/discounts.yaml"
        sub_path = "dc-file-discounts"
      }`, [][2]string{{"mount_mismatch", "discounts: secret dc-file-discounts is mounted at /etc/orders/discounts.yaml, but the app reads it from /etc/orders/discounts/discounts.yaml"}}},
		{"unmounted", args, "", [][2]string{{"mount_mismatch", "discounts: the container does not mount secret dc-file-discounts from volume dc-secrets"}}},
		{"hand_value", args, fmt.Sprintf(mount, "hand_value") + `
      env {
        name  = "REQUEST_TIMEOUT"
        value = "10m"
      }`, [][2]string{{"invalid_value", "REQUEST_TIMEOUT: 10m is above max 5m"}}},
		{"escaped", strings.Replace(args, `LOG_LEVEL    = { value = "warn" }`, `ALLOWED_ORIGINS = { value = ["https://shop.example.com/$(x)"] }`, 1), fmt.Sprintf(mount, "escaped"), nil},
		{"other_vault", strings.Replace(args, "orders-kv.vault.azure.net/secrets/orders-db", "dev-kv.vault.azure.net/secrets/orders-db", 1), fmt.Sprintf(mount, "other_vault"), nil},
		// A file argument that is not a constant: the app's secret says what it is.
		{"file_expr", strings.Replace(args, `"https://orders-kv.vault.azure.net/secrets/orders-discounts"`, `"${local.vault}/secrets/orders-discounts"`, 1), fmt.Sprintf(mount, "file_expr"), nil},
	}
	var root strings.Builder
	fmt.Fprintf(&root, `terraform {
  required_providers {
    azurerm = { source = "hashicorp/azurerm" }
  }
}

provider "azurerm" {
  features {}
  subscription_id                 = "00000000-0000-0000-0000-000000000000"
  tenant_id                       = "00000000-0000-0000-0000-000000000000"
  client_id                       = "00000000-0000-0000-0000-000000000000"
  client_secret                   = "offline"
  resource_provider_registrations = "none"
  metadata_host                   = %q
}

locals {
  vault = "https://orders-kv.vault.azure.net"
}
`, host)
	for _, s := range scenarios {
		fmt.Fprintf(&root, "\nmodule %q {\n  source = \"./modules/orders-config\"%s}\n", s.name, s.args)
		root.WriteString(app(s.name, s.extra))
	}
	os.WriteFile(filepath.Join(dir, "main.tf"), []byte(root.String()), 0o644)
	if out, err := tfInit(t, bin, dir); err != nil {
		t.Fatalf("init (needs the azurerm provider): %v\n%s", err, out)
	}
	t.Setenv("SSL_CERT_FILE", ca)
	t.Setenv("NO_PROXY", "127.0.0.1")
	t.Setenv("no_proxy", "127.0.0.1")
	if out, err := tf(t, bin, dir, "plan", "-input=false", "-no-color", "-refresh=false", "-out", "plan.out"); err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	planJSON, err := tf(t, bin, dir, "show", "-json", "plan.out")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, planJSON)
	}
	planFile := filepath.Join(dir, "plan.json")
	os.WriteFile(planFile, planJSON, 0o644)
	lintRun := func(extra ...string) map[string][]lint.Finding {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		run(append([]string{"lint", "terraform", planFile, "-dir", dir, "-format", "json"}, extra...), &stdout, &stderr)
		var got struct{ Findings []lint.Finding }
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("lint output: %v\n%s%s", err, stdout.String(), stderr.String())
		}
		m := map[string][]lint.Finding{}
		for _, f := range got.Findings {
			m[f.Module] = append(m[f.Module], f)
		}
		return m
	}
	findings := lintRun()
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			got := findings["module."+s.name]
			if len(got) == 0 {
				t.Fatalf("no findings at all, not even notices: is the app bound to module.%s?", s.name)
			}
			for _, w := range s.want {
				if !hasFinding(got, w[0], w[1]) {
					t.Errorf("no %s finding with %q; got:\n%s", w[0], w[1], describeFindings(got))
				}
			}
			for _, f := range got {
				if f.Code == "deferred" {
					t.Errorf("everything is known at plan, but: %s", f.Message)
				}
				expected := f.Level == lint.Notice
				for _, w := range s.want {
					expected = expected || f.Code == w[0]
				}
				if !expected {
					t.Errorf("unexpected %s %s: %s", f.Level, f.Code, f.Message)
				}
			}
		})
	}
	t.Run("policy", func(t *testing.T) {
		policy := filepath.Join(dir, "prod.cue")
		os.WriteFile(policy, []byte(`secretReferences: [string]: azureKeyVault: {vault: "orders-kv.vault.azure.net", identity: "System"}
`), 0o644)
		got := lintRun("-policy", policy)
		if f := got["module.good"]; hasLevel(f, lint.Error, "policy_mismatch") {
			t.Errorf("module.good meets the policy:\n%s", describeFindings(f))
		}
		if !hasFinding(got["module.other_vault"], "policy_mismatch", `DATABASE_URL: azureKeyVault.vault "dev-kv.vault.azure.net" is not allowed by policy (policy prod.cue)`) {
			t.Errorf("no vault mismatch:\n%s", describeFindings(got["module.other_vault"]))
		}
	})
}
