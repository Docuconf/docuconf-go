package docuconf

import (
	"fmt"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// File input types (SPEC §4.6).
const (
	fileConfig   = "config"
	fileTLS      = "tls"
	fileCABundle = "caBundle"
	fileKeystore = "keystore"
	fileText     = "text"
	fileBinary   = "binary"
)

// fileTags lists the docuconf file tags and the file types each applies to.
// A nil list means every type.
var fileTags = map[string][]string{
	"path":            nil,
	"pathEnv":         nil,
	"reload":          nil,
	"maxSize":         nil,
	"desc":            nil,
	"group":           nil,
	"deprecated":      nil,
	"secret":          nil,
	"format":          {fileConfig, fileKeystore},
	"dnsNames":        {fileTLS},
	"keyAlgorithms":   {fileTLS},
	"minRemaining":    {fileTLS},
	"requireCA":       {fileTLS},
	"minCertificates": {fileCABundle},
	"passwordVar":     {fileKeystore},
	"pattern":         {fileText},
	"minLength":       {fileText},
	"maxLength":       {fileText},
}

// fileTagKeys are the struct tag keys docuconf reads on a file field.
var fileTagKeys = func() []string {
	keys := []string{"file"}
	for k := range fileTags {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}()

func (d *declaration) addFile(src reflect.Type, sf reflect.StructField, idx []int, fp, fileTag string) {
	name, opts := splitTag(fileTag)
	fi := reflect.New(sf.Type).Interface().(fileInput)
	f := &fileDecl{
		name:    name,
		index:   idx,
		goPath:  fp,
		goType:  sf.Type,
		srcType: src,
		field:   sf.Name,
		typ:     fi.fileType(),
		reload:  "restart",
	}
	tag := sf.Tag
	problem := func(format string, args ...any) {
		d.problemf("file input %s (%s): %s", name, fp, fmt.Sprintf(format, args...))
	}
	if !inputNameRe.MatchString(name) {
		problem("input name must be a DNS label matching %s", inputNameRe)
	}
	for _, p := range tagTypos(tag, fileTagKeys) {
		problem("%s", p)
	}
	for _, o := range opts {
		switch o {
		case "required":
			f.required = true
		case "":
		default:
			problem("unknown file tag option %q", o)
		}
	}
	for t, types := range fileTags {
		if _, ok := tag.Lookup(t); ok && types != nil && !slices.Contains(types, f.typ) {
			problem("tag %s does not apply to a %s input", t, f.typ)
		}
	}

	f.path = tag.Get("path")
	switch {
	case f.path == "":
		problem("a path tag is required")
	case !absPathRe.MatchString(f.path) || dotSegRe.MatchString(f.path) ||
		strings.Contains(f.path, "//") || strings.HasSuffix(f.path, "/"):
		problem("path %q must be absolute and normalised", f.path)
	}
	f.pathEnv = tag.Get("pathEnv")
	if f.pathEnv != "" && !envNameRe.MatchString(f.pathEnv) {
		problem("pathEnv must match %s", envNameRe)
	}
	if r, ok := tag.Lookup("reload"); ok {
		if r != "restart" && r != "watch" {
			problem("reload must be restart or watch")
		}
		f.reload = r
	}
	if s, ok := tag.Lookup("maxSize"); ok {
		n, err := parseSize(s)
		if err != nil {
			problem("maxSize: %v", err)
		} else {
			f.maxSize = &n
		}
	}
	f.desc = tag.Get("desc")
	f.group = tag.Get("group")
	f.deprecated = tag.Get("deprecated")

	f.secret = f.typ == fileTLS || f.typ == fileKeystore
	if s, ok := tag.Lookup("secret"); ok {
		b, err := strconv.ParseBool(s)
		switch {
		case err != nil:
			problem("secret tag must be true or false")
		case !b && f.secret:
			problem("a %s input is always secret", f.typ)
		default:
			f.secret = b
		}
	}

	switch f.typ {
	case fileConfig:
		f.format = tag.Get("format")
		if f.format == "" {
			switch path.Ext(f.path) {
			case ".json":
				f.format = "json"
			case ".yaml", ".yml":
				f.format = "yaml"
			}
		}
		if f.format != "json" && f.format != "yaml" {
			problem("format must be json or yaml (set it with a format tag, or use a .json, .yaml or .yml path)")
		}
		f.configType = fi.(configInput).configType()
		s, err := schemaFor(f.configType, nil)
		if err != nil {
			problem("%v", err)
		}
		f.schema = s
	case fileTLS:
		f.dnsNames = splitList(tag.Get("dnsNames"))
		f.keyAlgorithms = splitList(tag.Get("keyAlgorithms"))
		for _, a := range f.keyAlgorithms {
			if a != "RSA" && a != "ECDSA" && a != "Ed25519" {
				problem("keyAlgorithms: %q is not RSA, ECDSA or Ed25519", a)
			}
		}
		if s, ok := tag.Lookup("minRemaining"); ok {
			dur, err := time.ParseDuration(s)
			if err != nil || dur < 0 {
				problem("minRemaining must be a duration such as 720h")
			} else {
				f.minRemaining = &dur
			}
		}
		if s, ok := tag.Lookup("requireCA"); ok {
			b, err := strconv.ParseBool(s)
			if err != nil {
				problem("requireCA must be true or false")
			}
			f.requireCA = b
		}
	case fileCABundle:
		f.minCertificates = 1
		if s, ok := tag.Lookup("minCertificates"); ok {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				problem("minCertificates must be a positive integer")
			} else {
				f.minCertificates = n
			}
		}
	case fileKeystore:
		f.format = tag.Get("format")
		if f.format == "" {
			f.format = "pkcs12"
		}
		if f.format != "pkcs12" {
			problem("format must be pkcs12; the Go SDK cannot read %q keystores", f.format)
		}
		f.passwordVar = tag.Get("passwordVar")
		if f.passwordVar != "" && !envNameRe.MatchString(f.passwordVar) {
			problem("passwordVar must match %s", envNameRe)
		}
	case fileText:
		if p, ok := tag.Lookup("pattern"); ok {
			re, err := regexp.Compile(p)
			if err != nil {
				problem("pattern is not valid RE2: %v", err)
			}
			f.pattern = re
		}
		for _, l := range []struct {
			name string
			dst  **int
		}{{"minLength", &f.minLength}, {"maxLength", &f.maxLength}} {
			if s, ok := tag.Lookup(l.name); ok {
				n, err := strconv.Atoi(s)
				if err != nil || n < 0 {
					problem("%s must be a non-negative integer", l.name)
					continue
				}
				*l.dst = &n
			}
		}
	}
	d.files = append(d.files, f)
}

// parseSize parses a byte count, with an optional Ki, Mi or Gi suffix.
func parseSize(s string) (int64, error) {
	mult := int64(1)
	for suffix, m := range map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30} {
		if strings.HasSuffix(s, suffix) {
			s, mult = strings.TrimSuffix(s, suffix), m
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a positive size in bytes (suffixes Ki, Mi and Gi are allowed)", s)
	}
	return n * mult, nil
}
