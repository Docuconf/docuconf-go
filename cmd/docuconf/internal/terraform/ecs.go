package terraform

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

// The ecs target renders an Amazon ECS container definition's environment
// and secrets (spec/IAC.md 5). A variable is a literal, a reference ECS
// resolves at task start (a Secrets Manager secret or a Parameter Store
// parameter), or injected. ECS mounts no files, so file inputs and
// overlays are not part of an ecs module.

// The forms of an ECS secrets[].valueFrom.
const (
	smARN   = `^arn:aws(-cn|-us-gov|-iso|-iso-b)?:secretsmanager:[a-z0-9-]+:[0-9]{12}:secret:[A-Za-z0-9/_+=.@-]+$`
	ssmARN  = `^arn:aws(-cn|-us-gov|-iso|-iso-b)?:ssm:[a-z0-9-]+:[0-9]{12}:parameter/[A-Za-z0-9/_.-]+$`
	bareRef = `^[A-Za-z0-9/_+=.@-]+$`
)

func ecsSources(v Var) []string {
	if v.Secret && str(v.Raw, "encoding") == "indexed" {
		// One variable per key: no single ECS secret carries it, but an
		// injector can set the variables.
		return []string{"injected"}
	}
	if v.Secret {
		return []string{"secret", "injected"}
	}
	return []string{"value", "injected"}
}

var ecsSourceTypes = map[string]string{
	// value_from is what ECS takes: a Secrets Manager ARN, a Parameter
	// Store ARN, or the name of either in the task's own account and region.
	// The selectors pick from a Secrets Manager secret.
	"secret":   "object({ value_from = string, json_key = optional(string), version_stage = optional(string), version_id = optional(string) })",
	"injected": "object({ provider = string, ref = optional(string) })",
}

func generateECS(c *Contract, opt Options) (map[string][]byte, error) {
	g := &gen{c: c, image: opt.Image}
	for _, v := range c.Vars {
		if err := g.checkSupported(v); err != nil {
			return nil, err
		}
	}
	var required []string
	for _, f := range c.Files {
		if boolean(f.Raw, "required") {
			required = append(required, f.Name)
		} else if opt.Warn != nil {
			opt.Warn(fmt.Sprintf("%s: ECS mounts no files, so the ecs module leaves this optional file input out", f.Name))
		}
	}
	if len(required) > 0 {
		return nil, fmt.Errorf("the ecs target mounts no files, and the contract requires file inputs %s; deploy it on a target that can (kubernetes)", strings.Join(required, ", "))
	}
	for _, o := range c.Overlays {
		if opt.Warn != nil {
			opt.Warn(fmt.Sprintf("overlay %s: ECS mounts no files, so the ecs module leaves this overlay out; set its variables in the environment", o.Name))
		}
	}
	digest := Digest(c.JSON)
	meta, err := metaJSON(c, opt, digest)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"versions.tf":   hclwrite.Format([]byte(Header + "\nterraform {\n  # optional() attributes, check blocks and output preconditions.\n  required_version = \">= 1.5.0\"\n}\n")),
		"variables.tf":  hclwrite.Format(g.ecsVariables()),
		"render.tf":     hclwrite.Format(g.ecsRender()),
		"outputs.tf":    hclwrite.Format(g.ecsOutputs(digest)),
		"docuconf.json": append(meta, '\n'),
		"README.md":     g.ecsReadme(),
	}, nil
}

func metaJSON(c *Contract, opt Options, digest string) ([]byte, error) {
	m := map[string]any{
		"apiVersion":     "docuconf.dev/v1alpha1",
		"kind":           "TerraformModule",
		"generator":      map[string]any{"name": "docuconf", "version": opt.GeneratorVersion},
		"targets":        opt.Targets,
		"contractDigest": digest,
		"contract":       json.RawMessage(c.JSON),
	}
	if opt.Image != "" {
		m["image"] = opt.Image
	}
	return json.MarshalIndent(m, "", "  ")
}

func (g *gen) ecsVariables() []byte {
	var b strings.Builder
	b.WriteString(Header)
	for _, v := range g.c.Vars {
		fmt.Fprintf(&b, "\nvariable %q {\n", v.Name)
		fmt.Fprintf(&b, "  description = %s\n", hclString(describeVar(v)))
		b.WriteString("  type = object({\n")
		for _, s := range ecsSources(v) {
			t := ecsSourceTypes[s]
			if s == "value" {
				t = valueType(v)
			}
			fmt.Fprintf(&b, "    %-8s = optional(%s)\n", s, t)
		}
		b.WriteString("  })\n")
		if boolean(v.Raw, "required") && !g.profileSupplies(v) {
			b.WriteString("  nullable = false\n")
		} else {
			b.WriteString("  default  = null\n")
		}
		for _, c := range g.ecsChecks(v, "var."+v.Name) {
			fmt.Fprintf(&b, "\n  validation {\n    condition     = %s\n    error_message = %s\n  }\n", c.cond, c.msg)
		}
		b.WriteString("}\n")
		if d, ok := v.Raw["deprecated"].(map[string]any); ok {
			writeDeprecated(&b, v.Name, "var."+v.Name+" == null", d)
		}
	}
	b.WriteString(`
variable "docuconf_options" {
  description = "Settings for the module that are not contract inputs (spec/IAC.md 4.2)."
  type = object({
    unmanaged_env = optional(list(string), []) # env the platform sets itself, ignored by docuconf lint
  })
  default  = {}
  nullable = false
}
`)
	return []byte(b.String())
}

// ecsChecks are an argument's validations on the ecs target: the source,
// the reference's form, and the contract's constraints on a literal.
func (g *gen) ecsChecks(v Var, ref string) []check {
	n := v.Name
	srcs := ecsSources(v)
	hint := ""
	switch {
	case v.Secret && !has(srcs, "secret"):
		hint = " (an indexed key set is one variable per key, which one ECS secret cannot carry; Terraform ignores other attributes here)"
	case v.Secret:
		hint = " (a secret is never a literal value; Terraform ignores a value attribute here)"
	default:
		hint = " (ECS has no ConfigMaps or Downward API; Terraform ignores other attributes here)"
	}
	out := []check{{
		cond: fmt.Sprintf("%s == null ? true : length([for k, s in %s : k if s != null]) == 1", ref, ref),
		msg:  hclString(fmt.Sprintf("%s: set exactly one of %s%s", n, orList(srcs), hint)),
	}}
	inj := ref + ".injected"
	out = append(out,
		check{
			cond: guard(ref, inj, fmt.Sprintf(`can(regex("^[a-z0-9]([-a-z0-9.]{0,61}[a-z0-9])?$", %s.provider))`, inj)),
			msg:  hclString(n + ": injected.provider must name the injector as a lowercase label, such as bank-vaults"),
		},
		check{
			cond: guard(ref, inj, fmt.Sprintf(`%s.ref == null ? true : %s.ref != ""`, inj, inj)),
			msg:  hclString(n + ": injected.ref cannot be empty; leave it out when the injector sets the variable itself"),
		},
	)
	if (v.Type == "list" || v.Type == "keySet") && str(v.Raw, "encoding") == "indexed" {
		out = append(out, check{
			cond: guard(ref, inj, inj+".ref == null"),
			msg:  hclString(n + ": an indexed list is spread over NAME__0, NAME__1, …, so one injected reference cannot carry it; let the injector set the variables, without ref"),
		})
	}
	if has(srcs, "secret") {
		s := ref + ".secret"
		vf := s + ".value_from"
		out = append(out,
			check{
				cond: guard(ref, s, fmt.Sprintf("can(regex(%s, %s)) || can(regex(%s, %s)) || can(regex(%s, %s))", hclString(smARN), vf, hclString(ssmARN), vf, hclString(bareRef), vf)),
				msg:  fmt.Sprintf(`"%s: secret.value_from ${jsonencode(%s)} is not a Secrets Manager ARN, a Parameter Store ARN, or the name of a secret or parameter"`, n, vf),
			},
			check{
				cond: guard(ref, s, fmt.Sprintf("(%s.json_key == null && %s.version_stage == null && %s.version_id == null) || can(regex(%s, %s))", s, s, s, hclString(smARN), vf)),
				msg:  hclString(n + ": secret.json_key, version_stage and version_id select from a Secrets Manager secret, so secret.value_from must be its full ARN"),
			},
			check{
				cond: guard(ref, s, fmt.Sprintf("%s.version_stage == null || %s.version_id == null", s, s)),
				msg:  hclString(n + ": set secret.version_stage or secret.version_id, not both"),
			},
		)
	} else if has(srcs, "value") {
		val := ref + ".value"
		for _, c := range valueChecks(v, val) {
			out = append(out, check{cond: guard(ref, val, c.cond), msg: c.msg})
		}
	}
	for i := range out {
		out[i].msg = safeMessage(out[i].msg)
	}
	return out
}

// ecsValueFrom is a secret argument's secrets[].valueFrom: the ARN, with
// any selectors appended as ECS writes them (arn:json-key:stage:id).
func ecsValueFrom(s string) string {
	sel := fmt.Sprintf("%s.json_key == null && %s.version_stage == null && %s.version_id == null", s, s, s)
	part := func(f string) string { return fmt.Sprintf(`${%s.%s == null ? "" : %s.%s}`, s, f, s, f) }
	return fmt.Sprintf(`%s ? %s.value_from : "${%s.value_from}:%s:%s:%s"`, sel, s, s, part("json_key"), part("version_stage"), part("version_id"))
}

func (g *gen) ecsRender() []byte {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString("\n# Each input's entries, in its wire encoding (SPEC.md 5). ECS does not expand $(VAR), so $ is not escaped.\nlocals {\n")
	noEsc := func(x string) string { return x }
	var envs []string
	for _, v := range g.c.Vars {
		ref := "var." + v.Name
		n := hclString(v.Name)
		injected := fmt.Sprintf(`%s.injected.ref == null ? [] : [{ name = %s, value = %s.injected.ref, value_from = null }]`, ref, n, ref)
		expr := "[]"
		switch srcs := ecsSources(v); {
		case has(srcs, "secret"):
			expr = fmt.Sprintf("[{ name = %s, value = null, value_from = %s }]", n, ecsValueFrom(ref+".secret"))
		case has(srcs, "value"):
			expr = renderWire(v, ref+".value", noEsc)
		}
		fmt.Fprintf(&b, "  env_%s = %s == null ? [] : (%s.injected != null ? (%s) : (%s))\n", v.Name, ref, ref, injected, expr)
		envs = append(envs, "local.env_"+v.Name)
	}
	all := "[]"
	if len(envs) > 0 {
		all = fmt.Sprintf("flatten([%s])", strings.Join(envs, ", "))
	}
	fmt.Fprintf(&b, "\n  entries         = %s\n", all)
	b.WriteString("  ecs_environment = [for e in local.entries : { name = e.name, value = e.value } if e.value_from == null]\n")
	b.WriteString("  ecs_secrets     = [for e in local.entries : { name = e.name, valueFrom = e.value_from } if e.value_from != null]\n")
	b.WriteString("\n  # How each input is set (spec/IAC.md 4.5); docuconf lint reads it.\n  bindings = {\n")
	for _, v := range g.c.Vars {
		var arms []string
		for _, s := range ecsSources(v) {
			arms = append(arms, fmt.Sprintf("var.%s.%s != null ? %q", v.Name, s, s))
		}
		fmt.Fprintf(&b, "    %s = { set = var.%s != null, source = var.%s == null ? null : (%s : null) }\n", v.Name, v.Name, v.Name, strings.Join(arms, " : "))
	}
	b.WriteString("  }\n}\n")
	return []byte(b.String())
}

func (g *gen) ecsOutputs(digest string) []byte {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString(`
# The container definition's environment: each { name, value }, in the contract's order.
output "ecs_environment" {
  description = "The container definition's environment, for container_definitions (README.md)."
  value       = local.ecs_environment
`)
	// The arguments' checks again: a value known only during apply is
	// checked before the task definition changes (spec/IAC.md 4.4).
	for _, v := range g.c.Vars {
		for _, c := range g.ecsChecks(v, "var."+v.Name) {
			fmt.Fprintf(&b, "\n  precondition {\n    condition     = %s\n    error_message = %s\n  }\n", c.cond, c.msg)
		}
	}
	b.WriteString(`}

# The container definition's secrets: each { name, valueFrom }, which ECS resolves when the task starts.
output "ecs_secrets" {
  description = "The container definition's secrets, for container_definitions. The task execution role must be allowed to read each."
  value       = local.ecs_secrets
}

output "bindings" {
  description = "For each input, whether it is set and by which source. docuconf lint reads it."
  value       = local.bindings
}

output "contract" {
  description = "The contract this module was generated from."
  value = {
`)
	b.WriteString(g.contractFields(digest))
	return []byte(b.String())
}

func (g *gen) ecsReadme() []byte {
	var b strings.Builder
	b.WriteString(MarkdownHeader)
	m := strings.ReplaceAll(g.c.Name, "-", "_") + "_config"
	fmt.Fprintf(&b, "# %s configuration module (ECS)\n\n", g.c.Name)
	fmt.Fprintf(&b, "Generated by `docuconf terraform -target ecs` from the `%s` contract. It takes one argument per input, checks it against the contract, and renders an ECS container definition's `environment` and `secrets`. See [spec/IAC.md](https://github.com/Docuconf/docuconf-go/blob/main/spec/IAC.md).\n\n", g.c.Name)
	b.WriteString("| Input | Type | Required | Set with | Description |\n|---|---|---|---|---|\n")
	for _, v := range g.c.Vars {
		req := ""
		if boolean(v.Raw, "required") && !g.profileSupplies(v) {
			req = "yes"
		}
		typ := v.Type
		if v.Type == "list" {
			typ = fmt.Sprintf("list of %s, %s", str(v.Raw, "items"), str(v.Raw, "encoding"))
		}
		if v.Secret {
			typ += ", secret"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", v.Name, typ, req, "`"+strings.Join(ecsSources(v), "`, `")+"`", strings.ReplaceAll(str(v.Raw, "description"), "|", `\|`))
	}
	if len(g.c.Files) > 0 || len(g.c.Overlays) > 0 {
		b.WriteString("\nECS mounts no files, so the contract's optional file inputs and overlays are not part of this module.\n")
	}
	b.WriteString("\nA secret's `value_from` is a Secrets Manager ARN, a Parameter Store ARN, or the name of either in the task's own account and region. `json_key`, `version_stage` and `version_id` select from a Secrets Manager secret. The task execution role must be allowed to read each secret (`secretsmanager:GetSecretValue`, `ssm:GetParameters`).\n\n")
	b.WriteString("## Usage\n\n```hcl\nmodule \"" + m + "\" {\n  source = \"./modules/" + g.c.Name + "-config\"\n")
	for _, v := range g.c.Vars {
		if !boolean(v.Raw, "required") || g.profileSupplies(v) {
			continue
		}
		if v.Secret {
			fmt.Fprintf(&b, "  %s = { secret = { value_from = aws_secretsmanager_secret.%s.arn } }\n", v.Name, strings.ToLower(v.Name))
		} else {
			fmt.Fprintf(&b, "  %s = { value = %s }\n", v.Name, exampleValue(v))
		}
	}
	fmt.Fprintf(&b, "}\n\nresource \"aws_ecs_task_definition\" \"app\" {\n  # …\n  container_definitions = jsonencode([{\n    name        = %q\n    image       = var.image\n    environment = module.%s.ecs_environment\n    secrets     = module.%s.ecs_secrets\n  }])\n}\n```\n", g.c.Name, m, m)
	return []byte(b.String())
}
