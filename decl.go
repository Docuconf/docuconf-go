package docuconf

import (
	"encoding"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Contract types (SPEC §4.3).
const (
	typeString   = "string"
	typeInt      = "int"
	typeFloat    = "float"
	typeBool     = "bool"
	typeDuration = "duration"
	typeURL      = "url"
	typeEnum     = "enum"
	typeList     = "list"
	typeJSON     = "json"
)

var (
	envNameRe   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	inputNameRe = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,40}[a-z0-9])?$`)
	absPathRe   = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)
	dotSegRe    = regexp.MustCompile(`(^|/)\.\.?(/|$)`)
	urlRe       = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://[^\s]+$`)

	durationType        = reflect.TypeOf(time.Duration(0))
	urlType             = reflect.TypeOf(url.URL{})
	locationType        = reflect.TypeOf(time.Location{})
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

// reservedDirs are directories a file input must never be mounted over,
// because the mount would hide what the image or the OS keeps there. It
// mirrors #ReservedDirs in the meta-schema.
var reservedDirs = []string{
	"/", "/app", "/bin", "/boot", "/dev", "/etc", "/etc/pki", "/etc/ssl",
	"/etc/ssl/certs", "/home", "/lib", "/lib64", "/opt", "/proc", "/root",
	"/run", "/sbin", "/srv", "/sys", "/tmp", "/usr", "/usr/lib", "/usr/local",
	"/usr/share", "/var", "/var/lib", "/var/run",
}

// varDecl is one environment variable read from a struct field.
type varDecl struct {
	name    string       // final env name, prefix included
	index   []int        // field index path from the root struct
	goPath  string       // Config.DB.Port, for messages
	goType  reflect.Type // field type with any pointer removed
	srcType reflect.Type // struct type that declares the field, for docs
	field   string       // Go field name within srcType

	typ        string
	required   bool
	secret     bool
	loadFile   bool
	expand     bool
	notEmpty   bool
	def        string
	hasDef     bool
	defTyped   any // a contract's default, typed (contract-first mode)
	desc       string
	group      string
	examples   []string
	deprecated string
	configKey  string

	minLength, maxLength *int
	pattern              *regexp.Regexp

	minInt, maxInt     *big.Int
	minFloat, maxFloat *float64
	minDur, maxDur     *time.Duration

	schemes []string
	values  []string

	// Integer parsing, mirroring caarlos0/env's parsers.
	intBits  int
	unsigned bool

	// Lists. itemMin and itemMax bound each item of an int list, and
	// include the range the element kind holds.
	items              string // "string" or "int"
	separator          string
	minItems, maxItems *int
	itemMin, itemMax   *big.Int

	// Wire encodings (SPEC §5). A declaration always uses the encodings
	// caarlos0/env parses, "go" and "csv"; a contract may name any.
	durEncoding  string
	listEncoding string

	// json variables.
	jsonType reflect.Type
	schema   *jsonSchema

	// custom validates the value of a type parsed by a TextUnmarshaler or
	// a FuncMap parser. Its error may include the value, so it is only
	// shown for non-secret variables.
	custom func(string) error
}

// fileDecl is one file input read from a struct field.
type fileDecl struct {
	name    string
	index   []int
	goPath  string
	goType  reflect.Type
	srcType reflect.Type
	field   string

	typ        string
	required   bool
	secret     bool
	path       string
	pathEnv    string
	reload     string
	maxSize    *int64
	desc       string
	group      string
	deprecated string

	format string // config: json|yaml; keystore: pkcs12

	// config
	configType reflect.Type
	schema     *jsonSchema

	// tls
	dnsNames      []string
	keyAlgorithms []string
	minRemaining  *time.Duration
	requireCA     bool

	// caBundle
	minCertificates int

	// keystore
	passwordVar string

	// text
	pattern              *regexp.Regexp
	minLength, maxLength *int
}

// mountDir is the directory the platform mounts for the input.
func (f *fileDecl) mountDir() string {
	if f.typ == fileTLS {
		return f.path
	}
	return path.Dir(f.path)
}

// declaration is everything docuconf knows about a configuration struct.
type declaration struct {
	root     reflect.Type
	vars     []*varDecl
	files    []*fileDecl
	problems []string
}

type declOptions struct {
	prefix  string
	funcMap map[reflect.Type]env.ParserFunc
}

// declare reads a configuration struct type. It returns a
// *DeclarationError listing every problem with the declaration itself.
func declare(t reflect.Type, opts declOptions) (*declaration, error) {
	if t == nil || t.Kind() != reflect.Struct {
		return nil, &DeclarationError{Problems: []string{fmt.Sprintf("%v is not a struct type", t)}}
	}
	d := &declaration{root: t}
	d.walk(t, nil, opts.prefix, t.Name(), opts, map[reflect.Type]bool{})
	d.crossCheck()
	if len(d.problems) > 0 {
		return d, &DeclarationError{Problems: d.problems}
	}
	return d, nil
}

func (d *declaration) problemf(format string, args ...any) {
	d.problems = append(d.problems, fmt.Sprintf(format, args...))
}

func (d *declaration) walk(t reflect.Type, index []int, prefix, goPath string, opts declOptions, visiting map[reflect.Type]bool) {
	if visiting[t] {
		d.problemf("%s: recursive struct type %v", goPath, t)
		return
	}
	visiting[t] = true
	defer delete(visiting, t)

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue // caarlos0/env cannot set it either
		}
		idx := append(slices.Clone(index), i)
		fp := goPath + "." + f.Name
		key, envOpts := splitTag(f.Tag.Get("env"))
		fileTag, hasFile := f.Tag.Lookup("file")

		if isFileType(f.Type) {
			if !hasFile {
				d.problemf("%s: %v needs a file tag naming the input, e.g. file:\"serving-tls\"", fp, f.Type)
				continue
			}
			d.addFile(t, f, idx, fp, fileTag)
			continue
		}
		if f.Type.Kind() == reflect.Pointer && isFileType(f.Type.Elem()) {
			d.problemf("%s: use %v, not a pointer to it", fp, f.Type.Elem())
			continue
		}
		if hasFile {
			d.problemf("%s: the file tag only applies to docuconf file types (TLSKeyPair, CABundle, Keystore, TextFile, BinaryFile, ConfigFile)", fp)
			continue
		}
		if key == "-" || slices.Contains(envOpts, "-") {
			continue
		}

		base := f.Type
		isPtr := base.Kind() == reflect.Pointer
		if isPtr {
			base = base.Elem()
		}
		if base.Kind() == reflect.Struct && !isValueStruct(base, opts.funcMap) {
			if key != "" {
				d.problemf("%s: %v is a struct type, which caarlos0/env cannot parse from one variable; register a parser for it in FuncMap (and return that FuncMap from a DocuconfOptions method so export sees it too), or drop the env tag and nest it with envPrefix", fp, base)
				continue
			}
			// caarlos0/env only descends into a nil pointer with ",init".
			if isPtr && !slices.Contains(envOpts, "init") {
				continue
			}
			d.walk(base, idx, prefix+f.Tag.Get("envPrefix"), fp, opts, visiting)
			continue
		}
		if key == "" {
			continue // caarlos0/env ignores fields without an env tag
		}
		if base.Kind() == reflect.Slice && base.Elem().Kind() == reflect.Struct && !isValueStruct(base.Elem(), opts.funcMap) {
			d.problemf("%s: slices of structs are not supported", fp)
			continue
		}
		d.addVar(t, f, idx, fp, prefix+key, envOpts, opts)
	}
}

// isValueStruct reports whether a struct type is parsed from a single
// variable rather than walked as a nested group.
func isValueStruct(t reflect.Type, funcMap map[reflect.Type]env.ParserFunc) bool {
	if t == urlType || t == locationType || isJSONType(t) {
		return true
	}
	if _, ok := funcMap[t]; ok {
		return true
	}
	return reflect.PointerTo(t).Implements(textUnmarshalerType)
}

func splitTag(tag string) (string, []string) {
	parts := strings.Split(tag, ",")
	return parts[0], parts[1:]
}

// splitList splits a comma-separated tag value, trimming spaces.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// constraintTags lists the docuconf constraint tags and the contract types
// each applies to.
var constraintTags = map[string][]string{
	"minLength": {typeString},
	"maxLength": {typeString},
	"pattern":   {typeString},
	"min":       {typeInt, typeFloat, typeDuration},
	"max":       {typeInt, typeFloat, typeDuration},
	"schemes":   {typeURL},
	"values":    {typeEnum},
	"minItems":  {typeList},
	"maxItems":  {typeList},
	"itemMin":   {typeList},
	"itemMax":   {typeList},
}

func (d *declaration) addVar(src reflect.Type, f reflect.StructField, idx []int, fp, name string, envOpts []string, opts declOptions) {
	v := &varDecl{
		name:    name,
		index:   idx,
		goPath:  fp,
		srcType: src,
		field:   f.Name,
		goType:  f.Type,
	}
	if v.goType.Kind() == reflect.Pointer {
		v.goType = v.goType.Elem()
	}
	tag := f.Tag
	problem := func(format string, args ...any) {
		d.problemf("%s (%s): %s", name, fp, fmt.Sprintf(format, args...))
	}

	if !envNameRe.MatchString(name) {
		problem("variable name must match %s", envNameRe)
	}
	for _, p := range tagTypos(tag, varTagKeys) {
		problem("%s", p)
	}
	for _, o := range envOpts {
		switch o {
		case "required":
			v.required = true
		case "file":
			v.loadFile = true
		case "expand":
			v.expand = true
		case "notEmpty":
			v.notEmpty = true
		case "", "unset", "init":
		default:
			problem("unknown env tag option %q", o)
		}
	}
	v.def, v.hasDef = tag.Lookup("envDefault")
	v.desc = tag.Get("desc")
	v.group = tag.Get("group")
	v.configKey = tag.Get("configKey")
	v.deprecated = tag.Get("deprecated")
	if ex, ok := tag.Lookup("examples"); ok {
		v.examples = strings.Split(ex, "|")
	}
	if s, ok := tag.Lookup("secret"); ok {
		b, err := strconv.ParseBool(s)
		if err != nil {
			problem("secret tag must be true or false")
		}
		v.secret = b
	}
	if v.goType == secretType {
		if !v.secret && tag.Get("secret") != "" {
			problem("a docuconf.Secret field is always secret; remove secret:%q", tag.Get("secret"))
		}
		v.secret = true
	}

	v.typ = d.contractType(v, tag, opts, problem)
	if v.typ == "" {
		return
	}

	for t, types := range constraintTags {
		if _, ok := tag.Lookup(t); ok && !slices.Contains(types, v.typ) {
			problem("tag %s does not apply to %s %s variable", t, typeArticle(v.typ), v.typ)
		}
	}
	v.parseConstraints(tag, problem)

	if v.required && v.hasDef {
		problem("a required variable must not have a default")
	}
	if v.secret && v.hasDef {
		problem("a secret variable must not have a default")
	}
	if v.secret && len(v.examples) > 0 {
		problem("a secret variable must not have examples")
	}
	if v.hasDef && v.def != "" && !v.loadFile {
		for _, viol := range v.check(v.def) {
			problem("default %s", viol.Message)
		}
	}
	d.vars = append(d.vars, v)
}

// contractType maps a Go type to a contract type, and records how values
// of that type are checked.
func (d *declaration) contractType(v *varDecl, tag reflect.StructTag, opts declOptions, problem func(string, ...any)) string {
	t := v.goType
	_, hasValues := tag.Lookup("values")
	_, hasSchemes := tag.Lookup("schemes")
	explicit := tag.Get("type")
	if explicit != "" && explicit != typeURL {
		problem("the type tag only accepts \"url\"")
	}

	if custom := customParser(t, opts.funcMap); custom != nil && t != durationType && t != urlType {
		v.custom = custom
		if isJSONType(t) {
			v.jsonType = jsonValueType(t)
			s, err := schemaFor(v.jsonType, nil)
			if err != nil {
				problem("%v", err)
				return ""
			}
			v.schema = s
			return typeJSON
		}
		if hasValues {
			return typeEnum
		}
		if hasSchemes || explicit == typeURL {
			return typeURL
		}
		return typeString
	}

	switch {
	case t == durationType:
		return typeDuration
	case t == urlType:
		return typeURL
	case t == locationType:
		problem("time.Location is not a contract type; use a string")
		return ""
	}

	switch t.Kind() {
	case reflect.String:
		switch {
		case hasValues:
			return typeEnum
		case hasSchemes || explicit == typeURL:
			return typeURL
		}
		return typeString
	case reflect.Bool:
		return typeBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.intBits, v.unsigned = intParsing(t.Kind())
		return typeInt
	case reflect.Float32, reflect.Float64:
		return typeFloat
	case reflect.Slice:
		et := t.Elem()
		if et.Kind() == reflect.Pointer {
			et = et.Elem()
		}
		v.separator = tag.Get("envSeparator")
		if v.separator == "" {
			v.separator = ","
		}
		if custom := customParser(et, opts.funcMap); custom != nil {
			v.items = "string"
			v.custom = custom
			return typeList
		}
		switch et.Kind() {
		case reflect.String:
			v.items = "string"
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			v.items = "int"
			v.intBits, v.unsigned = intParsing(et.Kind())
		default:
			problem("lists of %v are not a contract type; use strings or integers", et)
			return ""
		}
		return typeList
	}
	problem("%v is not a contract type", v.goType)
	return ""
}

// customParser returns a validity check for types parsed by a FuncMap
// entry or a TextUnmarshaler, or nil for built-in kinds.
func customParser(t reflect.Type, funcMap map[reflect.Type]env.ParserFunc) func(string) error {
	if p, ok := funcMap[t]; ok {
		return func(s string) error { _, err := p(s); return err }
	}
	if t == durationType {
		return func(s string) error { _, err := time.ParseDuration(s); return err }
	}
	if t == urlType {
		return func(s string) error { _, err := url.Parse(s); return err }
	}
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return func(s string) error {
			return reflect.New(t).Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(s))
		}
	}
	return nil
}

// intParsing mirrors the bit sizes caarlos0/env parses each kind with.
// Note that it parses int and uint as 32-bit values.
func intParsing(k reflect.Kind) (bits int, unsigned bool) {
	switch k {
	case reflect.Int, reflect.Int32:
		return 32, false
	case reflect.Int8:
		return 8, false
	case reflect.Int16:
		return 16, false
	case reflect.Int64:
		return 64, false
	case reflect.Uint, reflect.Uint32:
		return 32, true
	case reflect.Uint8:
		return 8, true
	case reflect.Uint16:
		return 16, true
	}
	return 64, true
}

// intRange is the range caarlos0/env accepts for an integer kind, or nil
// for 64-bit signed, which is the contract's own range.
func intRange(bits int, unsigned bool) (lo, hi *big.Int) {
	if unsigned {
		lo = big.NewInt(0)
		if bits < 64 {
			hi = new(big.Int).SetUint64(1<<uint(bits) - 1)
		}
		return lo, hi
	}
	if bits == 64 {
		return nil, nil
	}
	return big.NewInt(-1 << uint(bits-1)), big.NewInt(1<<uint(bits-1) - 1)
}

func (v *varDecl) parseConstraints(tag reflect.StructTag, problem func(string, ...any)) {
	nonNeg := func(name string) *int {
		s, ok := tag.Lookup(name)
		if !ok {
			return nil
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			problem("%s must be a non-negative integer", name)
			return nil
		}
		return &n
	}
	v.minLength, v.maxLength = nonNeg("minLength"), nonNeg("maxLength")
	v.minItems, v.maxItems = nonNeg("minItems"), nonNeg("maxItems")
	if p, ok := tag.Lookup("pattern"); ok {
		re, err := regexp.Compile(p)
		if err != nil {
			problem("pattern is not valid RE2: %v", err)
		} else {
			v.pattern = re
		}
	}
	if s, ok := tag.Lookup("schemes"); ok {
		v.schemes = splitList(s)
		if len(v.schemes) == 0 {
			problem("schemes must list at least one scheme")
		}
	}
	if s, ok := tag.Lookup("values"); ok {
		v.values = splitList(s)
		if len(v.values) == 0 {
			problem("values must list at least one value")
		}
	}

	minS, hasMin := tag.Lookup("min")
	maxS, hasMax := tag.Lookup("max")
	switch v.typ {
	case typeInt:
		v.minInt, v.maxInt = v.intBounds(tag, "min", "max", v.goType, problem)
	case typeList:
		_, hasItemMin := tag.Lookup("itemMin")
		_, hasItemMax := tag.Lookup("itemMax")
		if v.items == "int" {
			v.itemMin, v.itemMax = v.intBounds(tag, "itemMin", "itemMax", v.goType.Elem(), problem)
		} else if hasItemMin || hasItemMax {
			problem("itemMin and itemMax apply only to lists of integers")
		}
	case typeFloat:
		parse := func(name, s string) *float64 {
			f, err := strconv.ParseFloat(s, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				problem("%s must be a finite number", name)
				return nil
			}
			return &f
		}
		if hasMin {
			v.minFloat = parse("min", minS)
		}
		if hasMax {
			v.maxFloat = parse("max", maxS)
		}
	case typeDuration:
		parse := func(name, s string) *time.Duration {
			dur, err := time.ParseDuration(s)
			if err != nil {
				problem("%s must be a duration such as 1m30s", name)
				return nil
			}
			return &dur
		}
		if hasMin {
			v.minDur = parse("min", minS)
		}
		if hasMax {
			v.maxDur = parse("max", maxS)
		}
	}
}

// intBounds reads a pair of integer bound tags. Each defaults to, and
// must lie within, the range caarlos0/env parses the integer kind with,
// so the contract never accepts a value the field cannot hold.
func (v *varDecl) intBounds(tag reflect.StructTag, minName, maxName string, t reflect.Type, problem func(string, ...any)) (lo, hi *big.Int) {
	kindLo, kindHi := intRange(v.intBits, v.unsigned)
	parse := func(name string, def *big.Int) *big.Int {
		s, ok := tag.Lookup(name)
		if !ok {
			return def
		}
		n, ok := new(big.Int).SetString(s, 10)
		if !ok {
			problem("%s must be an integer", name)
			return def
		}
		if (kindLo != nil && n.Cmp(kindLo) < 0) || (kindHi != nil && n.Cmp(kindHi) > 0) {
			problem("%s %s is outside the range of %v", name, s, t)
		}
		return n
	}
	lo, hi = parse(minName, kindLo), parse(maxName, kindHi)
	if lo != nil && hi != nil && lo.Cmp(hi) > 0 {
		problem("%s is greater than %s", minName, maxName)
	}
	return lo, hi
}

// crossCheck applies rules that span several inputs.
func (d *declaration) crossCheck() {
	vars := map[string]*varDecl{}
	for _, v := range d.vars {
		if prev, ok := vars[v.name]; ok {
			d.problemf("%s is declared twice (%s and %s)", v.name, prev.goPath, v.goPath)
		}
		vars[v.name] = v
	}
	names := map[string]string{}
	mounts := map[string]string{}
	pathEnvs := map[string]string{}
	for _, f := range d.files {
		if prev, ok := names[f.name]; ok {
			d.problemf("file input %s is declared twice (%s and %s)", f.name, prev, f.goPath)
		}
		names[f.name] = f.goPath
		if f.path != "" {
			dir := f.mountDir()
			if other, ok := mounts[dir]; ok {
				d.problemf("file inputs %s and %s share the mount directory %s; each needs its own", other, f.name, dir)
			}
			mounts[dir] = f.name
			if slices.Contains(reservedDirs, dir) {
				d.problemf("file input %s would be mounted at %s, which hides what the image keeps there; use a dedicated directory", f.name, dir)
			}
		}
		if f.pathEnv != "" {
			if _, ok := vars[f.pathEnv]; ok {
				d.problemf("file input %s: pathEnv %s must not also be declared as a variable", f.name, f.pathEnv)
			}
			if other, ok := pathEnvs[f.pathEnv]; ok {
				d.problemf("file inputs %s and %s share pathEnv %s", other, f.name, f.pathEnv)
			}
			pathEnvs[f.pathEnv] = f.name
		}
		if f.typ == fileKeystore && f.passwordVar != "" {
			pv, ok := vars[f.passwordVar]
			if !ok || !pv.secret {
				d.problemf("file input %s: passwordVar %s must name a declared secret variable", f.name, f.passwordVar)
			}
		}
	}
}

// varByName returns the declared variable with the given name.
func (d *declaration) varByName(name string) *varDecl {
	for _, v := range d.vars {
		if v.name == name {
			return v
		}
	}
	return nil
}

// typeArticle returns "an" before a contract type that starts with a vowel
// sound, and "a" otherwise.
func typeArticle(typ string) string {
	switch typ {
	case typeInt, typeEnum:
		return "an"
	}
	return "a"
}
