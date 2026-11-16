package platform

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"cuelang.org/go/cue"
)

// RoleVar names the workload's role at runtime (SPEC §4.9).
const RoleVar = "DOCUCONF_ROLE"

// Roles are the roles a contract declares (SPEC §4.9); nil when it has
// none.
func (c *Contract) Roles() []string {
	var out []string
	_ = c.Value.LookupPath(cue.ParsePath("roles")).Decode(&out)
	return out
}

// ForRole is the contract as one role sees it (SPEC §4.9): only the inputs
// that role uses, with DOCUCONF_ROLE allowing only the role. A profile
// default or a requiredIf condition on a variable the role does not use is
// left out with it. Everything that checks or renders values for a
// workload of that role reads this contract.
func (p *Platform) ForRole(c *Contract, role string) (*Contract, error) {
	roles := c.Roles()
	switch {
	case len(roles) == 0:
		return nil, fmt.Errorf("the %s contract declares no roles", c.Name)
	case !slices.Contains(roles, role):
		return nil, fmt.Errorf("%s: %q is not one of the %s contract's roles, %s", RoleVar, role, c.Name, strings.Join(roles, ", "))
	}
	data, err := c.Value.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	used := func(in map[string]any) bool {
		rs, ok := in["roles"].([]any)
		return !ok || slices.Contains(rs, any(role))
	}
	out := map[string]string{}
	vars, _ := doc["vars"].(map[string]any)
	for n, x := range vars {
		in, _ := x.(map[string]any)
		if !used(in) {
			delete(vars, n)
			out[n] = role
			continue
		}
		delete(in, "roles")
	}
	if rv, ok := vars[RoleVar].(map[string]any); ok {
		rv["values"] = []any{role}
	}
	files, _ := doc["files"].(map[string]any)
	for n, x := range files {
		in, _ := x.(map[string]any)
		if !used(in) {
			delete(files, n)
			out[n] = role
			continue
		}
		delete(in, "roles")
	}
	// A condition on a variable the role does not use never holds in it.
	for _, group := range []map[string]any{vars, files} {
		for _, x := range group {
			in, _ := x.(map[string]any)
			if c, ok := in["requiredIf"].(map[string]any); ok {
				for cv := range c {
					if _, kept := vars[cv]; !kept {
						delete(in, "requiredIf")
					}
				}
			}
		}
	}
	if prof, ok := doc["profiles"].(map[string]any); ok {
		if defs, ok := prof["defaults"].(map[string]any); ok {
			for _, x := range defs {
				m, _ := x.(map[string]any)
				for n := range m {
					if _, kept := vars[n]; !kept {
						delete(m, n)
					}
				}
			}
		}
	}
	doc["roles"] = []any{role}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	rc, err := p.ParseContract(c.Name+"."+role+".json", b)
	if err != nil {
		return nil, err
	}
	rc.Role, rc.OutOfRole = role, out
	return rc, nil
}

// roleOf is the role a values document names in DOCUCONF_ROLE, or a
// problem line when it names none, or another value than a role.
func roleOf(c *Contract, values cue.Value) (string, string) {
	roles := strings.Join(c.Roles(), ", ")
	v := values.LookupPath(cue.MakePath(cue.Str(RoleVar)))
	if !v.Exists() {
		return "", fmt.Sprintf("%s: is required: the contract declares roles %s, and the platform names the workload's role", RoleVar, roles)
	}
	s, err := v.String()
	if err != nil || !slices.Contains(c.Roles(), s) {
		b, _ := v.MarshalJSON()
		return "", fmt.Sprintf("%s: %s is not one of %s", RoleVar, b, roles)
	}
	return s, ""
}
