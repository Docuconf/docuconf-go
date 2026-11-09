package docuconf

import (
	_ "embed"
	"fmt"
	"reflect"
	"slices"
	"sync"
)

// fragmentsJSON is spec/fragments.json: the well-known fragments
// (SPEC §4.8), exported from the meta-schema.
//
//go:embed spec/fragments.json
var fragmentsJSON []byte

// Fragments includes well-known fragments in a declaration (SPEC §4.8):
// variables a runtime, framework or library reads, such as GOMAXPROCS or
// OTEL_EXPORTER_OTLP_ENDPOINT, which the app never reads itself. Declare
// a blank field of this type, naming the fragments:
//
//	type Config struct {
//		_ docuconf.Fragments `include:"go-runtime@1,otel@1"`
//		Port int `env:"PORT" envDefault:"8080"`
//	}
//
// The exported contract lists them in includes and carries their
// variables inline, so the platform may set them; Parse checks their
// values at boot like any other, but sets no field. A variable the
// struct declares itself replaces the fragment's. A prefix does not
// apply to them: the names are the ones the library reads.
type Fragments struct{}

var fragmentsType = reflect.TypeOf(Fragments{})

var (
	fragmentsOnce sync.Once
	fragmentsDoc  map[string]any
)

// fragment returns a well-known fragment's variables, by name.
func fragment(ref string) (map[string]any, bool) {
	fragmentsOnce.Do(func() {
		doc, err := decodeJSON(fragmentsJSON)
		if err != nil {
			panic("docuconf: spec/fragments.json: " + err.Error())
		}
		fragmentsDoc = doc.(map[string]any)
	})
	f, ok := fragmentsDoc[ref].(map[string]any)
	if !ok {
		return nil, false
	}
	vars, _ := f["vars"].(map[string]any)
	return vars, true
}

// fragmentNames lists the well-known fragments, for messages.
func fragmentNames() []string {
	fragment("")
	var out []string
	for k := range fragmentsDoc {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// addIncludes reads a Fragments field's include tag.
func (d *declaration) addIncludes(f reflect.StructField, fp string) {
	refs := splitList(f.Tag.Get("include"))
	if len(refs) == 0 {
		d.problemf("%s: a docuconf.Fragments field needs an include tag naming fragments, e.g. include:\"go-runtime@1\"", fp)
	}
	for _, r := range refs {
		switch _, ok := fragment(r); {
		case !ok:
			d.problemf("%s: %q is not a well-known fragment; the fragments are %v", fp, r, fragmentNames())
		case slices.Contains(d.includes, r):
			d.problemf("%s: fragment %s is included twice", fp, r)
		default:
			d.includes = append(d.includes, r)
		}
	}
}

// resolveFragments adds the included fragments' variables that the
// struct does not declare itself.
func (d *declaration) resolveFragments() {
	declared := map[string]bool{}
	for _, v := range d.vars {
		declared[v.name] = true
	}
	for _, r := range d.includes {
		vars, _ := fragment(r)
		names := make([]string, 0, len(vars))
		for n := range vars {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			if declared[n] {
				continue
			}
			declared[n] = true
			o, _ := vars[n].(map[string]any)
			v := contractVar(n, o, func(format string, args ...any) {
				d.problemf("fragment %s: %s: %s", r, n, fmt.Sprintf(format, args...))
			})
			if v != nil {
				v.goPath = "fragment " + r
				d.fragmentVars = append(d.fragmentVars, v)
			}
		}
	}
}

// allVars is the declared variables and the included fragments'.
func (d *declaration) allVars() []*varDecl {
	return append(slices.Clone(d.vars), d.fragmentVars...)
}
