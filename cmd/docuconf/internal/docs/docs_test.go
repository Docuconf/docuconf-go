package docs

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// contract wraps vars and files in a contract document, as the CLI hands
// it to Build: JSON, unified with the meta-schema's defaults.
func contract(t *testing.T, vars, files string) []byte {
	t.Helper()
	doc := `{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "svc", "generator": {"language": "go", "sdk": "docuconf-go", "version": "0.1.0"}},
		"vars": {` + vars + `}, "files": {` + files + `}}`
	if !json.Valid([]byte(doc)) {
		t.Fatalf("test contract is not JSON:\n%s", doc)
	}
	return []byte(doc)
}

func build(t *testing.T, vars, files string) *Model {
	t.Helper()
	m, err := Build(contract(t, vars, files))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func only1(t *testing.T, m *Model) Input {
	t.Helper()
	if len(m.Groups) != 1 || len(m.Groups[0].Inputs) != 1 {
		t.Fatalf("want one input, got %+v", m.Groups)
	}
	return m.Groups[0].Inputs[0]
}

// TestConstraintPhrases covers every rule in the phrasing table, for
// every type it applies to.
func TestConstraintPhrases(t *testing.T) {
	v := func(fields string) string {
		return `"X": {"name": "X", "description": "An input", "required": false, "secret": false, ` + fields + `}`
	}
	f := func(fields string) string {
		return `"x": {"name": "x", "description": "An input", "required": false, "secret": false, "path": "/etc/x/x", "reload": "restart", ` + fields + `}`
	}
	for _, c := range []struct {
		name, vars, files string
		want              []string // rule: text
	}{
		{"int range", v(`"type": "int", "min": 1, "max": 65535`), "", []string{"range: between 1 and 65535"}},
		{"int min", v(`"type": "int", "min": 1`), "", []string{"range: at least 1"}},
		{"int max", v(`"type": "int", "max": -1`), "", []string{"range: at most -1"}},
		{"int exact", v(`"type": "int", "min": 3, "max": 3`), "", []string{"range: exactly 3"}},
		{"float range", v(`"type": "float", "min": 0, "max": 0.5`), "", []string{"range: between 0 and 0.5"}},
		{"duration range", v(`"type": "duration", "encoding": "go", "min": "1s", "max": "5m"`), "", []string{"range: between 1s and 5m"}},
		{"string length", v(`"type": "string", "minLength": 1, "maxLength": 120`), "", []string{"length: between 1 and 120 characters (Unicode code points)"}},
		{"string max length", v(`"type": "string", "maxLength": 120`), "", []string{"length: at most 120 characters (Unicode code points)"}},
		{"string min length 1", v(`"type": "string", "minLength": 1`), "", []string{"length: at least 1 character (Unicode code points)"}},
		{"anchored pattern", v(`"type": "string", "pattern": "^[a-z]+$"`), "", []string{"pattern: matches the RE2 pattern `^[a-z]+$`"}},
		{"unanchored pattern", v(`"type": "string", "pattern": "[a-z]"`), "", []string{"pattern: contains a match for the RE2 pattern `[a-z]`"}},
		{"pattern with a backtick and a pipe", v("\"type\": \"string\", \"pattern\": \"a`b|c\""), "", []string{"pattern: contains a match for the RE2 pattern ``a`b|c``"}},
		{"one scheme", v(`"type": "url", "schemes": ["https"]`), "", []string{"schemes: `https` URL"}},
		{"two schemes", v(`"type": "url", "schemes": ["https", "http"]`), "", []string{"schemes: `https` or `http` URL"}},
		{"url without schemes", v(`"type": "url"`), "", nil},
		{"enum", v(`"type": "enum", "values": ["debug", "info", "warn"]`), "", []string{"values: one of `debug`, `info` or `warn`"}},
		{"bool", v(`"type": "bool"`), "", nil},
		{"list items", v(`"type": "list", "items": "string", "encoding": "csv", "separator": ",", "minItems": 1, "maxItems": 5`), "", []string{"itemCount: between 1 and 5 items"}},
		{"list one item", v(`"type": "list", "items": "string", "encoding": "json", "minItems": 1`), "", []string{"itemCount: at least 1 item"}},
		{"int list item range", v(`"type": "list", "items": "int", "encoding": "indexed", "itemMin": 0, "itemMax": 1023`), "", []string{"itemRange: each item between 0 and 1023"}},
		{"json schema", v(`"type": "json", "schema": {"type": "object"}`), "", []string{"schema: matches the JSON Schema in the contract"}},
		{"config file", "", f(`"type": "config", "format": "yaml", "maxSize": 65536, "schema": {"type": "object"}`),
			[]string{"schema: matches the JSON Schema in the contract", "maxSize: at most 64 KiB (65536 bytes)"}},
		{"odd size", "", f(`"type": "binary", "maxSize": 1000`), []string{"maxSize: at most 1000 bytes"}},
		{"big size", "", f(`"type": "binary", "maxSize": 134217728`), []string{"maxSize: at most 128 MiB (134217728 bytes)"}},
		{"tls", "", f(`"type": "tls", "secret": true, "dnsNames": ["a.example.com", "b.example.com"], "keyAlgorithms": ["ECDSA", "RSA"], "minRemaining": "720h", "requireCA": true`),
			[]string{"dnsNames: the certificate covers `a.example.com` and `b.example.com`", "keyAlgorithms: key algorithm `ECDSA` or `RSA`",
				"minRemaining: at least 720h (30 days) of validity left", "requireCA: includes `ca.crt`, and the certificate chains to it"}},
		{"tls without CA", "", f(`"type": "tls", "secret": true, "requireCA": false, "keyAlgorithms": []`), nil},
		{"ca bundle", "", f(`"type": "caBundle", "minCertificates": 1`), []string{"minCertificates: at least 1 CA certificate"}},
		{"ca bundle of two", "", f(`"type": "caBundle", "minCertificates": 2`), []string{"minCertificates: at least 2 CA certificates"}},
		{"keystore", "", f(`"type": "keystore", "secret": true, "format": "pkcs12", "passwordVar": "KS_PASSWORD"`),
			[]string{"passwordVar: opens with the password in `KS_PASSWORD`"}},
		{"text file", "", f(`"type": "text", "pattern": "^[A-Z]+\\n?$", "minLength": 5, "maxLength": 5`),
			[]string{"length: exactly 5 characters (Unicode code points)", "pattern: matches the RE2 pattern `^[A-Z]+\\n?$`"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := only1(t, build(t, c.vars, c.files))
			var got []string
			for _, k := range in.Constraints {
				got = append(got, k.Rule+": "+k.Text)
				if len(k.Params) == 0 {
					t.Errorf("%s has no params", k.Rule)
				}
			}
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("constraints:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestConstraintParams(t *testing.T) {
	in := only1(t, build(t, `"PORT": {"type": "int", "description": "Listen port", "min": 1, "max": 65535}`, ""))
	got, _ := json.Marshal(in.Constraints[0])
	if want := `{"rule":"range","params":{"max":65535,"min":1},"text":"between 1 and 65535"}`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestWire(t *testing.T) {
	for _, c := range []struct{ vars, text, env string }{
		{`"type": "int", "default": 8080`, "a base-10 integer with no leading `+` or zeros, such as `8080`", "X=8080"},
		{`"type": "duration", "encoding": "go", "default": "1m30s"`, "a Go duration, such as `1m30s` (units `h`, `m`, `s`, `ms`, `us`, `ns`)", "X=1m30s"},
		{`"type": "duration", "encoding": "iso8601", "default": "1m30s"`, "an ISO 8601 duration, such as `PT90S` for 90 seconds", "X=PT90S"},
		{`"type": "duration", "encoding": "seconds", "default": "1500ms"`, "a number of seconds, such as `90` or `1.5`", "X=1.5"},
		{`"type": "duration", "encoding": "timespan", "default": "26h1m30s"`, "a .NET TimeSpan, `[d.]hh:mm:ss[.fff]`, such as `00:01:30` for 90 seconds", "X=1.02:01:30"},
		{`"type": "list", "items": "string", "encoding": "csv", "separator": ";", "default": ["a", "b"]`, "the items joined by `;` with nothing around it, such as `a;b`", "X=a;b"},
		{`"type": "list", "items": "int", "encoding": "json", "default": [1, 2]`, "a compact JSON array, such as `[1,2]`", "X=[1,2]"},
		{`"type": "list", "items": "string", "encoding": "indexed", "default": ["a", "b"]`, "one variable per item, `X__0`, `X__1` and so on, numbered from 0 with no gaps", "X__0=a X__1=b"},
		{`"type": "json", "default": {"b": 1, "a": "<&>"}`, "compact JSON on one line", `X={"a":"<&>","b":1}`},
		{`"type": "bool", "default": false`, "`true` or `false`", "X=false"},
	} {
		in := only1(t, build(t, `"X": {"description": "An input", `+c.vars+`}`, ""))
		var env []string
		for _, e := range in.DefaultEnv {
			env = append(env, e.Name+"="+e.Value)
		}
		if in.Wire.Text != c.text || strings.Join(env, " ") != c.env {
			t.Errorf("%s:\n got %q, %q\nwant %q, %q", c.vars, in.Wire.Text, env, c.text, c.env)
		}
	}
}

// TestSecrets checks that no value of a secret reaches the model or the
// rendered docs, even from a contract that (wrongly) holds one.
func TestSecrets(t *testing.T) {
	m := build(t, `"TOKEN": {"type": "string", "description": "API token", "secret": true, "required": false,
		"default": "hunter2-default", "examples": ["hunter2-example"]}`,
		`"key": {"type": "text", "description": "Signing key", "secret": true, "path": "/etc/key/key.pem", "reload": "restart"}`)
	model, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range [][]byte{model, Markdown(m), Agents(m)} {
		if bytes.Contains(out, []byte("hunter2")) {
			t.Errorf("a secret's value leaked:\n%s", out)
		}
	}
	tok := m.Groups[0].Inputs[0]
	if got := tok.Wire.Platform; got != "a `secretKeyRef` or `injected` reference, never the value" {
		t.Errorf("platform text %q", got)
	}
	if kinds := sourceList(tok.Sources); kinds != "`secretKeyRef`, `injected`" {
		t.Errorf("secret variable sources %s", kinds)
	}
	key := m.Groups[0].Inputs[1]
	if kinds := sourceList(key.Sources); kinds != "`secret`, `csi`, `injected` (the injector writes the file at the path)" {
		t.Errorf("secret file sources %s", kinds)
	}
	agents := string(Agents(m))
	if !strings.Contains(agents, "Secret inputs: `TOKEN` and `key`.") {
		t.Errorf("hard rules do not list the secrets:\n%s", agents)
	}
}

func TestSources(t *testing.T) {
	m, err := Build([]byte(`{"metadata": {"name": "svc", "generator": {"language": "dotnet", "sdk": "x", "version": "1"}},
		"overlays": {"platform": {"format": "json", "path": "/app/config/a.json", "keySeparator": ":", "reload": "watch"}},
		"profiles": {"selector": "ENV", "default": "Production", "defaults": {"Staging": {"PAGE": 10}, "Production": {"PAGE": 20}}},
		"vars": {
			"ENV": {"type": "string", "description": "Hosting environment", "configKey": "Env"},
			"PAGE": {"type": "int", "description": "Page size", "configKey": "Catalog:Page"},
			"IDS": {"type": "list", "description": "Some ids", "items": "int", "encoding": "indexed"}},
		"files": {"tls": {"type": "tls", "description": "Serving cert", "secret": true, "path": "/etc/tls", "reload": "watch"},
			"geo": {"type": "binary", "description": "GeoIP data", "path": "/data/geo.mmdb", "reload": "restart"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, in := range m.Groups[0].Inputs {
		got[in.Name] = sourceList(in.Sources)
	}
	want := map[string]string{
		"ENV":  "`literal`, `configMapKeyRef`, `fieldRef`, `injected`",
		"PAGE": "`literal`, `configMapKeyRef`, `resourceFieldRef`, `injected`, `overlay` (the `platform` overlay, at key `Catalog:Page`)",
		"IDS":  "`literal`, `injected` (without a `ref`: one reference cannot carry an indexed list)",
		"tls":  "`secret` (the whole `kubernetes.io/tls` Secret, with no key), `certificate`, `csi`, `injected` (the injector writes the file at the path)",
		"geo":  "`configMap`, `secret`, `csi`, `image`, `injected` (the injector writes the file at the path)",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s sources:\n got %s\nwant %s", k, got[k], w)
		}
	}
	page := m.Groups[0].Inputs[2]
	if page.Name != "PAGE" {
		t.Fatalf("inputs out of order: %s", page.Name)
	}
	pd, _ := json.Marshal(page.ProfileDefaults)
	if string(pd) != `[{"profile":"Production","value":20},{"profile":"Staging","value":10}]` {
		t.Errorf("profile defaults %s", pd)
	}
	if !m.Groups[0].Inputs[0].ProfileSelector {
		t.Error("ENV is the profile selector")
	}
}

func TestErrors(t *testing.T) {
	m := build(t, `"A": {"type": "string", "description": "Plain text"},
		"B": {"type": "enum", "description": "An enum", "values": ["x"], "required": true},
		"C": {"type": "list", "description": "Some ints", "items": "int", "encoding": "csv", "separator": ",", "maxItems": 2}`,
		`"t": {"type": "tls", "description": "A key pair", "secret": true, "path": "/etc/t", "reload": "restart", "dnsNames": ["a"]}`)
	got := map[string][]string{}
	for _, in := range m.Groups[0].Inputs {
		got[in.Name] = in.Errors
	}
	want := map[string]string{
		"A": "",
		"B": "missing_required not_in_enum",
		"C": "invalid_type out_of_range too_many_items",
		"t": "file_unreadable file_malformed certificate_invalid certificate_name_mismatch key_mismatch",
	}
	for k, w := range want {
		if strings.Join(got[k], " ") != w {
			t.Errorf("%s: got %v, want %s", k, got[k], w)
		}
	}
	var codes []string
	for _, e := range m.Errors {
		codes = append(codes, e.Code)
	}
	if strings.Join(codes, " ") != "missing_required invalid_type out_of_range not_in_enum too_many_items file_unreadable file_malformed certificate_invalid certificate_name_mismatch key_mismatch" {
		t.Errorf("model errors %v", codes)
	}
}

func TestGroups(t *testing.T) {
	m := build(t, `"Z": {"type": "bool", "description": "No group"},
		"E": {"type": "bool", "description": "Empty group", "group": ""},
		"B": {"type": "bool", "description": "In http", "group": "http"},
		"A": {"type": "bool", "description": "In database", "group": "database"}`,
		`"f": {"type": "binary", "description": "A file", "path": "/data/f", "reload": "restart", "group": "http"}`)
	var got []string
	for _, g := range m.Groups {
		var names []string
		for _, in := range g.Inputs {
			names = append(names, in.Name)
		}
		got = append(got, g.Title+"="+strings.Join(names, ","))
	}
	if want := "General=E,Z database=A http=B,f"; strings.Join(got, " ") != want {
		t.Errorf("groups %s, want %s", got, want)
	}
}

func TestDemote(t *testing.T) {
	in := "Intro.\n\n# Top\n\n## Second ##\n\nSetext one\n==========\n\nSetext two\n---\n\n---\n\n- item\n---\n\n```sh\n# not a heading\n```\n\n~~~\n## nor this\n~~~\n\n    # indented code\n####### seven is not a heading"
	got, heads := demote(in, 4)
	want := "Intro.\n\n##### Top\n\n###### Second\n\n##### Setext one\n\n###### Setext two\n\n---\n\n- item\n---\n\n```sh\n# not a heading\n```\n\n~~~\n## nor this\n~~~\n\n    # indented code\n####### seven is not a heading"
	if got != want {
		t.Errorf("demote:\n%s\nwant:\n%s", got, want)
	}
	if strings.Join(heads, "|") != "Top|Second|Setext one|Setext two" {
		t.Errorf("headings %q", heads)
	}
}

func TestDetailsRendering(t *testing.T) {
	m := build(t, `"URL": {"type": "url", "description": "CMS base URL", "details": "Why it exists.\n\n# When to change it\n\nIn staging."}`, "")
	md := string(Markdown(m))
	if !strings.Contains(md, "### `URL`\n\nCMS base URL\n") || !strings.Contains(md, "\n\nWhy it exists.\n\n#### When to change it\n\nIn staging.\n") {
		t.Errorf("markdown details:\n%s", md)
	}
	agents := string(Agents(m))
	if !strings.Contains(agents, "#### URL\n") || !strings.Contains(agents, "CMS base URL\n\nWhy it exists.\n\n##### When to change it\n\nIn staging.\n") {
		t.Errorf("agents details:\n%s", agents)
	}
}

func TestMarkdownEscaping(t *testing.T) {
	m := build(t, `"A_B": {"type": "string", "description": "Uses *stars*, _under_ and <tags> | pipes", "default": "a|b",
		"examples": ["short", "`+strings.Repeat("long ", 20)+`", "two\nlines"]}`, "")
	md := string(Markdown(m))
	for _, want := range []string{
		`Uses \*stars\*, \_under\_ and \<tags\> \| pipes`,
		"| Default | `a\\|b` |",
		"Examples:\n\n- `short`\n\n```text\n" + strings.Repeat("long ", 20) + "\n```\n\n```text\ntwo\nlines\n```\n",
		"[`A_B`](#a_b)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if agents := string(Agents(m)); !strings.Contains(agents, "- example: `\"two\\nlines\"`\n") {
		t.Errorf("agents example:\n%s", agents)
	}
}

func TestDeprecatedLink(t *testing.T) {
	m := build(t, `"OLD": {"type": "bool", "description": "Old switch", "deprecated": {"message": "Use the new one", "replacedBy": "NEW"}},
		"NEW": {"type": "bool", "description": "New switch"}`, "")
	md := string(Markdown(m))
	if !strings.Contains(md, "> **Deprecated.** Use the new one. Use [`NEW`](#new) instead.") {
		t.Errorf("deprecation:\n%s", md)
	}
}

func TestCode(t *testing.T) {
	for in, want := range map[string]string{"a": "`a`", "a`b": "``a`b``", "`a": "`` `a ``", "": "`\"\"`", " a ": "`  a  `"} {
		if got := code(in); got != want {
			t.Errorf("code(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugger(t *testing.T) {
	s := slugger{}
	var got []string
	for _, h := range []string{"`PORT`", "General", "General", "Größe & Maß", "general-1"} {
		got = append(got, s.slug(h))
	}
	if want := "port general general-1 größe--maß general-1-1"; strings.Join(got, " ") != want {
		t.Errorf("slugs %q, want %s", got, want)
	}
}

// TestDeterminism builds and renders the same contract repeatedly: the
// output must not depend on map order.
func TestDeterminism(t *testing.T) {
	c := contract(t, `"A": {"type": "json", "description": "A value", "default": {"z": 1, "a": [3, 2], "m": {"y": 1, "b": 2}}, "group": "g2"},
		"B": {"type": "int", "description": "B value", "min": 1, "max": 2, "group": "g1"},
		"C": {"type": "list", "description": "C value", "items": "string", "encoding": "csv", "separator": ",", "minItems": 1, "maxItems": 3}`,
		`"f": {"type": "config", "format": "json", "description": "A file", "path": "/etc/f/f.json", "reload": "restart",
			"schema": {"type": "object", "properties": {"z": {"type": "string"}, "a": {"type": "integer"}}}}`)
	var first [3][]byte
	for i := range 20 {
		m, err := Build(c)
		if err != nil {
			t.Fatal(err)
		}
		model, err := Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		outs := [3][]byte{model, Markdown(m), Agents(m)}
		if i == 0 {
			first = outs
			continue
		}
		for k := range outs {
			if !bytes.Equal(outs[k], first[k]) {
				t.Fatalf("output %d differs between runs", k)
			}
		}
	}
}

// TestModelRoundTrip checks that the renderers need nothing but the
// model: rendering a decoded docs.json gives the same bytes.
func TestModelRoundTrip(t *testing.T) {
	m := build(t, `"A": {"type": "json", "description": "A value", "default": {"b": 1, "a": 2}},
		"B": {"type": "duration", "description": "B value", "encoding": "timespan", "default": "90s", "details": "# Why\n\nBecause."}`,
		`"f": {"type": "text", "description": "A file", "path": "/etc/f/f", "reload": "watch", "secret": true, "required": true}`)
	model, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(model)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Encode(back)
	if !bytes.Equal(model, again) {
		t.Errorf("model changed in a round trip:\n%s\n%s", model, again)
	}
	if !bytes.Equal(Markdown(m), Markdown(back)) || !bytes.Equal(Agents(m), Agents(back)) {
		t.Error("rendering a decoded model differs")
	}
	if !IsModel(model) || IsModel(contract(t, "", "")) {
		t.Error("IsModel")
	}
	if _, err := Decode([]byte(`{"apiVersion": "docs.docuconf.dev/v1alpha1", "kind": "ConfigDocs", "extra": 1}`)); err == nil {
		t.Error("Decode accepted an unknown field")
	}
}
