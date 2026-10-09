package docs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Build turns a contract, as JSON unified with the meta-schema (so that
// defaults such as required: false and a list's encoding are explicit),
// into the docs model. The contract is assumed valid: the CLI checks it
// against #Contract first.
func Build(contractJSON []byte) (*Model, error) {
	dec := json.NewDecoder(bytes.NewReader(contractJSON))
	dec.UseNumber()
	var c struct {
		Metadata struct {
			Name       string    `json:"name"`
			AppVersion string    `json:"appVersion"`
			Generator  Generator `json:"generator"`
		} `json:"metadata"`
		Vars     map[string]fields `json:"vars"`
		Files    map[string]fields `json:"files"`
		Overlays map[string]fields `json:"overlays"`
		Profiles *struct {
			Selector string                    `json:"selector"`
			Default  string                    `json:"default"`
			Defaults map[string]map[string]any `json:"defaults"`
		} `json:"profiles"`
	}
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	m := &Model{
		APIVersion: APIVersion,
		Kind:       Kind,
		Service:    Service{Name: c.Metadata.Name, AppVersion: c.Metadata.AppVersion, Generator: c.Metadata.Generator},
		Groups:     []Group{},
		Errors:     []ErrorInfo{},
	}
	for _, name := range sortedKeys(c.Overlays) {
		o := c.Overlays[name]
		m.Overlays = append(m.Overlays, Overlay{Name: name, Format: o.str("format"), Path: o.str("path"),
			KeySeparator: o.str("keySeparator"), Reload: o.str("reload")})
	}
	selector := ""
	if p := c.Profiles; p != nil {
		selector = p.Selector
		m.Profiles = &Profiles{Selector: p.Selector, Default: p.Default, Names: sortedKeys(p.Defaults)}
	}

	var inputs []Input
	for _, name := range sortedKeys(c.Vars) {
		f := c.Vars[name]
		in, err := buildVar(name, f, m.Overlays, name == selector)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if c.Profiles != nil && !in.Secret {
			for _, p := range m.Profiles.Names {
				if v, ok := c.Profiles.Defaults[p][name]; ok {
					in.ProfileDefaults = append(in.ProfileDefaults, ProfileDefault{Profile: p, Value: raw(v)})
				}
			}
		}
		inputs = append(inputs, in)
	}
	for _, name := range sortedKeys(c.Files) {
		inputs = append(inputs, buildFile(name, c.Files[name]))
	}

	// Groups: ungrouped first, then by name. Inputs keep their order,
	// variables before files, each by name.
	byGroup := map[string][]Input{}
	for _, in := range inputs {
		byGroup[in.Group] = append(byGroup[in.Group], in)
	}
	used := codes{}
	for _, g := range sortedKeys(byGroup) {
		title := g
		if g == "" {
			title = "General"
		}
		m.Groups = append(m.Groups, Group{Name: g, Title: title, Inputs: byGroup[g]})
		for _, in := range byGroup[g] {
			for _, e := range in.Errors {
				used[e] = true
			}
		}
	}
	for _, e := range errorCatalog {
		if used[e.Code] {
			m.Errors = append(m.Errors, e)
		}
	}
	return m, nil
}

func common(name, kind, typ string, f fields) Input {
	in := Input{
		Name:        name,
		Kind:        kind,
		Type:        typ,
		Group:       f.str("group"),
		Required:    f["required"] == true,
		Secret:      f["secret"] == true,
		Description: f.str("description"),
		Details:     strings.TrimSpace(f.str("details")),
		Constraints: constraints(kind, typ, f),
	}
	if s, ok := f["schema"]; ok {
		in.Fields = schemaFields(s, f["secret"] == true)
	}
	if d, ok := f["deprecated"].(map[string]any); ok {
		msg, _ := d["message"].(string)
		by, _ := d["replacedBy"].(string)
		in.Deprecated = &Deprecated{Message: msg, ReplacedBy: by}
	}
	return in
}

func buildVar(name string, f fields, overlays []Overlay, selector bool) (Input, error) {
	typ := f.str("type")
	in := common(name, KindVar, typ, f)
	in.TypeLabel = varTypeLabel(typ, f)
	in.ConfigKey = f.str("configKey")
	in.ProfileSelector = selector
	in.Wire = wire(name, typ, f)
	if typ == "keySet" {
		in.Rotation = keySetRotation
	}
	in.Sources = varSources(typ, f, overlays, selector)
	in.Errors = varErrors(typ, f)
	// A secret never has a value in the docs (SPEC §6), even if a
	// hand-written contract tried.
	if !in.Secret {
		in.Examples = f.strs("examples")
		if def, ok := f["default"]; ok {
			in.Default = raw(def)
			env, err := envValue(name, typ, f, def)
			if err != nil {
				return in, fmt.Errorf("default: %w", err)
			}
			in.DefaultEnv = env
		}
	}
	return in, nil
}

func buildFile(name string, f fields) Input {
	typ := f.str("type")
	in := common(name, KindFile, typ, f)
	in.TypeLabel = fileTypeLabel(typ, f)
	reload := f.str("reload")
	if reload == "" {
		reload = "restart"
	}
	in.File = &FileInfo{
		Path:       f.str("path"),
		PathEnv:    f.str("pathEnv"),
		Reload:     reload,
		Contents:   fileContents(typ, f),
		ReloadText: reloadText(reload),
	}
	if typ == "config" || typ == "keystore" {
		in.File.Format = f.str("format")
	}
	if n, ok := f["maxSize"].(json.Number); ok {
		in.File.MaxSize, _ = n.Int64()
	}
	in.Sources = fileSources(typ, in.Secret)
	in.Errors = fileErrors(typ, f)
	return in
}

// envValue writes a typed value as the process environment holds it, as
// #Render does, without Kubernetes' $ escaping.
func envValue(name, typ string, f fields, v any) ([]EnvEntry, error) {
	one := func(s string) []EnvEntry { return []EnvEntry{{Name: name, Value: s}} }
	switch typ {
	case "json":
		return one(string(raw(v))), nil
	case "duration":
		s, err := renderDuration(scalar(v), f.str("encoding"))
		return one(s), err
	case "list":
		xs, _ := v.([]any)
		items := make([]string, len(xs))
		for i, x := range xs {
			items[i] = scalar(x)
		}
		switch f.str("encoding") {
		case "json":
			return one(string(raw(v))), nil
		case "indexed":
			out := make([]EnvEntry, len(items))
			for i, x := range items {
				out[i] = EnvEntry{Name: fmt.Sprintf("%s__%d", name, i), Value: x}
			}
			return out, nil
		}
		return one(strings.Join(items, f.str("separator"))), nil
	case "bool":
		return one(fmt.Sprint(v)), nil
	}
	return one(scalar(v)), nil
}

// renderDuration converts a Go-syntax duration to an encoding, as
// #RenderDuration does.
func renderDuration(s, encoding string) (string, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return "", err
	}
	if encoding == "go" || encoding == "" {
		return s, nil
	}
	ms := int64(d / time.Millisecond)
	secs, frac := ms/1000, ms%1000
	fracStr := ""
	if frac != 0 {
		fracStr = "." + strings.TrimRight(fmt.Sprintf("%03d", frac), "0")
	}
	switch encoding {
	case "seconds":
		return fmt.Sprintf("%d%s", secs, fracStr), nil
	case "iso8601":
		return fmt.Sprintf("PT%d%sS", secs, fracStr), nil
	case "timespan":
		days := ""
		if secs >= 86400 {
			days = fmt.Sprintf("%d.", secs/86400)
		}
		return fmt.Sprintf("%s%02d:%02d:%02d%s", days, secs%86400/3600, secs%3600/60, secs%60, fracStr), nil
	}
	return "", fmt.Errorf("unknown duration encoding %q", encoding)
}

// raw encodes a decoded JSON value compactly, without HTML escaping.
// Object keys come out sorted, so the result is deterministic.
func raw(v any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return json.RawMessage("null")
	}
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
