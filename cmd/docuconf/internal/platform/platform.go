// Package platform validates and renders what a platform supplies for a
// docuconf contract, using the CUE meta-schema's #Validate and #Render.
package platform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing/fstest"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/load"
	"cuelang.org/go/encoding/jsonschema"
	"cuelang.org/go/encoding/yaml"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/metaschema"
)

// Platform holds a CUE context and the compiled meta-schema.
type Platform struct {
	ctx  *cue.Context
	meta cue.Value
	fsys fstest.MapFS
}

// New compiles the embedded meta-schema.
func New() (*Platform, error) {
	fsys := fstest.MapFS{}
	err := fs.WalkDir(metaschema.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(metaschema.FS(), p)
		fsys[p] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		return nil, err
	}
	p := &Platform{ctx: cuecontext.New(), fsys: fsys}
	meta, err := p.build("./contract", nil)
	if err != nil {
		return nil, fmt.Errorf("meta-schema: %w", err)
	}
	p.meta = meta
	return p, nil
}

// build loads one instance from the meta-schema module, with extra files
// added under /input.
func (p *Platform) build(arg string, extra map[string][]byte) (cue.Value, error) {
	fsys := fstest.MapFS{}
	for k, v := range p.fsys {
		fsys[k] = v
	}
	for k, v := range extra {
		fsys[k] = &fstest.MapFile{Data: v}
	}
	insts := load.Instances([]string{arg}, &load.Config{FS: fsys, Dir: "/"})
	if len(insts) != 1 {
		return cue.Value{}, fmt.Errorf("expected one CUE instance, got %d", len(insts))
	}
	if err := insts[0].Err; err != nil {
		return cue.Value{}, err
	}
	v := p.ctx.BuildInstance(insts[0])
	return v, v.Err()
}

// Contract is a loaded contract document.
type Contract struct {
	Value cue.Value
	Name  string
}

// LoadContract reads a contract.cue. The file may be the contract itself,
// as SDKs emit it (contract.#Contract & {...}), or hold exactly one field
// whose value is a contract, as the spec's examples do.
func (p *Platform) LoadContract(file string) (*Contract, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	v, err := p.build("/input/"+filepath.Base(file), map[string][]byte{"input/" + filepath.Base(file): src})
	if err != nil {
		return nil, contractError(file, err)
	}
	if !isContract(v) {
		var found []cue.Value
		var names []string
		it, _ := v.Fields()
		for it != nil && it.Next() {
			if isContract(it.Value()) {
				found = append(found, it.Value())
				names = append(names, it.Selector().String())
			}
		}
		switch len(found) {
		case 0:
			return nil, fmt.Errorf("%s holds no ConfigContract", file)
		case 1:
			v = found[0]
		default:
			return nil, fmt.Errorf("%s holds several contracts (%s); keep one per file", file, strings.Join(names, ", "))
		}
	}
	if err := v.Validate(cue.Concrete(true)); err != nil {
		return nil, contractError(file, err)
	}
	// A contract is plain data (SPEC §4). Rebuilding it from JSON detaches
	// it from the copy of the meta-schema its file imported, so it unifies
	// cleanly with this platform's #Validate and #Render.
	data, err := v.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("%s: %s", file, errorLines(err))
	}
	v = p.ctx.CompileBytes(data, cue.Filename(file))
	name, _ := v.LookupPath(cue.ParsePath("metadata.name")).String()
	return &Contract{Value: v, Name: name}, nil
}

func isContract(v cue.Value) bool {
	k, err := v.LookupPath(cue.ParsePath("kind")).String()
	return err == nil && k == "ConfigContract"
}

// LoadData reads a values, files or policy document: YAML, JSON or CUE.
// An empty path yields an empty struct.
func (p *Platform) LoadData(file string) (cue.Value, error) {
	if file == "" {
		return p.ctx.CompileString("{}"), nil
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return cue.Value{}, err
	}
	var v cue.Value
	switch strings.ToLower(path.Ext(file)) {
	case ".yaml", ".yml":
		f, err := yaml.Extract(file, src)
		if err != nil {
			return cue.Value{}, err
		}
		v = p.ctx.BuildFile(f)
	default: // JSON is CUE
		v = p.ctx.CompileBytes(src, cue.Filename(file))
	}
	if err := v.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("%s: %s", file, errorLines(err))
	}
	if v.IncompleteKind() != cue.StructKind {
		return cue.Value{}, fmt.Errorf("%s: expected a mapping of names to values", file)
	}
	return v, nil
}

// Schemas compiles every JSON Schema in the contract (json variables and
// config files) to CUE, keyed by variable or input name.
func (p *Platform) Schemas(c *Contract) (map[string]cue.Value, error) {
	out := map[string]cue.Value{}
	for _, section := range []string{"vars", "files"} {
		it, _ := c.Value.LookupPath(cue.ParsePath(section)).Fields()
		for it != nil && it.Next() {
			s := it.Value().LookupPath(cue.ParsePath("schema"))
			if !s.Exists() {
				continue
			}
			name := selName(it.Selector())
			f, err := jsonschema.Extract(s, &jsonschema.Config{})
			if err != nil {
				return nil, fmt.Errorf("%s: schema: %s", name, errorLines(err))
			}
			v := p.ctx.BuildFile(f)
			if err := v.Err(); err != nil {
				return nil, fmt.Errorf("%s: schema: %s", name, errorLines(err))
			}
			out[name] = v
		}
	}
	return out, nil
}

// Validate runs #Validate, and the policy against the values, and returns
// one readable line per problem. Secret values never appear in the lines.
func (p *Platform) Validate(c *Contract, values, files, overlays, policy cue.Value) ([]string, error) {
	schemas, err := p.Schemas(c)
	if err != nil {
		return nil, err
	}
	t := newTranslator(c, values, files, overlays)
	// An undeclared name makes CUE reject the whole values struct, which
	// would hide every other problem, so unknown names are reported here
	// and left out of #Validate.
	values = t.declaredOnly(p.ctx, values, t.vars, "is not declared in the contract (check the spelling)")
	files = t.declaredOnly(p.ctx, files, t.fileDefs, "is not a file input declared in the contract")
	overlays = t.declaredOverlays(p.ctx)

	// Policy applies to a value however it is supplied: env or overlay.
	if policy.Exists() {
		all := values
		for _, m := range fields(overlays) {
			all = all.Unify(m)
		}
		if err := all.Unify(policy).Validate(cue.Concrete(true)); err != nil {
			t.policy(err)
		}
	}
	v := p.meta.LookupPath(cue.ParsePath("#Validate")).
		FillPath(cue.ParsePath("contract"), c.Value).
		FillPath(cue.ParsePath("values"), values).
		FillPath(cue.ParsePath("files"), files).
		FillPath(cue.ParsePath("overlays"), overlays)
	for name, s := range schemas {
		v = v.FillPath(cue.MakePath(cue.Def("#schemas"), cue.Str(name)), s)
	}
	if err := v.Validate(cue.Concrete(true)); err != nil {
		t.validate(err)
	}
	return t.lines(), nil
}

// Render runs #Render and returns its output as YAML.
func (p *Platform) Render(c *Contract, values, files, overlays cue.Value) ([]byte, error) {
	r := p.meta.LookupPath(cue.ParsePath("#Render")).
		FillPath(cue.ParsePath("contract"), c.Value).
		FillPath(cue.ParsePath("values"), values).
		FillPath(cue.ParsePath("files"), files).
		FillPath(cue.ParsePath("overlays"), overlays)
	// Each section is encoded separately to keep this order in the output.
	var buf bytes.Buffer
	for _, f := range []string{"env", "volumes", "volumeMounts", "configMaps", "restartTriggers"} {
		v := r.LookupPath(cue.ParsePath(f))
		if err := v.Validate(cue.Concrete(true)); err != nil {
			return nil, fmt.Errorf("render %s: %s", f, errorLines(err))
		}
		b, err := yaml.Encode(v)
		if err != nil {
			return nil, err
		}
		if n, _ := v.Len().Int64(); n == 0 {
			fmt.Fprintf(&buf, "%s: []\n", f)
			continue
		}
		fmt.Fprintf(&buf, "%s:\n", f)
		for _, line := range strings.SplitAfter(strings.TrimSuffix(string(b), "\n"), "\n") {
			if strings.TrimSpace(line) == "" {
				buf.WriteString(line)
				continue
			}
			buf.WriteString("  " + line)
		}
		buf.WriteString("\n")
	}
	return buf.Bytes(), nil
}

// HelmValuesSchema runs #HelmValuesSchema and returns a values.schema.json
// for a chart that renders the contract with the docuconf library chart.
func (p *Platform) HelmValuesSchema(c *Contract) ([]byte, error) {
	v := p.meta.LookupPath(cue.ParsePath("#HelmValuesSchema")).
		FillPath(cue.ParsePath("contract"), c.Value).
		LookupPath(cue.ParsePath("out"))
	if err := v.Validate(cue.Concrete(true)); err != nil {
		return nil, fmt.Errorf("helm values schema: %s", errorLines(err))
	}
	return indentJSON(v)
}

// ContractJSON returns the contract as indented JSON, the form the
// docuconf library chart reads from files/docuconf/contract.json.
func (p *Platform) ContractJSON(c *Contract) ([]byte, error) {
	return indentJSON(c.Value)
}

func indentJSON(v cue.Value) ([]byte, error) {
	raw, err := v.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("%s", errorLines(err))
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// contractError explains why a contract does not unify with #Contract.
// A variable is a disjunction of one definition per type, so CUE reports
// a conflict with every other type; those lines are dropped, leaving the
// problem with the variable's own type.
func contractError(file string, err error) error {
	var keep []string
	all := strings.Split(errorLines(err), "\n")
	for _, l := range all {
		if strings.Contains(l, "errors in empty disjunction") ||
			(strings.Contains(l, ".type: conflicting values") && !strings.Contains(l, "incomplete")) {
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) == 0 {
		keep = all
	}
	return fmt.Errorf("%s is not a valid contract:\n  %s", file, strings.Join(keep, "\n  "))
}

// errorLines formats CUE errors one per line, without positions.
func errorLines(err error) string {
	var lines []string
	seen := map[string]bool{}
	for _, e := range errors.Errors(err) {
		l := e.Error()
		if !seen[l] {
			seen[l] = true
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// selName returns a field selector as a plain name, without quotes.
func selName(s cue.Selector) string {
	return unquote(s.String())
}

func unquote(s string) string {
	if strings.HasPrefix(s, `"`) {
		var u string
		if json.Unmarshal([]byte(s), &u) == nil {
			return u
		}
	}
	return s
}

// jsonOf encodes a concrete value as compact JSON, for messages.
func jsonOf(v cue.Value) string {
	b, err := v.MarshalJSON()
	if err != nil {
		return fmt.Sprint(v)
	}
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return string(b)
	}
	return buf.String()
}
