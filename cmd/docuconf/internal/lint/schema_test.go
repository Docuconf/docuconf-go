package lint

import (
	"encoding/json"
	"testing"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/terraform"
)

func TestResolve(t *testing.T) {
	var s Schema
	if err := json.Unmarshal([]byte(`{"format_version": "1.0", "provider_schemas": {"registry.terraform.io/acme/cloud": {
		"resource_schemas": {"cloud_db": {"block": {
			"attributes": {
				"port":   {"type": "number"},
				"tags":   {"type": ["map", "string"]},
				"conn":   {"type": ["object", {"host": "string", "tls": "bool"}]},
				"hosts":  {"type": ["list", "string"]},
				"extra":  {"type": "dynamic"},
				"ep":     {"nested_type": {"nesting_mode": "list", "attributes": {"url": {"type": "string"}}}}
			},
			"block_types": {"endpoint": {"nesting_mode": "list", "block": {"attributes": {"secure": {"type": "bool"}}}}}
		}}},
		"data_source_schemas": {"cloud_region": {"block": {"attributes": {"zones": {"type": ["set", "string"]}}}}}
	}}}`), &s); err != nil {
		t.Fatal(err)
	}
	p := &Plan{Schema: &s, Resources: map[string]Resource{
		"cloud_db.main":           {Type: "cloud_db", Mode: "managed", Provider: "registry.terraform.io/acme/cloud"},
		"module.app.cloud_db.sub": {Type: "cloud_db", Mode: "managed", Provider: "registry.terraform.io/acme/cloud"},
		"data.cloud_region.here":  {Type: "cloud_region", Mode: "data", Provider: "registry.terraform.io/acme/cloud"},
	}}
	for _, c := range []struct{ scope, ref, kind string }{
		{"", "cloud_db.main.port", "number"},
		{"", "cloud_db.main.tags", "map"},
		{"", `cloud_db.main.tags["team"]`, "string"},
		{"", "cloud_db.main.conn.tls", "bool"},
		{"", "cloud_db.main.hosts[0]", "string"},
		{"", "cloud_db.main.endpoint", "list"},
		{"", "cloud_db.main.endpoint[0].secure", "bool"},
		{"", "cloud_db.main.ep[0].url", "string"},
		{"", "cloud_db.main[0].port", "number"},
		{"module.app", "cloud_db.sub.port", "number"},
		{"", "data.cloud_region.here.zones", "set"},
		{"", "cloud_db.main.extra", "dynamic"},
	} {
		got, _, ok := p.resolve(c.scope, c.ref)
		if !ok || got.kind != c.kind {
			t.Errorf("%s in %q: got %q (%v), want %s", c.ref, c.scope, got.kind, ok, c.kind)
		}
	}
	for _, ref := range []string{"cloud_db.main", "cloud_db.other.port", "var.port", "module.x.out", "cloud_db.main.nope"} {
		if _, _, ok := p.resolve("", ref); ok {
			t.Errorf("%s resolved", ref)
		}
	}
}

func TestTypeMismatch(t *testing.T) {
	v := func(typ string, raw map[string]any) terraform.Var {
		if raw == nil {
			raw = map[string]any{}
		}
		return terraform.Var{Type: typ, Raw: raw}
	}
	levels := map[string]any{"values": []any{"debug", "info"}}
	flags := map[string]any{"values": []any{"true", "false"}}
	codes := map[string]any{"values": []any{"200", "404"}}
	for _, c := range []struct {
		v    terraform.Var
		kind string
		bad  bool
	}{
		{v("string", nil), "bool", false},
		{v("string", nil), "number", false},
		{v("string", nil), "list", true},
		{v("url", nil), "bool", true},
		{v("url", nil), "string", false},
		{v("duration", nil), "number", true},
		{v("int", nil), "number", false},
		{v("int", nil), "string", true},
		{v("float", nil), "bool", true},
		{v("bool", nil), "bool", false},
		{v("bool", nil), "string", true},
		{v("enum", levels), "bool", true},
		{v("enum", flags), "bool", false},
		{v("enum", levels), "number", true},
		{v("enum", codes), "number", false},
		{v("list", nil), "set", false},
		{v("list", nil), "string", true},
		{v("json", nil), "object", false},
	} {
		if got := typeMismatch(c.v, ctyType{kind: c.kind}); (got != "") != c.bad {
			t.Errorf("%s ← %s: got %q, want a mismatch: %v", c.v.Type, c.kind, got, c.bad)
		}
	}
}
