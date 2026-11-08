package docuconf

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// condition is an input's requiredIf (SPEC §4.2.1): the input is required
// while variable name has the value raw.
type condition struct {
	name string
	raw  string // the value in wire form, as the condition variable reads it
	// value is the value typed by the condition variable: bool, int64 or
	// *big.Int, or string. Set by resolve.
	value any
}

// parseConditionTag parses a requiredIf tag: requiredIf:"TLS_ENABLED=true".
func parseConditionTag(s string) (*condition, error) {
	name, raw, ok := strings.Cut(s, "=")
	if !ok || !envNameRe.MatchString(name) {
		return nil, fmt.Errorf("requiredIf must be VAR=value, naming a variable that matches %s, e.g. requiredIf:\"TLS_ENABLED=true\"", envNameRe)
	}
	return &condition{name: name, raw: raw}, nil
}

// contractCondition reads a contract's requiredIf: {"TLS_ENABLED": true}.
// The value's JSON kind is checked against the variable's type in resolve.
func contractCondition(x any) (*condition, error) {
	m, ok := x.(map[string]any)
	if !ok || len(m) != 1 {
		return nil, fmt.Errorf("requiredIf must be an object naming exactly one variable")
	}
	for name, v := range m {
		if !envNameRe.MatchString(name) {
			return nil, fmt.Errorf("requiredIf must name a variable that matches %s", envNameRe)
		}
		c := &condition{name: name}
		switch v := v.(type) {
		case bool:
			c.raw, c.value = strconv.FormatBool(v), v
		case json.Number:
			c.raw, c.value = v.String(), v
		case string:
			c.raw, c.value = v, v
		default:
			return nil, fmt.Errorf("requiredIf: the value of %s must be a boolean, an integer or a string", name)
		}
		return c, nil
	}
	return nil, nil
}

// resolve checks the condition against the declared variables (SPEC
// §4.2.1) and types its value. input is the name of the input that has
// it.
func (c *condition) resolve(input string, byName func(string) *varDecl) []string {
	cv := byName(c.name)
	switch {
	case cv == nil:
		return []string{fmt.Sprintf("requiredIf names %s, which is not a declared variable", c.name)}
	case c.name == input:
		return []string{"requiredIf must name another variable"}
	case cv.secret:
		return []string{fmt.Sprintf("requiredIf names %s, which is secret: its value is never known before deploy", c.name)}
	}
	switch cv.typ {
	case typeString, typeEnum, typeInt, typeBool:
	default:
		return []string{fmt.Sprintf("requiredIf names %s, %s %s variable; it must name a string, int, bool or enum variable", c.name, typeArticle(cv.typ), cv.typ)}
	}
	// A contract's value has a JSON kind of its own, which must be the
	// variable's: "true" is not a bool.
	if c.value != nil {
		want := map[string]string{typeBool: "boolean", typeInt: "integer", typeString: "string", typeEnum: "string"}[cv.typ]
		var got string
		switch c.value.(type) {
		case bool:
			got = "boolean"
		case json.Number:
			got = "integer"
		default:
			got = "string"
		}
		if got != want {
			return []string{fmt.Sprintf("requiredIf: the value for %s must be a JSON %s, as %s is %s %s", c.name, want, c.name, typeArticle(cv.typ), cv.typ)}
		}
	}
	val, viols := cv.parse(c.raw)
	if len(viols) > 0 {
		return []string{fmt.Sprintf("requiredIf: %s %s", c.name, viols[0].Message)}
	}
	c.value = val
	return nil
}

// String is the condition as messages write it, the value as JSON:
// "TLS_ENABLED is true", "LOG_FORMAT is \"json\"".
func (c *condition) String() string {
	switch v := c.value.(type) {
	case string:
		b, _ := json.Marshal(v)
		return fmt.Sprintf("%s is %s", c.name, b)
	case nil:
		return fmt.Sprintf("%s is %s", c.name, c.raw)
	}
	return fmt.Sprintf("%s is %s", c.name, sameValue(c.value))
}

// contractValue is the value as the contract writes it.
func (c *condition) contractValue() any {
	switch v := c.value.(type) {
	case bool, string:
		return v
	}
	return json.Number(sameValue(c.value))
}

// sameValue is a typed value in the form two equal values share.
func sameValue(x any) string {
	switch v := x.(type) {
	case bool:
		return strconv.FormatBool(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case *big.Int:
		return v.String()
	case json.Number:
		if n, ok := new(big.Int).SetString(v.String(), 10); ok {
			return n.String()
		}
		return v.String()
	case string:
		return v
	}
	return fmt.Sprint(x)
}

// holds reports whether the condition holds on the condition variable's
// loaded value: the environment, an overlay or a profile (typed), else its
// default. A condition variable with a violation of its own makes nothing
// required: it is already reported.
func (c *condition) holds(vars []*varDecl, typed map[string]any, flagged map[string]bool) bool {
	if c == nil || flagged[c.name] {
		return false
	}
	if v, ok := typed[c.name]; ok {
		return sameValue(v) == sameValue(c.value)
	}
	for _, v := range vars {
		if v.name != c.name {
			continue
		}
		switch {
		case v.defTyped != nil:
			return sameValue(v.defTyped) == sameValue(c.value)
		case v.hasDef:
			def, viols := v.parse(v.def)
			return len(viols) == 0 && sameValue(def) == sameValue(c.value)
		}
	}
	return false
}

// checkRequiredIf reports each variable whose requiredIf condition holds
// and that is not set (SPEC §4.2.1).
func checkRequiredIf(vars []*varDecl, res *varResults) {
	for _, v := range vars {
		if v.requiredIf == nil || res.flagged[v.name] {
			continue
		}
		if _, ok := res.typed[v.name]; ok {
			continue
		}
		if v.requiredIf.holds(vars, res.typed, res.flagged) {
			res.viols = append(res.viols, Violation{Input: v.name, Code: CodeMissingRequired, Message: fmt.Sprintf("is required when %s but not set", v.requiredIf)})
			res.flagged[v.name] = true
		}
	}
}

// requiredNow is the file input as it loads: required, when its requiredIf
// condition holds.
func (f *fileDecl) requiredNow(vars []*varDecl, res varResults) *fileDecl {
	if f.required || !f.requiredIf.holds(vars, res.typed, res.flagged) {
		return f
	}
	cp := *f
	cp.required = true
	cp.requiredWhy = f.requiredIf.String()
	return &cp
}
