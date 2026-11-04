package lint

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/terraform"
)

func TestGoDuration(t *testing.T) {
	for _, c := range []struct{ in, enc, want string }{
		{"1m30s", "go", "1m30s"},
		{"90", "seconds", "90s"},
		{"1.5", "seconds", "1500ms"},
		{"PT5400S", "iso8601", "5400s"},
		{"PT0.25S", "iso8601", "250ms"},
		{"1.02:03:04.005", "timespan", "93784005ms"},
		{"00:00:59", "timespan", "59s"},
	} {
		got, ok := goDuration(c.in, c.enc)
		if !ok || got != c.want {
			t.Errorf("goDuration(%q, %s) = %q, %v; want %q", c.in, c.enc, got, ok, c.want)
		}
	}
	if _, ok := goDuration("90 seconds", "seconds"); ok {
		t.Error("goDuration accepted 90 seconds")
	}
}

func TestFromWire(t *testing.T) {
	v := func(raw string) terraform.Var {
		var m map[string]any
		json.Unmarshal([]byte(raw), &m)
		return terraform.Var{Raw: m, Type: m["type"].(string)}
	}
	for _, c := range []struct {
		v    terraform.Var
		in   string
		want any
	}{
		{v(`{"type":"int"}`), "8080", json.Number("8080")},
		{v(`{"type":"int"}`), "eight", "eight"}, // kept as text, so vet says it is not an integer
		{v(`{"type":"bool"}`), "true", true},
		{v(`{"type":"list","items":"int","encoding":"csv","separator":";"}`), "80;443", []any{int64(80), int64(443)}},
		{v(`{"type":"list","items":"string","encoding":"csv"}`), "", []any{}},
		{v(`{"type":"list","items":"string","encoding":"json"}`), `["a","b"]`, []any{"a", "b"}},
		{v(`{"type":"json"}`), `{"a":1}`, map[string]any{"a": float64(1)}},
		{v(`{"type":"duration","encoding":"seconds"}`), "2.5", "2500ms"},
		{v(`{"type":"string"}`), "x", "x"},
	} {
		if got := fromWire(c.v, c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("fromWire(%s, %q) = %#v, want %#v", c.v.Type, c.in, got, c.want)
		}
	}
}

func TestStripKeys(t *testing.T) {
	for in, want := range map[string]string{
		`module.a[0].module.b["x.y"].kubernetes_pod_v1.p[1]`: "module.a.module.b.kubernetes_pod_v1.p",
		`module.a["q\"]"]`:    "module.a",
		"kubernetes_pod_v1.p": "kubernetes_pod_v1.p",
	} {
		if got := stripKeys(in); got != want {
			t.Errorf("stripKeys(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestClosest(t *testing.T) {
	c := &terraform.Contract{Vars: []terraform.Var{{Name: "PORT"}, {Name: "LOG_LEVEL"}}}
	for in, want := range map[string]string{"PORTT": "PORT", "log_level": "LOG_LEVEL", "LOGLEVEL": "LOG_LEVEL", "DATABASE": ""} {
		if got := closest(c, in); got != want {
			t.Errorf("closest(%s) = %q, want %q", in, got, want)
		}
	}
}
