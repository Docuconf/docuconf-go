package terraform

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// requiredIf is an input's condition (SPEC.md 4.2.1): the variable it
// names and the value that makes the input required, or "" when it has
// none.
func requiredIf(raw map[string]any) (name string, value any) {
	c, _ := raw["requiredIf"].(map[string]any)
	for n, x := range c {
		return n, x
	}
	return "", nil
}

// whenText is the condition as vet writes it: "TLS_ENABLED is true", the
// value as JSON.
func whenText(raw map[string]any) string {
	n, x := requiredIf(raw)
	if n == "" {
		return ""
	}
	b, _ := json.Marshal(x)
	return fmt.Sprintf("%s is %s", n, b)
}

// hclLiteral is a condition value or a default in HCL.
func hclLiteral(x any) string {
	switch x := x.(type) {
	case string:
		return hclString(x)
	case nil:
		return "null"
	}
	b, _ := json.Marshal(x)
	return string(b)
}

// conditionValue is an HCL expression for a condition variable's value:
// its argument's literal, else an overlay's, else its default. It is null
// where the value is not known here: the argument is a reference, or a
// profile may set it (spec/IAC.md 4.3). docuconf lint and the SDK check
// those.
func (g *gen) conditionValue(name string, overlays bool) string {
	var cv *Var
	for i := range g.c.Vars {
		if g.c.Vars[i].Name == name {
			cv = &g.c.Vars[i]
		}
	}
	if cv == nil {
		return "null"
	}
	base := "null"
	if !g.profileSupplies(*cv) {
		base = hclLiteral(cv.Raw["default"])
	}
	for i := len(g.c.Overlays) - 1; overlays && i >= 0; i-- {
		o := g.c.Overlays[i]
		if ok, _ := g.overlayVar(*cv, o); ok {
			base = fmt.Sprintf("((%s == null ? false : %s != null) ? %s : %s)", overlayRef(o), overlayValue(o, *cv), overlayValue(o, *cv), base)
		}
	}
	return fmt.Sprintf("(var.%s != null ? var.%s.value : %s)", name, name, base)
}

// requiredIfChecks are the preconditions for requiredIf (SPEC.md 4.2.1):
// an input whose condition holds must be set, as #Validate's
// missingRequired has it. files are the file inputs the target carries; a
// conditional one it leaves out is checked to stay unneeded. overlays says
// whether the module has overlay arguments.
func (g *gen) requiredIfChecks(target string, files []File, overlays bool) []check {
	var out []check
	for _, v := range g.c.Vars {
		n, x := requiredIf(v.Raw)
		if n == "" || g.profileSupplies(v) {
			continue
		}
		set := []string{"var." + v.Name + " != null"}
		if overlays {
			set = append(set, g.inOverlays(v)...)
		}
		out = append(out, check{
			fmt.Sprintf("%s != %s || %s", g.conditionValue(n, overlays), hclLiteral(x), strings.Join(set, " || ")),
			hclString(fmt.Sprintf("%s: is required when %s, and set neither by the platform nor by the selected profile", v.Name, whenText(v.Raw))),
		})
	}
	for _, f := range g.c.Files {
		n, x := requiredIf(f.Raw)
		if n == "" {
			continue
		}
		if !slices.ContainsFunc(files, func(c File) bool { return c.Name == f.Name }) {
			out = append(out, check{
				fmt.Sprintf("%s != %s", g.conditionValue(n, overlays), hclLiteral(x)),
				hclString(fmt.Sprintf("%s: is a required file input when %s, and the %s module cannot carry it; deploy it on a target that can (kubernetes)", f.Name, whenText(f.Raw), target)),
			})
			continue
		}
		out = append(out, check{
			fmt.Sprintf("%s != %s || var.%s != null", g.conditionValue(n, overlays), hclLiteral(x), f.Name),
			hclString(fmt.Sprintf("%s: is a required file input when %s, and has no source", f.Name, whenText(f.Raw))),
		})
	}
	return out
}
