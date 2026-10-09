package docs

import (
	"strings"
	"testing"
)

// fieldRows writes each row as "path | type | required | constraints".
func fieldRows(in Input) []string {
	var out []string
	for _, f := range in.Fields {
		var cons []string
		for _, c := range f.Constraints {
			cons = append(cons, c.Rule+": "+c.Text)
		}
		req := "optional"
		if f.Required {
			req = "required"
		}
		row := f.Path + " | " + f.Type + " | " + req + " | " + strings.Join(cons, "; ")
		if f.Schema != nil {
			row += " | " + string(f.Schema)
		}
		out = append(out, row)
	}
	return out
}

func TestFieldTable(t *testing.T) {
	m := build(t, `"DB": {"type": "json", "description": "Database settings", "schema": {
		"type": "object", "required": ["database"], "additionalProperties": false, "properties": {
			"database": {"type": "object", "description": "Where to connect", "required": ["host"], "properties": {
				"host": {"type": "string", "description": "Host name", "minLength": 1},
				"port": {"type": "integer", "default": 5432, "minimum": 1, "maximum": 65535},
				"mode": {"type": "string", "enum": ["rw", "ro"]}}},
			"replicas": {"type": "array", "maxItems": 3, "items": {"type": "object", "required": ["name"], "properties": {
				"name": {"type": "string", "pattern": "^[a-z]+$"}, "weight": {"type": "number", "exclusiveMinimum": 0}}}},
			"tags": {"type": "array", "uniqueItems": true, "items": {"type": "string", "maxLength": 8}},
			"labels": {"type": "object", "additionalProperties": {"type": "string"}},
			"backoff": {"anyOf": [{"type": "integer"}, {"type": "string"}]},
			"tls": {"$ref": "#/$defs/tls"},
			"extra": {"type": "object", "patternProperties": {"^x-": {"type": "string"}}},
			"note": {"type": ["string", "null"], "format": "email"}}}}`, "")
	in := only1(t, m)
	want := []string{
		`backoff | see schema | optional |  | {"anyOf":[{"type":"integer"},{"type":"string"}]}`,
		"database | object | required | ",
		"database.host | string | required | length: at least 1 character (Unicode code points)",
		"database.mode | string | optional | ",
		"database.port | integer | optional | range: between 1 and 65535",
		`extra | see schema | optional |  | {"patternProperties":{"^x-":{"type":"string"}},"type":"object"}`,
		`labels | see schema | optional |  | {"additionalProperties":{"type":"string"},"type":"object"}`,
		"note | string or null | optional | format: format `email`",
		"replicas | list of objects | optional | itemCount: at most 3 items",
		"replicas[].name | string | required | pattern: matches the RE2 pattern `^[a-z]+$`",
		"replicas[].weight | number | optional | exclusiveRange: above 0",
		"tags | list of strings | optional | uniqueItems: no two items equal",
		"tags[] | string | required | length: at most 8 characters (Unicode code points)",
		`tls | see schema | optional |  | {"$ref":"#/$defs/tls"}`,
	}
	if got := fieldRows(in); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if in.Fields[4].Default == nil || string(in.Fields[4].Default) != "5432" || string(in.Fields[3].Enum[1]) != `"ro"` {
		t.Errorf("default and enum: %+v %+v", in.Fields[4], in.Fields[3])
	}
	if schemaOf(in) == nil {
		t.Error("the raw schema stays in the model")
	}

	md := string(Markdown(m))
	for _, want := range []string{
		"Fields of the value, from its JSON Schema:\n\n| Field | Type | Required | Default | Constraints | Description |",
		"| `database.port` | integer | no | `5432` | between 1 and 65535 |  |",
		"| `database.mode` | string | no |  | one of `rw` or `ro` |  |",
		"| `backoff` | see the schema below | no |  |  |  |",
		"<summary>JSON Schema of `backoff`</summary>",
		"<summary>JSON Schema of `labels`</summary>",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "<summary>JSON Schema</summary>") {
		t.Errorf("markdown shows the whole schema as well as the table:\n%s", md)
	}
	agents := string(Agents(m))
	for _, want := range []string{
		"- field `database.port`: integer, optional, default `5432`, between 1 and 65535\n",
		"- field `database.host`: string, required, at least 1 character (Unicode code points). Host name.\n",
		"- field `backoff`: see schema `{\"anyOf\":[{\"type\":\"integer\"},{\"type\":\"string\"}]}`\n",
	} {
		if !strings.Contains(agents, want) {
			t.Errorf("agents lacks %q:\n%s", want, agents)
		}
	}
}

func TestFieldTableFallbacks(t *testing.T) {
	// A schema that is not an object, or falls back at its root, is one row
	// for the whole value.
	for schema, want := range map[string]string{
		`{"type": "array", "items": {"type": "string"}, "minItems": 1}`:   " | list of strings | required | itemCount: at least 1 item",
		`{"oneOf": [{"type": "string"}, {"type": "integer"}]}`:            ` | see schema | required |  | {"oneOf":[{"type":"string"},{"type":"integer"}]}`,
		`{"type": "object", "additionalProperties": {"type": "integer"}}`: ` | see schema | required |  | {"additionalProperties":{"type":"integer"},"type":"object"}`,
	} {
		in := only1(t, build(t, `"X": {"type": "json", "description": "A value", "schema": `+schema+`}`, ""))
		if got := strings.Join(fieldRows(in), "\n"); got != want {
			t.Errorf("%s:\n got %s\nwant %s", schema, got, want)
		}
	}
	md := string(Markdown(build(t, `"X": {"type": "json", "description": "A value", "schema": {"oneOf": [{"type": "string"}]}}`, "")))
	if !strings.Contains(md, "| (the whole value) | see the schema below | yes |") || !strings.Contains(md, "<summary>JSON Schema</summary>") {
		t.Errorf("markdown:\n%s", md)
	}
}

func TestFieldTableHidesSecretDefaults(t *testing.T) {
	in := only1(t, build(t, `"X": {"type": "json", "description": "A value", "secret": true, "schema": {
		"type": "object", "properties": {"token": {"type": "string", "default": "s3cr3t"}}}}`, ""))
	if len(in.Fields) != 1 || in.Fields[0].Default != nil {
		t.Errorf("fields: %+v", in.Fields)
	}
}

func TestKeySetDocs(t *testing.T) {
	m := build(t, `"KEYS": {"type": "keySet", "description": "Keys that verify webhooks", "secret": true, "required": true,
		"encoding": "csv", "separator": ";", "minKeys": 1, "maxKeys": 2, "keyMinLength": 32}`, "")
	in := only1(t, m)
	if in.TypeLabel != "key set" || in.Rotation == nil || len(in.Rotation.Steps) != 3 {
		t.Fatalf("input: %+v", in)
	}
	var cons []string
	for _, c := range in.Constraints {
		cons = append(cons, c.Rule+": "+c.Text)
	}
	if got, want := strings.Join(cons, "; "), "keyCount: between 1 and 2 keys; keyLength: each key at least 32 characters (Unicode code points)"; got != want {
		t.Errorf("constraints %q, want %q", got, want)
	}
	if got := strings.Join(in.Errors, " "); got != "missing_required invalid_type out_of_range too_few_items too_many_items" {
		t.Errorf("errors %s", got)
	}
	if in.Wire.Separator != ";" || !strings.Contains(in.Wire.Text, "`old;new`") || !strings.Contains(in.Wire.Platform, "never the value") {
		t.Errorf("wire %+v", in.Wire)
	}
	md := string(Markdown(m))
	for _, want := range []string{
		"| Type | `keySet` (key set) |",
		"**Rotation.** ",
		"1. add the new key to the set, and roll out;\n2. switch the sender",
		"3. remove the old key from the set, and roll out.\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if agents := string(Agents(m)); !strings.Contains(agents, "- rotation: The app accepts every key in the set") || !strings.Contains(agents, "(3) remove the old key") {
		t.Errorf("agents:\n%s", agents)
	}
	idx := only1(t, build(t, `"KEYS": {"type": "keySet", "description": "Keys that verify webhooks", "secret": true, "encoding": "indexed", "minKeys": 1, "maxKeys": 2}`, ""))
	if len(idx.Sources) != 2 || idx.Sources[1].Note == "" || !strings.Contains(idx.Wire.Text, "`KEYS__0`") {
		t.Errorf("indexed key set: %+v %+v", idx.Sources, idx.Wire)
	}
}

func TestDeprecatedAgentRule(t *testing.T) {
	m := build(t, `"OLD": {"type": "bool", "description": "Old switch", "deprecated": {"message": "Use the new one", "replacedBy": "NEW"}},
		"NEW": {"type": "bool", "description": "New switch"}`, "")
	agents := string(Agents(m))
	if !strings.Contains(agents, "Do not add or use deprecated inputs") || !strings.Contains(agents, "`OLD` (use `NEW`)") {
		t.Errorf("agents:\n%s", agents)
	}
}
