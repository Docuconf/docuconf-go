package docuconf

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"math/big"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// LoadContract validates an environment against a contract given as JSON,
// with no Go declaration: the contract-first mode of SPEC §11.2, item 11.
// The JSON is a contract as `cue export` writes it, or as `docuconf
// export` exports it and cue converts it.
//
// It returns every declared variable by name, typed:
//
//	string, url, enum  string
//	int                int64
//	float              float64
//	bool               bool
//	duration           time.Duration
//	list               []string or []int64
//	json               the decoded value: map[string]any, []any, string,
//	                   json.Number, bool or nil
//
// An optional variable that is unset and has no default is nil. Values
// are parsed in each variable's encoding: lists as csv (with their
// separator), json or indexed (NAME__0, NAME__1, ...), durations as go,
// iso8601, seconds or timespan. Violations are checked exactly as Parse
// checks a declared struct, and are all returned together in a
// *ValidationError, which is also written to the termination log. A
// contract that is itself invalid returns a *DeclarationError.
//
// File inputs are loaded and checked as Parse checks the matching Go
// types, with the same codes, and returned by input name (a DNS label,
// so it never clashes with a variable name), as the Go type for each
// file type:
//
//	tls       TLSKeyPair
//	caBundle  CABundle
//	keystore  Keystore
//	text      TextFile
//	binary    BinaryFile
//	config    ConfigFile[any], checked against the contract's schema and
//	          decoded as encoding/json decodes into an any
//
// A config file in the toml format, or a jks keystore, is a
// *DeclarationError: the Go SDK cannot read them.
//
// Options.Environment, DotEnv, FileRoot, TerminationLog, Now,
// WatchInterval and Logger apply; Prefix and FuncMap concern declared
// structs. A contract with overlays or profiles is rejected, since this
// mode does not load them yet.
//
//	vals, err := docuconf.LoadContract(contractJSON, docuconf.Options{})
//	timeout := vals["REQUEST_TIMEOUT"].(time.Duration)
//	licence := vals["licence"].(docuconf.TextFile).Content()
func LoadContract(contractJSON []byte, opts Options) (map[string]any, error) {
	vars, files, err := declareContract(contractJSON)
	if err != nil {
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	environ, err := environment(opts)
	if err != nil {
		return nil, err
	}
	res := checkVars(vars, environ, logger)
	viols := res.viols
	loaded := loadContractFiles(files, environ, res.raw, opts, logger)
	out := make(map[string]any, len(vars)+len(files))
	for _, f := range loaded {
		viols = append(viols, f.viols...)
		out[f.name] = f.value
	}
	if len(viols) > 0 {
		verr := &ValidationError{Violations: viols}
		writeTerminationLog(opts.TerminationLog, environ, verr, logger)
		return nil, verr
	}
	for _, v := range vars {
		if val, ok := res.typed[v.name]; ok {
			out[v.name] = val
		} else {
			out[v.name] = v.defTyped // nil when there is no default
		}
	}
	return out, nil
}

type loadedFile struct {
	name  string
	value any
	viols []Violation
}

// loadContractFiles binds each file input to a value of its Go type, the
// way load binds a struct's fields, so both paths run the same checks.
func loadContractFiles(files []*fileDecl, environ, raw map[string]string, opts Options, logger *slog.Logger) []loadedFile {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	interval := opts.WatchInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	root := opts.FileRoot
	if root == "" {
		root = environ[EnvFileRoot]
	}
	var out []loadedFile
	for _, f := range files {
		p := f.path
		if f.pathEnv != "" && environ[f.pathEnv] != "" {
			p = environ[f.pathEnv]
		}
		if root != "" {
			p = filepath.Join(root, p)
		}
		b := &fileBinding{
			decl:     f,
			path:     p,
			now:      now,
			interval: interval,
			logger:   logger,
			password: func() (string, bool) {
				// The password variable is usually declared; when it is
				// not, it is read from the environment as it is.
				if s, ok := raw[f.passwordVar]; ok {
					return s, true
				}
				s, ok := environ[f.passwordVar]
				return s, ok
			},
		}
		var fi fileInput
		switch f.typ {
		case fileTLS:
			fi = &TLSKeyPair{}
		case fileCABundle:
			fi = &CABundle{}
		case fileKeystore:
			fi = &Keystore{}
		case fileText:
			fi = &TextFile{}
		case fileBinary:
			fi = &BinaryFile{}
		case fileConfig:
			fi = &ConfigFile[any]{}
		}
		viols := fi.bind(b)
		value := reflect.ValueOf(fi).Elem().Interface()
		if f.deprecated != "" && len(viols) == 0 && value.(interface{ Present() bool }).Present() {
			logger.Warn("docuconf: deprecated file input is present", "input", f.name, "message", f.deprecated)
		}
		out = append(out, loadedFile{name: f.name, value: value, viols: viols})
	}
	return out
}

// contractFields lists the fields each variable type may carry, besides
// the common ones (SPEC §4.2, §4.3).
var contractFields = map[string][]string{
	typeString:   {"minLength", "maxLength", "pattern"},
	typeInt:      {"min", "max"},
	typeFloat:    {"min", "max"},
	typeBool:     {},
	typeDuration: {"min", "max", "encoding"},
	typeURL:      {"schemes"},
	typeEnum:     {"values"},
	typeList:     {"items", "encoding", "separator", "minItems", "maxItems", "itemMin", "itemMax"},
	typeJSON:     {"schema"},
}

var commonFields = []string{"name", "type", "description", "details", "required", "secret", "default", "group", "examples", "deprecated", "configKey"}

// Go types that stand for each contract type, for messages and parsing.
var contractGoTypes = map[string]reflect.Type{
	typeString:   reflect.TypeFor[string](),
	typeInt:      reflect.TypeFor[int64](),
	typeFloat:    reflect.TypeFor[float64](),
	typeBool:     reflect.TypeFor[bool](),
	typeDuration: durationType,
	typeURL:      reflect.TypeFor[string](),
	typeEnum:     reflect.TypeFor[string](),
	typeJSON:     reflect.TypeFor[any](),
}

// declareContract builds variable and file declarations from a contract
// document, so contract-first loading runs the same checks as a Go
// declaration.
func declareContract(contractJSON []byte) ([]*varDecl, []*fileDecl, error) {
	doc, err := decodeJSON(contractJSON)
	if err != nil {
		return nil, nil, &DeclarationError{Problems: []string{fmt.Sprintf("contract is not valid JSON: %v", err)}}
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return nil, nil, &DeclarationError{Problems: []string{"contract is not a JSON object"}}
	}
	var problems []string
	problemf := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if root["apiVersion"] != "docuconf.dev/v1alpha1" {
		problemf("apiVersion must be docuconf.dev/v1alpha1")
	}
	if root["kind"] != "ConfigContract" {
		problemf("kind must be ConfigContract")
	}
	for _, k := range []string{"overlays", "profiles"} {
		if _, ok := root[k]; ok {
			problemf("contract-first mode loads variables and files only; the contract's %s are not supported", k)
		}
	}
	varsObj, ok := root["vars"].(map[string]any)
	if !ok {
		problemf("vars must be an object")
	}
	names := make([]string, 0, len(varsObj))
	for n := range varsObj {
		names = append(names, n)
	}
	slices.Sort(names)
	var vars []*varDecl
	for _, name := range names {
		obj, ok := varsObj[name].(map[string]any)
		if !ok {
			problemf("%s: must be an object", name)
			continue
		}
		v := contractVar(name, obj, func(format string, args ...any) {
			problemf("%s: %s", name, fmt.Sprintf(format, args...))
		})
		if v != nil {
			vars = append(vars, v)
		}
	}
	var files []*fileDecl
	if x, ok := root["files"]; ok {
		filesObj, ok := x.(map[string]any)
		if !ok {
			problemf("files must be an object")
		}
		fnames := make([]string, 0, len(filesObj))
		for n := range filesObj {
			fnames = append(fnames, n)
		}
		slices.Sort(fnames)
		for _, name := range fnames {
			obj, ok := filesObj[name].(map[string]any)
			if !ok {
				problemf("file input %s: must be an object", name)
				continue
			}
			f := contractFile(name, obj, func(format string, args ...any) {
				problemf("file input %s: %s", name, fmt.Sprintf(format, args...))
			})
			if f != nil {
				files = append(files, f)
			}
		}
	}
	if len(problems) > 0 {
		return nil, nil, &DeclarationError{Problems: problems}
	}
	return vars, files, nil
}

// contractFileFields lists the fields each file type may carry, besides
// the common ones (SPEC §4.6).
var contractFileFields = map[string][]string{
	fileConfig:   {"format", "schema"},
	fileTLS:      {"dnsNames", "keyAlgorithms", "minRemaining", "requireCA"},
	fileCABundle: {"minCertificates"},
	fileKeystore: {"format", "passwordVar"},
	fileText:     {"pattern", "minLength", "maxLength"},
	fileBinary:   {},
}

var commonFileFields = []string{"name", "type", "description", "details", "required", "secret", "path", "pathEnv", "reload", "maxSize", "group", "deprecated"}

// contractFile builds one file input, reporting problems with it.
func contractFile(name string, o map[string]any, problem func(string, ...any)) *fileDecl {
	f := &fileDecl{name: name, goPath: name, reload: "restart", minCertificates: 1}
	bad := false
	fail := func(format string, args ...any) {
		bad = true
		problem(format, args...)
	}
	if !inputNameRe.MatchString(name) {
		fail("input name must be a DNS label matching %s", inputNameRe)
	}
	str := func(k string) (string, bool) {
		x, ok := o[k]
		if !ok {
			return "", false
		}
		s, isStr := x.(string)
		if !isStr {
			fail("%s must be a string", k)
		}
		return s, isStr
	}
	boolean := func(k string) (bool, bool) {
		x, ok := o[k]
		if !ok {
			return false, false
		}
		b, isBool := x.(bool)
		if !isBool {
			fail("%s must be a bool", k)
		}
		return b, isBool
	}
	strs := func(k string) []string {
		x, ok := o[k]
		if !ok {
			return nil
		}
		a, isArr := x.([]any)
		var out []string
		for _, e := range a {
			s, isStr := e.(string)
			if !isStr {
				isArr = false
				break
			}
			out = append(out, s)
		}
		if !isArr {
			fail("%s must be a list of strings", k)
		}
		return out
	}
	count := func(k string, min int64) (int64, bool) {
		x, ok := o[k]
		if !ok {
			return 0, false
		}
		num, isNum := x.(json.Number)
		i, err := num.Int64()
		if !isNum || err != nil || i < min {
			fail("%s must be an integer of at least %d", k, min)
			return 0, false
		}
		return i, true
	}

	if s, ok := str("name"); ok && s != name {
		fail("name %q does not match its key", s)
	}
	f.typ, _ = str("type")
	allowed, known := contractFileFields[f.typ]
	if !known {
		fail("type %q is not a file input type", f.typ)
		return nil
	}
	for k := range o {
		if !slices.Contains(commonFileFields, k) && !slices.Contains(allowed, k) {
			fail("field %s does not apply to a %s input", k, f.typ)
		}
	}
	f.desc, _ = str("description")
	if utf8.RuneCountInString(f.desc) < 5 {
		fail("description must be at least 5 characters")
	}
	if d, ok := str("details"); ok {
		checkDetails(d, fail)
	}
	f.required, _ = boolean("required")
	f.secret, _ = boolean("secret")
	if f.typ == fileTLS || f.typ == fileKeystore {
		f.secret = true // always secret (SPEC §4.6)
	}
	f.path, _ = str("path")
	if !absPathRe.MatchString(f.path) || dotSegRe.MatchString(f.path) ||
		strings.Contains(f.path, "//") || strings.HasSuffix(f.path, "/") {
		fail("path %q must be absolute and normalised", f.path)
	}
	if s, ok := str("pathEnv"); ok {
		if !envNameRe.MatchString(s) {
			fail("pathEnv must match %s", envNameRe)
		}
		f.pathEnv = s
	}
	if s, ok := str("reload"); ok {
		if s != "restart" && s != "watch" {
			fail("reload must be restart or watch")
		}
		f.reload = s
	}
	if n, ok := count("maxSize", 1); ok {
		f.maxSize = &n
	}
	f.group, _ = str("group")
	if d, ok := o["deprecated"]; ok {
		m, isObj := d.(map[string]any)
		msg, isStr := m["message"].(string)
		if !isObj || !isStr {
			fail("deprecated must be an object with a message")
		}
		f.deprecated = msg
		if f.deprecated == "" {
			f.deprecated = "deprecated"
		}
	}

	switch f.typ {
	case fileConfig:
		f.format, _ = str("format")
		if f.format != "json" && f.format != "yaml" {
			fail("format %q is not supported; the Go SDK reads json and yaml config files", f.format)
		}
		f.schema = &jsonSchema{}
		if s, ok := o["schema"]; ok {
			schema, err := schemaFromJSON(s, "schema")
			if err != nil {
				fail("%v", err)
			}
			f.schema = schema
		}
	case fileTLS:
		f.dnsNames = strs("dnsNames")
		f.keyAlgorithms = strs("keyAlgorithms")
		for _, a := range f.keyAlgorithms {
			if a != "RSA" && a != "ECDSA" && a != "Ed25519" {
				fail("keyAlgorithms: %q is not RSA, ECDSA or Ed25519", a)
			}
		}
		if s, ok := str("minRemaining"); ok {
			d, err := time.ParseDuration(s)
			if err != nil || d < 0 {
				fail("minRemaining must be a duration such as 720h")
			}
			f.minRemaining = &d
		}
		f.requireCA, _ = boolean("requireCA")
	case fileCABundle:
		if n, ok := count("minCertificates", 1); ok {
			f.minCertificates = int(n)
		}
	case fileKeystore:
		f.format = "pkcs12"
		if s, ok := str("format"); ok {
			f.format = s
		}
		if f.format != "pkcs12" {
			fail("format %q is not supported; the Go SDK reads pkcs12 keystores", f.format)
		}
		if s, ok := str("passwordVar"); ok {
			if !envNameRe.MatchString(s) {
				fail("passwordVar must match %s", envNameRe)
			}
			f.passwordVar = s
		}
	case fileText:
		if p, ok := str("pattern"); ok {
			re, err := regexp.Compile(p)
			if err != nil {
				fail("pattern is not valid RE2: %v", err)
			}
			f.pattern = re
		}
		for _, l := range []struct {
			k   string
			dst **int
		}{{"minLength", &f.minLength}, {"maxLength", &f.maxLength}} {
			if n, ok := count(l.k, 0); ok {
				m := int(n)
				*l.dst = &m
			}
		}
	}
	if bad {
		return nil
	}
	return f
}

// contractVar builds one variable, reporting problems with it.
func contractVar(name string, o map[string]any, problem func(string, ...any)) *varDecl {
	v := &varDecl{name: name, goPath: name, intBits: 64}
	bad := false
	fail := func(format string, args ...any) {
		bad = true
		problem(format, args...)
	}
	if !envNameRe.MatchString(name) {
		fail("variable name must match %s", envNameRe)
	}
	str := func(k string) (string, bool) {
		x, ok := o[k]
		if !ok {
			return "", false
		}
		s, isStr := x.(string)
		if !isStr {
			fail("%s must be a string", k)
		}
		return s, isStr
	}
	boolean := func(k string) bool {
		x, ok := o[k]
		if !ok {
			return false
		}
		b, isBool := x.(bool)
		if !isBool {
			fail("%s must be a bool", k)
		}
		return b
	}
	strs := func(k string) []string {
		x, ok := o[k]
		if !ok {
			return nil
		}
		a, isArr := x.([]any)
		var out []string
		for _, e := range a {
			s, isStr := e.(string)
			if !isStr {
				isArr = false
				break
			}
			out = append(out, s)
		}
		if !isArr {
			fail("%s must be a list of strings", k)
		}
		return out
	}
	integer := func(k string) *big.Int {
		x, ok := o[k]
		if !ok {
			return nil
		}
		num, isNum := x.(json.Number)
		i, isInt := new(big.Int).SetString(string(num), 10)
		if !isNum || !isInt {
			fail("%s must be an integer", k)
			return nil
		}
		return i
	}
	nonNeg := func(k string) *int {
		i := integer(k)
		if i == nil {
			return nil
		}
		if i.Sign() < 0 || !i.IsInt64() || i.Int64() > math.MaxInt32 {
			fail("%s must be a non-negative integer", k)
			return nil
		}
		n := int(i.Int64())
		return &n
	}

	if s, ok := str("name"); ok && s != name {
		fail("name %q does not match its key", s)
	}
	v.typ, _ = str("type")
	allowed, known := contractFields[v.typ]
	if !known {
		fail("type %q is not a contract type", v.typ)
		return nil
	}
	for k := range o {
		if !slices.Contains(commonFields, k) && !slices.Contains(allowed, k) {
			fail("field %s does not apply to a %s variable", k, v.typ)
		}
	}
	v.desc, _ = str("description")
	if utf8.RuneCountInString(v.desc) < 5 {
		fail("description must be at least 5 characters")
	}
	if d, ok := str("details"); ok {
		checkDetails(d, fail)
	}
	v.required = boolean("required")
	v.secret = boolean("secret")
	v.group, _ = str("group")
	v.configKey, _ = str("configKey")
	v.examples = strs("examples")
	if d, ok := o["deprecated"]; ok {
		m, isObj := d.(map[string]any)
		msg, isStr := m["message"].(string)
		if !isObj || !isStr {
			fail("deprecated must be an object with a message")
		}
		v.deprecated = msg
		if v.deprecated == "" {
			v.deprecated = "deprecated"
		}
	}
	v.goType = contractGoTypes[v.typ]

	switch v.typ {
	case typeString:
		v.minLength, v.maxLength = nonNeg("minLength"), nonNeg("maxLength")
		if p, ok := str("pattern"); ok {
			re, err := regexp.Compile(p)
			if err != nil {
				fail("pattern is not valid RE2: %v", err)
			}
			v.pattern = re
		}
	case typeInt:
		v.minInt, v.maxInt = integer("min"), integer("max")
	case typeFloat:
		v.minFloat, v.maxFloat = contractFloat(o, "min", fail), contractFloat(o, "max", fail)
	case typeDuration:
		v.durEncoding = encGo
		if e, ok := str("encoding"); ok {
			v.durEncoding = e
		}
		if !slices.Contains([]string{encGo, encISO8601, encSeconds, encTimespan}, v.durEncoding) {
			fail("encoding %q is not a duration encoding", v.durEncoding)
		}
		for _, b := range []struct {
			k string
			p **time.Duration
		}{{"min", &v.minDur}, {"max", &v.maxDur}} {
			if s, ok := str(b.k); ok {
				d, err := time.ParseDuration(s)
				if err != nil {
					fail("%s must be a duration such as 1m30s", b.k)
				}
				*b.p = &d
			}
		}
	case typeURL:
		v.schemes = strs("schemes")
		if _, ok := o["schemes"]; ok && len(v.schemes) == 0 {
			fail("schemes must list at least one scheme")
		}
	case typeEnum:
		v.values = strs("values")
		if len(v.values) == 0 {
			fail("values must list at least one value")
		}
	case typeList:
		v.items, _ = str("items")
		switch v.items {
		case "string":
			v.goType = reflect.TypeFor[[]string]()
		case "int":
			v.goType = reflect.TypeFor[[]int64]()
		default:
			fail("items must be string or int")
		}
		v.listEncoding = encCSV
		if e, ok := str("encoding"); ok {
			v.listEncoding = e
		}
		if !slices.Contains([]string{encCSV, encJSON, encIndexed}, v.listEncoding) {
			fail("encoding %q is not a list encoding", v.listEncoding)
		}
		v.separator = ","
		if s, ok := str("separator"); ok {
			if v.listEncoding != encCSV {
				fail("separator applies only to the csv encoding")
			}
			v.separator = s
		}
		if v.separator == "" {
			fail("separator must not be empty")
		}
		v.minItems, v.maxItems = nonNeg("minItems"), nonNeg("maxItems")
		v.itemMin, v.itemMax = integer("itemMin"), integer("itemMax")
		if (v.itemMin != nil || v.itemMax != nil) && v.items != "int" {
			fail("itemMin and itemMax apply only to lists of integers")
		}
	case typeJSON:
		if s, ok := o["schema"]; ok {
			schema, err := schemaFromJSON(s, "schema")
			if err != nil {
				fail("%v", err)
			}
			v.schema = schema
		}
	}
	if v.minInt != nil && v.maxInt != nil && v.minInt.Cmp(v.maxInt) > 0 {
		fail("min is greater than max")
	}
	if v.itemMin != nil && v.itemMax != nil && v.itemMin.Cmp(v.itemMax) > 0 {
		fail("itemMin is greater than itemMax")
	}

	def, hasDef := o["default"]
	if hasDef && v.required {
		fail("a required variable must not have a default")
	}
	if hasDef && v.secret {
		fail("a secret variable must not have a default")
	}
	if v.secret && len(v.examples) > 0 {
		fail("a secret variable must not have examples")
	}
	if bad {
		return nil
	}
	if hasDef {
		val, err := v.typedDefault(def)
		if err != "" {
			problem("default %s", err)
			return nil
		}
		v.defTyped = val
	}
	return v
}

func contractFloat(o map[string]any, k string, fail func(string, ...any)) *float64 {
	x, ok := o[k]
	if !ok {
		return nil
	}
	num, isNum := x.(json.Number)
	f, err := num.Float64()
	if !isNum || err != nil || math.IsInf(f, 0) {
		fail("%s must be a finite number", k)
		return nil
	}
	return &f
}

// typedDefault checks a contract default against the variable's own
// constraints, as a declaration's default is, and returns it typed. It
// is written back to wire form and parsed by the same code as a value,
// in the go and json encodings that every default can be written in.
func (v *varDecl) typedDefault(def any) (any, string) {
	w := *v
	var raw string
	ok := true
	switch v.typ {
	case typeString, typeURL, typeEnum, typeDuration:
		raw, ok = def.(string)
		w.durEncoding = encGo
	case typeInt, typeFloat:
		var n json.Number
		n, ok = def.(json.Number)
		raw = n.String()
	case typeBool:
		var b bool
		b, ok = def.(bool)
		raw = fmt.Sprint(b)
	case typeList, typeJSON:
		_, isList := def.([]any)
		ok = isList || v.typ == typeJSON
		b, err := json.Marshal(def)
		ok = ok && err == nil
		raw = string(b)
		w.listEncoding = encJSON
	}
	if !ok {
		return nil, fmt.Sprintf("is not a %s value", v.typ)
	}
	val, viols := w.parse(raw)
	if len(viols) > 0 {
		msgs := make([]string, len(viols))
		for i, x := range viols {
			msgs[i] = x.Message
		}
		return nil, strings.Join(msgs, "; ")
	}
	return val, ""
}

// ContractCUE writes a contract document, given as JSON, as a contract.cue
// in the form Export writes: the generated-code header, a package clause
// (pkg, or the service name with dashes replaced by underscores), the
// meta-schema import and contract.#Contract & {...}, with variables and
// file inputs sorted by name and their fields in the order of SPEC §4.
// It is for SDKs and generators that build the contract themselves, in a
// language Export cannot reflect on.
//
// The contract is first checked as LoadContract checks it, and a
// *DeclarationError lists every problem; so, as there, overlays and
// profiles are not supported.
func ContractCUE(contractJSON []byte, pkg string) ([]byte, error) {
	if _, _, err := declareContract(contractJSON); err != nil {
		return nil, err
	}
	doc, _ := decodeJSON(contractJSON) // declareContract decoded it already
	root := doc.(map[string]any)
	name, _ := nested(root, "metadata")["name"].(string)
	if !dnsLabelRe.MatchString(name) {
		return nil, &DeclarationError{Problems: []string{fmt.Sprintf("metadata.name %q must be a DNS label ([a-z0-9-], at most 63 characters)", name)}}
	}
	out := ordered(root, []string{"apiVersion", "kind", "metadata", "vars", "files"}, func(k string, v any) any {
		switch k {
		case "metadata":
			return ordered(v, []string{"name", "appVersion", "generator"}, func(k string, v any) any {
				if k == "generator" {
					return ordered(v, []string{"language", "sdk", "version"}, nil)
				}
				return fromJSON(v)
			})
		case "vars":
			return ordered(v, nil, input(varFieldOrder))
		case "files":
			return ordered(v, nil, input(fileFieldOrder))
		}
		return fromJSON(v)
	})
	return contractSource(out.(obj), name, pkg), nil
}

// The order Export writes a variable's and a file input's fields in.
// checkDetails applies SPEC §4.2's rule for details: docs only, so never
// read at runtime, but not blank and at most maxDetails characters.
func checkDetails(d string, fail func(string, ...any)) {
	switch {
	case strings.TrimSpace(d) == "":
		fail("details must not be blank")
	case utf8.RuneCountInString(d) > maxDetails:
		fail("details must be at most %d characters", maxDetails)
	}
}

var (
	varFieldOrder = []string{"type", "description", "details", "required", "secret", "default", "group", "examples", "deprecated", "configKey",
		"minLength", "maxLength", "pattern", "min", "max", "encoding", "schemes", "values", "items", "separator",
		"minItems", "maxItems", "itemMin", "itemMax", "schema"}
	fileFieldOrder = []string{"type", "format", "description", "details", "required", "secret", "path", "pathEnv", "reload", "maxSize", "group", "deprecated",
		"schema", "dnsNames", "keyAlgorithms", "minRemaining", "requireCA", "minCertificates", "passwordVar", "pattern", "minLength", "maxLength"}
)

func nested(m map[string]any, k string) map[string]any {
	x, _ := m[k].(map[string]any)
	return x
}

// ordered converts a decoded JSON object to obj, with the keys in order
// first and any others after them, sorted. sub converts each value; by
// default it is fromJSON.
func ordered(v any, order []string, sub func(k string, v any) any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return fromJSON(v)
	}
	if sub == nil {
		sub = func(_ string, v any) any { return fromJSON(v) }
	}
	var keys, rest []string
	for _, k := range order {
		if _, ok := m[k]; ok {
			keys = append(keys, k)
		}
	}
	for k := range m {
		if !slices.Contains(order, k) {
			rest = append(rest, k)
		}
	}
	slices.Sort(rest)
	o := obj{}
	for _, k := range append(keys, rest...) {
		o = o.add(k, sub(k, m[k]))
	}
	return o
}

// input orders a variable's or file input's fields. Its name is left
// out: the meta-schema derives it from the key.
func input(order []string) func(string, any) any {
	return func(_ string, v any) any {
		if m, ok := v.(map[string]any); ok {
			v = maps.Clone(m)
			delete(v.(map[string]any), "name")
		}
		return ordered(v, order, nil)
	}
}
