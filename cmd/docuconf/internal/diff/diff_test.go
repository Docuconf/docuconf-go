package diff

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// base is a contract as platform.ContractJSON writes it, defaults filled.
const base = `{
  "apiVersion": "docuconf.dev/v1alpha1", "kind": "Contract", "metadata": {"name": "orders-api"},
  "vars": {
    "PORT": {"name": "PORT", "type": "int", "description": "HTTP listen port", "required": false, "secret": false, "default": 8080, "min": 1, "max": 65535},
    "BIG": {"name": "BIG", "type": "int", "description": "A 64-bit bound", "required": false, "secret": false, "max": 9223372036854775807},
    "LOG_LEVEL": {"name": "LOG_LEVEL", "type": "enum", "description": "Log level", "required": false, "secret": false, "default": "info", "values": ["debug", "info", "warn"]},
    "TIMEOUT": {"name": "TIMEOUT", "type": "duration", "description": "Request time limit", "required": false, "secret": false, "encoding": "go", "min": "1s", "max": "5m"},
    "NAME": {"name": "NAME", "type": "string", "description": "Display name", "required": false, "secret": false, "maxLength": 40},
    "UPSTREAM": {"name": "UPSTREAM", "type": "url", "description": "Upstream service", "required": true, "secret": false, "schemes": ["https", "http"]},
    "ORIGINS": {"name": "ORIGINS", "type": "list", "description": "Allowed origins", "required": false, "secret": false, "items": "string", "encoding": "csv", "separator": ","},
    "DATABASE_URL": {"name": "DATABASE_URL", "type": "string", "description": "Database URL", "required": true, "secret": true},
    "KEYS": {"name": "KEYS", "type": "list", "description": "Webhook keys", "required": false, "secret": true, "items": "string", "encoding": "csv", "separator": ","},
    "LIMITS": {"name": "LIMITS", "type": "json", "description": "Rate limits", "required": false, "secret": false,
      "schema": {"type": "object", "properties": {"perMinute": {"type": "integer", "minimum": 1}, "burst": {"type": "integer"}}, "required": ["perMinute"]}},
    "OLD_PORT": {"name": "OLD_PORT", "type": "int", "description": "The old port", "required": false, "secret": false, "deprecated": {"message": "Use PORT"}}
  },
  "files": {
    "serving-tls": {"name": "serving-tls", "type": "tls", "description": "Serving certificate", "required": false, "secret": true, "path": "/etc/orders/tls", "reload": "restart", "requireCA": false, "dnsNames": ["orders.internal"], "keyAlgorithms": ["ECDSA", "RSA"], "minRemaining": "720h"},
    "routes": {"name": "routes", "type": "config", "description": "Routing table", "required": false, "secret": false, "path": "/etc/orders/routes.yaml", "reload": "watch", "format": "yaml"}
  },
  "overlays": {"platform": {"name": "platform", "format": "json", "path": "/app/config/appsettings.Production.json", "keySeparator": ":", "reload": "restart"}},
  "profiles": {"selector": "DOTNET_ENVIRONMENT", "default": "Production", "defaults": {"Production": {"UPSTREAM": "https://up.internal"}}}
}`

type want struct{ class, kind, input, field, message string }

func TestContracts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c map[string]any)
		want   []want // the breaking and notable changes, all of them
		compat []want // compatible changes that must be among the rest
	}{
		{name: "unchanged", mutate: func(map[string]any) {}},
		{name: "add optional var", mutate: func(c map[string]any) {
			vars(c)["WORKERS"] = map[string]any{"name": "WORKERS", "type": "int", "description": "Worker count", "required": false, "secret": false}
		}, compat: []want{{Compatible, "var", "WORKERS", "", "an optional variable is added"}}},
		{name: "add required var", mutate: func(c map[string]any) {
			vars(c)["REGION"] = map[string]any{"name": "REGION", "type": "string", "description": "Cloud region", "required": true, "secret": false}
		}, want: []want{{Breaking, "var", "REGION", "", "a required variable is added: the platform must supply it"}}},
		{name: "remove var", mutate: func(c map[string]any) { delete(vars(c), "PORT") },
			want: []want{{Breaking, "var", "PORT", "", "deprecate it in a release first"}}},
		{name: "remove deprecated var", mutate: func(c map[string]any) { delete(vars(c), "OLD_PORT") },
			want: []want{{Breaking, "var", "OLD_PORT", "", "it was deprecated (Use PORT), so a platform that heeded the warning is unaffected"}}},
		{name: "deprecate", mutate: func(c map[string]any) {
			v(c, "PORT")["deprecated"] = map[string]any{"message": "Use LISTEN_PORT", "replacedBy": "LISTEN_PORT"}
		}, want: []want{{Notable, "var", "PORT", "deprecated", "is deprecated: the platform should stop setting it (Use LISTEN_PORT); it is replaced by LISTEN_PORT"}}},
		{name: "optional to required", mutate: func(c map[string]any) { v(c, "PORT")["required"] = true; delete(v(c, "PORT"), "default") },
			want: []want{{Breaking, "var", "PORT", "required", "becomes required"}, {Notable, "var", "PORT", "default", "loses its default, 8080"}}},
		{name: "required to optional", mutate: func(c map[string]any) { v(c, "UPSTREAM")["required"] = false },
			compat: []want{{Compatible, "var", "UPSTREAM", "required", "becomes optional"}}},
		{name: "change type", mutate: func(c map[string]any) { v(c, "NAME")["type"] = "url" },
			want: []want{{Breaking, "var", "NAME", "type", "its type changes from string to url"}}},
		{name: "become secret", mutate: func(c map[string]any) { v(c, "NAME")["secret"] = true },
			want: []want{{Breaking, "var", "NAME", "secret", "becomes secret"}}},
		{name: "secret list to keySet", mutate: func(c map[string]any) {
			k := v(c, "KEYS")
			k["type"] = "keySet"
			delete(k, "items")
			k["minKeys"], k["maxKeys"] = json.Number("1"), json.Number("2")
		}, want: []want{{Breaking, "var", "KEYS", "type", "the wire format and the Secret that holds the keys stay the same"}}},
		{name: "tighten range", mutate: func(c map[string]any) {
			v(c, "PORT")["max"] = json.Number("1024")
			v(c, "PORT")["min"] = json.Number("80")
		},
			want: []want{{Breaking, "var", "PORT", "max", "its max falls from 65535 to 1024"}, {Breaking, "var", "PORT", "min", "its min rises from 1 to 80"}}},
		{name: "loosen range", mutate: func(c map[string]any) { delete(v(c, "PORT"), "max"); v(c, "PORT")["min"] = json.Number("0") },
			compat: []want{{Compatible, "var", "PORT", "max", "loses max 65535"}, {Compatible, "var", "PORT", "min", "its min falls from 1 to 0"}}},
		{name: "int64 bound exactly", mutate: func(c map[string]any) { v(c, "BIG")["max"] = json.Number("9223372036854775806") },
			want: []want{{Breaking, "var", "BIG", "max", "falls from 9223372036854775807 to 9223372036854775806"}}},
		{name: "durations", mutate: func(c map[string]any) { v(c, "TIMEOUT")["max"] = "90s"; v(c, "TIMEOUT")["min"] = "500ms" },
			want:   []want{{Breaking, "var", "TIMEOUT", "max", `its max falls from "5m" to "90s"`}},
			compat: []want{{Compatible, "var", "TIMEOUT", "min", `its min falls from "1s" to "500ms"`}}},
		{name: "add constraint", mutate: func(c map[string]any) {
			v(c, "NAME")["pattern"] = "^[a-z]+$"
			v(c, "NAME")["minLength"] = json.Number("2")
		},
			want: []want{{Breaking, "var", "NAME", "pattern", "gains pattern"}, {Breaking, "var", "NAME", "minLength", "gains minLength 2"}}},
		{name: "fewer enum values", mutate: func(c map[string]any) { v(c, "LOG_LEVEL")["values"] = []any{"info", "warn", "error"} },
			want:   []want{{Breaking, "var", "LOG_LEVEL", "values", "debug is no longer allowed"}},
			compat: []want{{Compatible, "var", "LOG_LEVEL", "values", "error is now allowed"}}},
		{name: "fewer schemes", mutate: func(c map[string]any) { v(c, "UPSTREAM")["schemes"] = []any{"https"} },
			want: []want{{Breaking, "var", "UPSTREAM", "schemes", "http is no longer allowed"}}},
		{name: "docs and default", mutate: func(c map[string]any) {
			v(c, "PORT")["description"] = "The HTTP port"
			v(c, "PORT")["group"] = "http"
			v(c, "PORT")["default"] = json.Number("9090")
		}, want: []want{{Notable, "var", "PORT", "default", "its default changes from 8080 to 9090"}},
			compat: []want{{Compatible, "var", "PORT", "description", "docs only"}, {Compatible, "var", "PORT", "group", "docs only"}}},
		{name: "encoding", mutate: func(c map[string]any) { v(c, "ORIGINS")["encoding"] = "json"; delete(v(c, "ORIGINS"), "separator") },
			want: []want{{Notable, "var", "ORIGINS", "encoding", `its encoding changes from "csv" to "json": the platform renders the new wire form`}}},
		{name: "schema tightened", mutate: func(c map[string]any) {
			s := v(c, "LIMITS")["schema"].(map[string]any)
			s["required"] = []any{"perMinute", "burst"}
			s["additionalProperties"] = false
			s["properties"].(map[string]any)["perMinute"].(map[string]any)["minimum"] = json.Number("10")
		}, want: []want{
			{Breaking, "var", "LIMITS", "schema", "/: property burst is now required"},
			{Breaking, "var", "LIMITS", "schema", "/: properties it does not list are no longer allowed"},
			{Breaking, "var", "LIMITS", "schema", "/properties/perMinute: minimum rises to 10"},
		}},
		{name: "schema loosened", mutate: func(c map[string]any) {
			s := v(c, "LIMITS")["schema"].(map[string]any)
			s["required"] = []any{}
			s["properties"].(map[string]any)["perMinute"].(map[string]any)["type"] = []any{"integer", "string"}
		}, compat: []want{{Compatible, "var", "LIMITS", "schema", "accepts more"}}},
		{name: "schema type narrowed", mutate: func(c map[string]any) {
			v(c, "LIMITS")["schema"].(map[string]any)["properties"].(map[string]any)["burst"] = map[string]any{"type": "integer", "maximum": json.Number("100")}
			v(c, "LIMITS")["schema"].(map[string]any)["anyOf"] = []any{map[string]any{"required": []any{"burst"}}}
		}, want: []want{
			{Breaking, "var", "LIMITS", "schema", "/: anyOf changes, which diff cannot compare"},
			{Breaking, "var", "LIMITS", "schema", "/properties/burst: maximum falls to 100"},
		}},
		{name: "required file added", mutate: func(c map[string]any) {
			files(c)["license"] = map[string]any{"name": "license", "type": "text", "description": "Licence key", "required": true, "secret": false, "path": "/etc/orders/license", "reload": "restart"}
		}, want: []want{{Breaking, "file", "license", "", "a required file input is added"}}},
		{name: "file moved", mutate: func(c map[string]any) {
			f(c, "routes")["path"] = "/etc/orders/routing.yaml"
			f(c, "routes")["format"] = "json"
		},
			want: []want{
				{Notable, "file", "routes", "format", "breaking for the app image only"},
				{Notable, "file", "routes", "path", "breaking for the app image only"},
			}},
		{name: "certificate tightened", mutate: func(c map[string]any) {
			t := f(c, "serving-tls")
			t["dnsNames"] = []any{"orders.internal", "api.example.com"}
			t["keyAlgorithms"] = []any{"ECDSA"}
			t["minRemaining"] = "1440h"
			t["requireCA"] = true
		}, want: []want{
			{Breaking, "file", "serving-tls", "dnsNames", "must also cover api.example.com"},
			{Breaking, "file", "serving-tls", "keyAlgorithms", "RSA is no longer allowed"},
			{Breaking, "file", "serving-tls", "minRemaining", `rises from "720h" to "1440h"`},
			{Breaking, "file", "serving-tls", "requireCA", "now requires ca.crt"},
		}},
		{name: "reload", mutate: func(c map[string]any) { f(c, "routes")["reload"] = "restart"; f(c, "serving-tls")["reload"] = "watch" },
			want:   []want{{Notable, "file", "routes", "reload", "a changed source now needs a rollout"}},
			compat: []want{{Compatible, "file", "serving-tls", "reload", "is now reloaded by the app"}}},
		{name: "overlay removed", mutate: func(c map[string]any) { delete(c, "overlays") },
			want: []want{{Breaking, "overlay", "platform", "", "the overlay is removed"}}},
		{name: "profile no longer supplies a required var", mutate: func(c map[string]any) {
			c["profiles"].(map[string]any)["defaults"] = map[string]any{"Production": map[string]any{}}
		}, want: []want{{Breaking, "profile", "Production", "UPSTREAM", "Production no longer supplies UPSTREAM, which is required"}}},
		{name: "renamed service", mutate: func(c map[string]any) { c["metadata"].(map[string]any)["name"] = "orders" },
			want: []want{{Notable, "contract", "", "metadata.name", "the service is renamed from orders-api to orders"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := parse(t, base)
			tc.mutate(c)
			next, _ := json.Marshal(c)
			got, err := Contracts([]byte(base), next)
			if err != nil {
				t.Fatal(err)
			}
			var loud []Change
			for _, g := range got {
				if g.Class != Compatible {
					loud = append(loud, g)
				}
			}
			for _, w := range append(tc.want, tc.compat...) {
				if !has(got, w) {
					t.Errorf("no %s %s change to %s %s with %q; got:\n%s", w.class, w.kind, w.input, w.field, w.message, describe(got))
				}
			}
			if len(loud) != len(tc.want) {
				t.Errorf("want %d breaking or notable changes, got:\n%s", len(tc.want), describe(loud))
			}
		})
	}
}

func parse(t *testing.T, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var c map[string]any
	if err := dec.Decode(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

func vars(c map[string]any) map[string]any  { return c["vars"].(map[string]any) }
func files(c map[string]any) map[string]any { return c["files"].(map[string]any) }
func v(c map[string]any, n string) map[string]any {
	return vars(c)[n].(map[string]any)
}
func f(c map[string]any, n string) map[string]any { return files(c)[n].(map[string]any) }

func has(got []Change, w want) bool {
	for _, g := range got {
		if g.Class == w.class && g.Kind == w.kind && g.Input == w.input && g.Field == w.field && strings.Contains(g.Message, w.message) {
			return true
		}
	}
	return false
}

func describe(cs []Change) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "  %s %s %s %s: %s\n", c.Class, c.Kind, c.Input, c.Field, c.Message)
	}
	return b.String()
}
