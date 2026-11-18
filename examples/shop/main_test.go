package main

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go"
	"github.com/docuconf/docuconf-go/examples/shop/internal/config"
)

// keyPath is where the webhook-signing-key file input lives, under the
// file root.
const keyPath = "etc/shop/webhooks/signing.key"

// TestMatrix runs every case of the README's table: each starts from the
// base env, with a fresh, empty file root.
func TestMatrix(t *testing.T) {
	type problem struct{ input, code, message string } // message: a substring
	cases := []struct {
		name string
		env  map[string]string
		key  bool // write a 32+ character key file under the root
		out  string
		want []problem // when set, the only problems
	}{
		{name: "1 web", env: map[string]string{"DOCUCONF_ROLE": "web", "ALLOWED_ORIGINS": "https://shop.example"},
			out: "shop web: port 8080, origins https://shop.example"},
		{name: "2 worker needs no ALLOWED_ORIGINS", env: map[string]string{"DOCUCONF_ROLE": "worker", "QUEUES": "default,mail"},
			out: "shop worker: queues default,mail, webhooks off"},
		{name: "3 web without ALLOWED_ORIGINS", env: map[string]string{"DOCUCONF_ROLE": "web"},
			want: []problem{{"ALLOWED_ORIGINS", "missing_required", "is required but not set"}}},
		{name: "4 no role", env: map[string]string{"QUEUES": "default"},
			want: []problem{{"DOCUCONF_ROLE", "missing_required", "is required but not set"}}},
		{name: "5 unknown role", env: map[string]string{"DOCUCONF_ROLE": "cron"},
			want: []problem{{"DOCUCONF_ROLE", "not_in_enum", `"cron" is not one of web, worker`}}},
		{name: "6 json logs need a schema", env: map[string]string{"DOCUCONF_ROLE": "worker", "QUEUES": "default", "LOG_FORMAT": "json"},
			want: []problem{{"LOG_SCHEMA_URL", "missing_required", `is required when LOG_FORMAT is "json"`}}},
		{name: "7 json logs with a schema", env: map[string]string{"DOCUCONF_ROLE": "worker", "QUEUES": "default", "LOG_FORMAT": "json", "LOG_SCHEMA_URL": "https://logs.example/v1.json"},
			out: "shop worker: queues default, webhooks off"},
		{name: "8 webhooks need the key", env: map[string]string{"DOCUCONF_ROLE": "worker", "QUEUES": "default", "WEBHOOKS_ENABLED": "true"},
			want: []problem{{"webhook-signing-key", "file_missing", ", and is required when WEBHOOKS_ENABLED is true"}}},
		{name: "9 webhooks with the key", env: map[string]string{"DOCUCONF_ROLE": "worker", "QUEUES": "default", "WEBHOOKS_ENABLED": "true"}, key: true,
			out: "shop worker: queues default, webhooks on"},
		{name: "10 the key belongs to the worker", env: map[string]string{"DOCUCONF_ROLE": "web", "ALLOWED_ORIGINS": "https://shop.example", "WEBHOOKS_ENABLED": "true"},
			out: "shop web: port 8080, origins https://shop.example"},
		{name: "11 fragment vars are checked", env: map[string]string{"DOCUCONF_ROLE": "web", "ALLOWED_ORIGINS": "https://shop.example", "OTEL_EXPORTER_OTLP_PROTOCOL": "carrier-pigeon"},
			want: []problem{{"OTEL_EXPORTER_OTLP_PROTOCOL", "not_in_enum", `"carrier-pigeon" is not one of grpc, http/protobuf, http/json`}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := map[string]string{"DATABASE_URL": "postgres://shop@db:5432/shop"}
			maps.Copy(env, c.env)
			root := t.TempDir()
			if c.key {
				p := filepath.Join(root, keyPath)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(strings.Repeat("k", 40)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := docuconf.ParseWithOptions[config.Config](docuconf.Options{
				Environment:    env,
				FileRoot:       root,
				TerminationLog: "-", // do not write /dev/termination-log
			})
			if c.want == nil {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				if got := summary(env["DOCUCONF_ROLE"], cfg); got != c.out {
					t.Errorf("summary: got %q, want %q", got, c.out)
				}
				return
			}
			var verr *docuconf.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("want a ValidationError, got %v", err)
			}
			if len(verr.Violations) != len(c.want) {
				t.Fatalf("want %d problems, got:\n%v", len(c.want), err)
			}
			for i, w := range c.want {
				v := verr.Violations[i]
				if v.Input != w.input || string(v.Code) != w.code || !strings.Contains(v.Message, w.message) {
					t.Errorf("problem %d: got %s %s %q, want %s %s containing %q", i+1, v.Input, v.Code, v.Message, w.input, w.code, w.message)
				}
			}
			if strings.Contains(err.Error(), "shop@db") {
				t.Errorf("the secret DATABASE_URL leaked into the error:\n%v", err)
			}
		})
	}
}
