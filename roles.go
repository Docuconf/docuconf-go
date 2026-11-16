package docuconf

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// EnvRole names the role a workload runs (SPEC §4.9). The platform sets it
// when the contract declares roles, and the SDK reads it at boot.
const EnvRole = "DOCUCONF_ROLE"

// Roles declares the roles one image runs as, one per kind of process:
// the web server, the job worker, the migration job (SPEC §4.9). Declare a
// blank field of this type, and tag each input only some roles use:
//
//	type Config struct {
//		_ docuconf.Roles `roles:"web,worker,migrate"`
//		DatabaseURL docuconf.Secret `env:"DATABASE_URL,required"`
//		Origins []string `env:"ALLOWED_ORIGINS,required" roles:"web"`
//		Queues  []string `env:"QUEUES,required" roles:"worker"`
//	}
//
// The exported contract lists the roles, each input's roles, and the
// variable DOCUCONF_ROLE, which the platform sets to the workload's role.
// Parse requires it, and then requires only the inputs that role uses: an
// input of another role is optional, and keeps its zero value when unset.
type Roles struct{}

var rolesType = reflect.TypeOf(Roles{})

// addRoles reads a Roles field's roles tag.
func (d *declaration) addRoles(f reflect.StructField, fp string) {
	if d.roles != nil {
		d.problemf("%s: roles are declared twice", fp)
		return
	}
	d.roles = splitList(f.Tag.Get("roles"))
	if len(d.roles) == 0 {
		d.problemf("%s: a docuconf.Roles field needs a roles tag naming the roles, e.g. roles:\"web,worker\"", fp)
	}
	d.roles = append([]string{}, d.roles...)
	checkRoleNames(d.roles, func(format string, args ...any) { d.problemf("%s: %s", fp, fmt.Sprintf(format, args...)) })
}

func checkRoleNames(roles []string, problem func(string, ...any)) {
	for i, r := range roles {
		if !inputNameRe.MatchString(r) {
			problem("role %q must be a DNS label matching %s", r, inputNameRe)
		}
		if slices.Contains(roles[:i], r) {
			problem("role %s is listed twice", r)
		}
	}
}

// inputRoles reads an input's roles tag.
func inputRoles(tag reflect.StructTag, problem func(string, ...any)) []string {
	s, ok := tag.Lookup("roles")
	if !ok {
		return nil
	}
	rs := splitList(s)
	if len(rs) == 0 {
		problem("roles must name at least one role; leave it out for an input every role uses")
	}
	checkRoleNames(rs, problem)
	return rs
}

// resolveRoles checks the inputs' roles against the declared ones, and
// adds DOCUCONF_ROLE.
func (d *declaration) resolveRoles() {
	check := func(what string, rs []string) {
		for _, r := range rs {
			if !slices.Contains(d.roles, r) {
				if d.roles == nil {
					d.problemf("%s: has roles, but the struct declares none; declare them with a docuconf.Roles field", what)
					return
				}
				d.problemf("%s: role %s is not one of the declared roles, %s", what, r, strings.Join(d.roles, ", "))
			}
		}
	}
	for _, v := range d.vars {
		check(v.name+" ("+v.goPath+")", v.roles)
		if v.name == EnvRole && d.roles != nil {
			d.problemf("%s (%s): docuconf declares %s for the roles itself; remove the field", v.name, v.goPath, EnvRole)
		}
	}
	for _, f := range d.files {
		check("file input "+f.name+" ("+f.goPath+")", f.roles)
	}
	if d.roles == nil {
		return
	}
	values := make([]any, len(d.roles))
	for i, r := range d.roles {
		values[i] = r
	}
	v := contractVar(EnvRole, map[string]any{
		"type":        typeEnum,
		"description": "The role this workload runs: " + strings.Join(d.roles, ", "),
		"required":    true,
		"values":      values,
	}, func(format string, args ...any) { d.problemf("%s: %s", EnvRole, fmt.Sprintf(format, args...)) })
	if v != nil {
		v.goPath = "roles"
		d.fragmentVars = append(d.fragmentVars, v)
	}
}

// forRole is the inputs as a workload of the role in environ sees them
// (SPEC §4.9): an input another role uses is not required there. The role
// variable itself is checked like any other, so a missing or unknown role
// is reported; then only the inputs every role uses are required, so that
// the role is the one problem reported about roles. out names the inputs
// the role does not use.
func forRole(roles []string, vars []*varDecl, files []*fileDecl, environ map[string]string) (rv []*varDecl, rf []*fileDecl, out map[string]bool) {
	if len(roles) == 0 {
		return vars, files, nil
	}
	role := environ[EnvRole]
	if !slices.Contains(roles, role) {
		role = "" // no role uses a role-specific input
	}
	out = map[string]bool{}
	for _, v := range vars {
		if v.roles != nil && !slices.Contains(v.roles, role) {
			cp := *v
			cp.required, cp.requiredIf, cp.notEmpty = false, nil, false
			v = &cp
			out[v.name] = true
		}
		rv = append(rv, v)
	}
	for _, f := range files {
		if f.roles != nil && !slices.Contains(f.roles, role) {
			cp := *f
			cp.required, cp.requiredIf = false, nil
			f = &cp
			out[f.name] = true
		}
		rf = append(rf, f)
	}
	return rv, rf, out
}
