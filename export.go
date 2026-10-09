package docuconf

import (
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"encoding/json"

	"github.com/caarlos0/env/v11"
)

// Version is the docuconf-go SDK version recorded in exported contracts.
const Version = "0.1.0" // x-release-please-version

// Meta describes the service a contract is exported for.
type Meta struct {
	// Name is the service name: a DNS label such as "billing-api".
	Name string
	// AppVersion is the version or git SHA the contract is exported from.
	AppVersion string
	// Package is the CUE package clause of the output. It defaults to
	// Name with dashes replaced by underscores.
	Package string
	// SourceDir is searched first for the Go source of the configuration
	// struct, whose field doc comments become descriptions. By default
	// the package is located with go/build, then the working directory.
	SourceDir string
	// Prefix and FuncMap must match the env.Options the app parses with.
	// When empty, they come from the struct's DocuconfOptions method, if
	// it has one (see OptionsProvider).
	Prefix  string
	FuncMap map[reflect.Type]env.ParserFunc
}

// maxDetails is the most characters (Unicode code points) an input's
// details may have (SPEC §4.2).
const maxDetails = 4000

var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// Export returns the contract for configuration struct T as CUE source
// that unifies with the docuconf meta-schema (#Contract).
//
// Descriptions come from each field's doc comment, read from the Go
// source, with the desc tag as a fallback; every input needs one of at
// least five characters. Export therefore runs where the source is
// available: in CI or through the docuconf export command.
func Export[T any](meta Meta) ([]byte, error) {
	return ExportType(reflect.TypeFor[T](), meta)
}

// ExportType is Export for a reflect.Type. Every problem with the
// declaration, the name and the descriptions is reported together, in one
// *DeclarationError.
func ExportType(t reflect.Type, meta Meta) ([]byte, error) {
	if t == nil || t.Kind() != reflect.Struct {
		return nil, errNotStruct("Export", t)
	}
	if meta.Prefix == "" || meta.FuncMap == nil {
		own := typeOptions(t)
		if meta.Prefix == "" {
			meta.Prefix = own.Prefix
		}
		if meta.FuncMap == nil {
			meta.FuncMap = own.FuncMap
		}
	}
	d, err := declare(t, declOptions{prefix: meta.Prefix, funcMap: meta.FuncMap})
	var problems []string
	var derr *DeclarationError
	switch {
	case errors.As(err, &derr):
		problems = append(problems, derr.Problems...)
	case err != nil:
		return nil, err
	}
	if !dnsLabelRe.MatchString(meta.Name) {
		problems = append(problems, fmt.Sprintf("service name %q must be a DNS label ([a-z0-9-], at most 63 characters)", meta.Name))
	}
	docs := newDocResolver(meta.SourceDir)
	// The description is the doc comment's first paragraph (or the desc
	// tag), and details the rest of the comment, as Markdown (splitDoc).
	describe := func(what, desc string, idx []int) (string, string) {
		doc, details := docs.fieldDoc(t, idx)
		if doc != "" {
			desc = doc
		}
		if utf8.RuneCountInString(desc) < 5 {
			problems = append(problems, fmt.Sprintf("%s needs a description of at least 5 characters: write a doc comment on the field, or a desc tag", what))
		}
		if n := utf8.RuneCountInString(details); n > maxDetails {
			problems = append(problems, fmt.Sprintf("%s: the doc comment after its first paragraph is %d characters; details may have at most %d", what, n, maxDetails))
		}
		return desc, details
	}

	vars := slices.Clone(d.vars)
	slices.SortFunc(vars, func(a, b *varDecl) int { return strings.Compare(a.name, b.name) })
	var varsObj obj
	for _, v := range vars {
		desc, details := describe(v.name+" ("+v.goPath+")", v.desc, v.index)
		o, err := v.contract(desc, details, docs)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", v.name, err))
		}
		varsObj = varsObj.add(v.name, o)
	}
	files := slices.Clone(d.files)
	slices.SortFunc(files, func(a, b *fileDecl) int { return strings.Compare(a.name, b.name) })
	var filesObj obj
	for _, f := range files {
		desc, details := describe("file input "+f.name+" ("+f.goPath+")", f.desc, f.index)
		o, err := f.contract(desc, details, docs)
		if err != nil {
			problems = append(problems, fmt.Sprintf("file input %s: %v", f.name, err))
		}
		filesObj = filesObj.add(f.name, o)
	}
	if len(problems) > 0 {
		return nil, &DeclarationError{Problems: problems}
	}

	metadata := obj{}.add("name", meta.Name)
	if meta.AppVersion != "" {
		metadata = metadata.add("appVersion", meta.AppVersion)
	}
	metadata = metadata.add("generator", obj{}.
		add("language", "go").
		add("sdk", "docuconf-go").
		add("version", Version))

	doc := obj{}.
		add("apiVersion", "docuconf.dev/v1alpha1").
		add("kind", "ConfigContract").
		add("metadata", metadata)
	if varsObj == nil {
		varsObj = obj{}
	}
	doc = doc.add("vars", varsObj)
	if len(filesObj) > 0 {
		doc = doc.add("files", filesObj)
	}

	return contractSource(doc, meta.Name, meta.Package), nil
}

// contractSource writes a contract document as contract.cue.
func contractSource(doc obj, name, pkg string) []byte {
	if pkg == "" {
		pkg = strings.ReplaceAll(name, "-", "_")
		if pkg == "" || (pkg[0] >= '0' && pkg[0] <= '9') {
			pkg = "c" + pkg
		}
	}
	var b strings.Builder
	b.WriteString("// Code generated by docuconf. DO NOT EDIT.\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("import \"docuconf.dev/contract\"\n\n")
	b.WriteString("contract.#Contract & ")
	writeValue(&b, doc, 0)
	b.WriteByte('\n')
	return []byte(b.String())
}

// contract returns the variable's fields in the order of SPEC §4.
func (v *varDecl) contract(desc, details string, docs *docResolver) (obj, error) {
	o := obj{}.add("type", v.typ).add("description", desc)
	if details != "" {
		o = o.add("details", details)
	}
	if v.required {
		o = o.add("required", true)
	}
	if v.secret {
		o = o.add("secret", true)
	}
	if v.hasDef && !v.loadFile && (v.def != "" || v.typ == typeString) {
		def, err := v.defaultValue()
		if err != nil {
			return o, err
		}
		o = o.add("default", def)
	}
	if v.group != "" {
		o = o.add("group", v.group)
	}
	if len(v.examples) > 0 {
		o = o.add("examples", stringsToList(v.examples))
	}
	if v.deprecated != "" {
		o = o.add("deprecated", obj{}.add("message", v.deprecated))
	}
	if v.configKey != "" {
		o = o.add("configKey", v.configKey)
	}
	switch v.typ {
	case typeString:
		if v.minLength != nil {
			o = o.add("minLength", int64(*v.minLength))
		}
		if v.maxLength != nil {
			o = o.add("maxLength", int64(*v.maxLength))
		}
		if v.pattern != nil {
			o = o.add("pattern", v.pattern.String())
		}
	case typeInt:
		if v.minInt != nil {
			o = o.add("min", json.Number(v.minInt.String()))
		}
		if v.maxInt != nil {
			o = o.add("max", json.Number(v.maxInt.String()))
		}
	case typeFloat:
		if v.minFloat != nil {
			o = o.add("min", *v.minFloat)
		}
		if v.maxFloat != nil {
			o = o.add("max", *v.maxFloat)
		}
	case typeDuration:
		if v.minDur != nil {
			o = o.add("min", formatDuration(*v.minDur))
		}
		if v.maxDur != nil {
			o = o.add("max", formatDuration(*v.maxDur))
		}
		o = o.add("encoding", "go") // time.ParseDuration
	case typeURL:
		if len(v.schemes) > 0 {
			o = o.add("schemes", stringsToList(v.schemes))
		}
		if v.maxLength != nil {
			o = o.add("maxLength", int64(*v.maxLength))
		}
	case typeEnum:
		o = o.add("values", stringsToList(v.values))
	case typeList:
		o = o.add("items", v.items).
			add("encoding", "csv"). // caarlos0/env splits on envSeparator
			add("separator", v.separator)
		if v.minItems != nil {
			o = o.add("minItems", int64(*v.minItems))
		}
		if v.maxItems != nil {
			o = o.add("maxItems", int64(*v.maxItems))
		}
		if v.itemMin != nil {
			o = o.add("itemMin", json.Number(v.itemMin.String()))
		}
		if v.itemMax != nil {
			o = o.add("itemMax", json.Number(v.itemMax.String()))
		}
		if v.itemMinLength != nil {
			o = o.add("itemMinLength", int64(*v.itemMinLength))
		}
		if v.itemMaxLength != nil {
			o = o.add("itemMaxLength", int64(*v.itemMaxLength))
		}
	case typeKeySet:
		// caarlos0/env splits a KeySet on envSeparator, like a list.
		o = o.add("encoding", "csv").add("separator", v.separator).
			add("minKeys", int64(*v.minItems)).add("maxKeys", int64(*v.maxItems))
		if v.itemMinLength != nil {
			o = o.add("keyMinLength", int64(*v.itemMinLength))
		}
		if v.itemMaxLength != nil {
			o = o.add("keyMaxLength", int64(*v.itemMaxLength))
		}
	case typeJSON:
		if v.maxLength != nil {
			o = o.add("maxLength", int64(*v.maxLength))
		}
		s, err := schemaFor(v.jsonType, docs)
		if err != nil {
			return o, err
		}
		o = o.add("schema", s.toValue())
	}
	return o, nil
}

// defaultValue converts envDefault to a typed contract value.
func (v *varDecl) defaultValue() (any, error) {
	raw := v.def
	switch v.typ {
	case typeInt:
		n, ok := new(big.Int).SetString(raw, 10)
		if !ok {
			return nil, fmt.Errorf("default %q is not an integer", raw)
		}
		return json.Number(n.String()), nil
	case typeFloat:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, err
		}
		return f, nil
	case typeBool:
		return strconv.ParseBool(strings.ToLower(raw))
	case typeDuration:
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, err
		}
		return formatDuration(d), nil
	case typeList:
		var out []any
		for _, item := range strings.Split(raw, v.separator) {
			if v.items == "int" {
				n, ok := new(big.Int).SetString(item, 10)
				if !ok {
					return nil, fmt.Errorf("default item %q is not an integer", item)
				}
				out = append(out, json.Number(n.String()))
			} else {
				out = append(out, item)
			}
		}
		return out, nil
	case typeJSON:
		doc, err := decodeJSON([]byte(raw))
		if err != nil {
			return nil, err
		}
		return fromJSON(doc), nil
	}
	return raw, nil
}

// contract returns the file input's fields in the order of SPEC §4.6.
func (f *fileDecl) contract(desc, details string, docs *docResolver) (obj, error) {
	o := obj{}.add("type", f.typ)
	if f.typ == fileConfig || f.typ == fileKeystore {
		o = o.add("format", f.format)
	}
	o = o.add("description", desc)
	if details != "" {
		o = o.add("details", details)
	}
	if f.required {
		o = o.add("required", true)
	}
	if f.secret {
		o = o.add("secret", true)
	}
	o = o.add("path", f.path)
	if f.pathEnv != "" {
		o = o.add("pathEnv", f.pathEnv)
	}
	if f.reload != "restart" {
		o = o.add("reload", f.reload)
	}
	if f.maxSize != nil {
		o = o.add("maxSize", *f.maxSize)
	}
	if f.group != "" {
		o = o.add("group", f.group)
	}
	if f.deprecated != "" {
		o = o.add("deprecated", obj{}.add("message", f.deprecated))
	}
	switch f.typ {
	case fileConfig:
		s, err := schemaFor(f.configType, docs)
		if err != nil {
			return o, err
		}
		o = o.add("schema", s.toValue())
	case fileTLS:
		if len(f.dnsNames) > 0 {
			o = o.add("dnsNames", stringsToList(f.dnsNames))
		}
		if len(f.keyAlgorithms) > 0 {
			o = o.add("keyAlgorithms", stringsToList(f.keyAlgorithms))
		}
		if f.minRemaining != nil {
			o = o.add("minRemaining", formatDuration(*f.minRemaining))
		}
		if f.requireCA {
			o = o.add("requireCA", true)
		}
	case fileCABundle:
		if f.minCertificates != 1 {
			o = o.add("minCertificates", int64(f.minCertificates))
		}
	case fileKeystore:
		if f.passwordVar != "" {
			o = o.add("passwordVar", f.passwordVar)
		}
	case fileText:
		if f.pattern != nil {
			o = o.add("pattern", f.pattern.String())
		}
		if f.minLength != nil {
			o = o.add("minLength", int64(*f.minLength))
		}
		if f.maxLength != nil {
			o = o.add("maxLength", int64(*f.maxLength))
		}
	}
	return o, nil
}
