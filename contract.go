package docuconf

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/big"
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
// Options.Environment, DotEnv, TerminationLog and Logger apply; the other
// options concern declared structs and file inputs. The contract's
// variables are all that is loaded: a contract with files, overlays or
// profiles is rejected, since this mode does not load them yet.
//
//	vals, err := docuconf.LoadContract(contractJSON, docuconf.Options{})
//	timeout := vals["REQUEST_TIMEOUT"].(time.Duration)
func LoadContract(contractJSON []byte, opts Options) (map[string]any, error) {
	vars, err := declareContract(contractJSON)
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
	if len(res.viols) > 0 {
		verr := &ValidationError{Violations: res.viols}
		writeTerminationLog(opts.TerminationLog, environ, verr, logger)
		return nil, verr
	}
	out := make(map[string]any, len(vars))
	for _, v := range vars {
		if val, ok := res.typed[v.name]; ok {
			out[v.name] = val
		} else {
			out[v.name] = v.defTyped // nil when there is no default
		}
	}
	return out, nil
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

var commonFields = []string{"name", "type", "description", "required", "secret", "default", "group", "examples", "deprecated", "configKey"}

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

// declareContract builds variable declarations from a contract document,
// so contract-first loading runs the same checks as a Go declaration.
func declareContract(contractJSON []byte) ([]*varDecl, error) {
	doc, err := decodeJSON(contractJSON)
	if err != nil {
		return nil, &DeclarationError{Problems: []string{fmt.Sprintf("contract is not valid JSON: %v", err)}}
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return nil, &DeclarationError{Problems: []string{"contract is not a JSON object"}}
	}
	var problems []string
	problemf := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if root["apiVersion"] != "docuconf.dev/v1alpha1" {
		problemf("apiVersion must be docuconf.dev/v1alpha1")
	}
	if root["kind"] != "ConfigContract" {
		problemf("kind must be ConfigContract")
	}
	for _, k := range []string{"files", "overlays", "profiles"} {
		if _, ok := root[k]; ok {
			problemf("contract-first mode loads variables only; the contract's %s are not supported", k)
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
	if len(problems) > 0 {
		return nil, &DeclarationError{Problems: problems}
	}
	return vars, nil
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
