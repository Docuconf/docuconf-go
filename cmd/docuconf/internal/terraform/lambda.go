package terraform

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

// The lambda target renders an AWS Lambda function's environment variables
// (spec/IAC.md 5). A variable is a literal or injected. Lambda resolves no
// secret references in its environment, so a secret input is injected: its
// ref, if any, such as a Secrets Manager ARN, is the value, and the
// function reads the secret itself (through the AWS Parameters and Secrets
// Lambda Extension, or an SDK). Lambda mounts no files and does not expand
// $(VAR).

// lambdaReserved are the environment variables Lambda sets itself and
// refuses in a function's configuration.
var lambdaReserved = map[string]bool{
	"_HANDLER": true, "_X_AMZN_TRACE_ID": true, "AWS_DEFAULT_REGION": true, "AWS_REGION": true,
	"AWS_EXECUTION_ENV": true, "AWS_LAMBDA_FUNCTION_NAME": true, "AWS_LAMBDA_FUNCTION_MEMORY_SIZE": true,
	"AWS_LAMBDA_FUNCTION_VERSION": true, "AWS_LAMBDA_INITIALIZATION_TYPE": true, "AWS_LAMBDA_LOG_GROUP_NAME": true,
	"AWS_LAMBDA_LOG_STREAM_NAME": true, "AWS_ACCESS_KEY": true, "AWS_ACCESS_KEY_ID": true,
	"AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true, "AWS_LAMBDA_RUNTIME_API": true,
	"LAMBDA_TASK_ROOT": true, "LAMBDA_RUNTIME_DIR": true,
}

// LambdaEnvLimit is the size Lambda allows a function's environment
// variables, keys and values together.
const LambdaEnvLimit = 4096

// LambdaReserved reports whether Lambda refuses an environment variable.
func LambdaReserved(name string) bool { return lambdaReserved[name] }

func lambdaSources(v Var) []string {
	if v.Secret {
		return []string{"injected"} // Lambda has no secret references
	}
	return []string{"value", "injected"}
}

func generateLambda(c *Contract, opt Options) (map[string][]byte, error) {
	g := &gen{c: c}
	var reserved []string
	for _, v := range c.Vars {
		if err := g.checkSupported(v); err != nil {
			return nil, err
		}
		if lambdaReserved[v.Name] {
			reserved = append(reserved, v.Name)
		}
	}
	if len(reserved) > 0 {
		sort.Strings(reserved)
		return nil, fmt.Errorf("the contract declares %s, which Lambda reserves and refuses in a function's configuration; rename them in the contract", strings.Join(reserved, ", "))
	}
	var required []string
	for _, f := range c.Files {
		if boolean(f.Raw, "required") {
			required = append(required, f.Name)
		} else if opt.Warn != nil {
			opt.Warn(fmt.Sprintf("%s: Lambda mounts no files, so the lambda module leaves this optional file input out", f.Name))
		}
	}
	if len(required) > 0 {
		return nil, fmt.Errorf("the lambda target mounts no files, and the contract requires file inputs %s; deploy it on a target that can (kubernetes)", strings.Join(required, ", "))
	}
	for _, o := range c.Overlays {
		if opt.Warn != nil {
			opt.Warn(fmt.Sprintf("overlay %s: Lambda mounts no files, so the lambda module leaves this overlay out; set its variables in the environment", o.Name))
		}
	}
	digest := Digest(c.JSON)
	meta, err := metaJSON(c, opt, digest)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"versions.tf":   hclwrite.Format([]byte(Header + "\nterraform {\n  # optional() attributes, check blocks and output preconditions.\n  required_version = \">= 1.5.0\"\n}\n")),
		"variables.tf":  hclwrite.Format(g.lambdaVariables()),
		"render.tf":     hclwrite.Format(g.lambdaRender()),
		"outputs.tf":    hclwrite.Format(g.lambdaOutputs(digest)),
		"docuconf.json": append(meta, '\n'),
		"README.md":     g.lambdaReadme(),
	}, nil
}

func (g *gen) lambdaVariables() []byte {
	var b strings.Builder
	b.WriteString(Header)
	for _, v := range g.c.Vars {
		fmt.Fprintf(&b, "\nvariable %q {\n", v.Name)
		fmt.Fprintf(&b, "  description = %s\n", hclString(describeVar(v)))
		b.WriteString("  type = object({\n")
		for _, s := range lambdaSources(v) {
			t := ecsSourceTypes["injected"]
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
		for _, c := range g.lambdaChecks(v, "var."+v.Name) {
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

// lambdaChecks are an argument's validations on the lambda target, as
// ecsChecks are on ecs.
func (g *gen) lambdaChecks(v Var, ref string) []check {
	n := v.Name
	srcs := lambdaSources(v)
	hint := " (Lambda has no ConfigMaps or Downward API; Terraform ignores other attributes here)"
	if v.Secret {
		hint = " (Lambda resolves no secret references, so a secret is injected: give the secret's ARN as ref, and the function reads it; Terraform ignores other attributes here)"
	}
	out := []check{{
		cond: fmt.Sprintf("%s == null ? true : length([for k, s in %s : k if s != null]) == 1", ref, ref),
		msg:  hclString(fmt.Sprintf("%s: set exactly one of %s%s", n, orList(srcs), hint)),
	}}
	inj := ref + ".injected"
	out = append(out,
		check{
			cond: guard(ref, inj, fmt.Sprintf(`can(regex("^[a-z0-9]([-a-z0-9.]{0,61}[a-z0-9])?$", %s.provider))`, inj)),
			msg:  hclString(n + ": injected.provider must name the injector as a lowercase label, such as aws-parameters-and-secrets"),
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
	if has(srcs, "value") {
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

func (g *gen) lambdaRender() []byte {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString("\n# Each input's entries, in its wire encoding (SPEC.md 5). Lambda does not expand $(VAR), so $ is not escaped.\nlocals {\n")
	noEsc := func(x string) string { return x }
	var envs []string
	for _, v := range g.c.Vars {
		ref := "var." + v.Name
		n := hclString(v.Name)
		injected := fmt.Sprintf(`%s.injected.ref == null ? [] : [{ name = %s, value = %s.injected.ref, value_from = null }]`, ref, n, ref)
		expr := "[]"
		if has(lambdaSources(v), "value") {
			expr = renderWire(v, ref+".value", noEsc)
		}
		fmt.Fprintf(&b, "  env_%s = %s == null ? [] : (%s.injected != null ? (%s) : (%s))\n", v.Name, ref, ref, injected, expr)
		envs = append(envs, "local.env_"+v.Name)
	}
	all := "[]"
	if len(envs) > 0 {
		all = fmt.Sprintf("flatten([%s])", strings.Join(envs, ", "))
	}
	fmt.Fprintf(&b, "\n  entries            = %s\n", all)
	b.WriteString("  lambda_environment = { for e in local.entries : e.name => e.value }\n")
	b.WriteString("  # Lambda allows 4 KB of environment variables, keys and values together.\n")
	b.WriteString("  lambda_environment_size = sum(concat([0], [for k, v in local.lambda_environment : length(k) + length(v)]))\n")
	b.WriteString("\n  # How each input is set (spec/IAC.md 4.5); docuconf lint reads it.\n  bindings = {\n")
	for _, v := range g.c.Vars {
		var arms []string
		for _, s := range lambdaSources(v) {
			arms = append(arms, fmt.Sprintf("var.%s.%s != null ? %q", v.Name, s, s))
		}
		fmt.Fprintf(&b, "    %s = { set = var.%s != null, source = var.%s == null ? null : (%s : null) }\n", v.Name, v.Name, v.Name, strings.Join(arms, " : "))
	}
	b.WriteString("  }\n}\n")
	return []byte(b.String())
}

func (g *gen) lambdaOutputs(digest string) []byte {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString(`
# The function's environment variables, a map for environment { variables = ... }.
output "lambda_environment" {
  description = "The function's environment variables, for its environment block (README.md)."
  value       = local.lambda_environment
`)
	for _, v := range g.c.Vars {
		for _, c := range g.lambdaChecks(v, "var."+v.Name) {
			fmt.Fprintf(&b, "\n  precondition {\n    condition     = %s\n    error_message = %s\n  }\n", c.cond, c.msg)
		}
	}
	fmt.Fprintf(&b, `
  precondition {
    condition     = local.lambda_environment_size <= %[1]d
    error_message = "the environment variables are ${local.lambda_environment_size} bytes, and Lambda allows %[1]d; move large values to files the function reads, or to Parameter Store"
  }
}

output "bindings" {
  description = "For each input, whether it is set and by which source. docuconf lint reads it."
  value       = local.bindings
}

output "contract" {
  description = "The contract this module was generated from."
  value = {
`, LambdaEnvLimit)
	fmt.Fprintf(&b, "    name   = %s\n    digest = %s\n  }\n}\n", hclString(g.c.Name), hclString(digest))
	return []byte(b.String())
}

func (g *gen) lambdaReadme() []byte {
	var b strings.Builder
	b.WriteString(MarkdownHeader)
	m := strings.ReplaceAll(g.c.Name, "-", "_") + "_config"
	fmt.Fprintf(&b, "# %s configuration module (AWS Lambda)\n\n", g.c.Name)
	fmt.Fprintf(&b, "Generated by `docuconf terraform -target lambda` from the `%s` contract. It takes one argument per input, checks it against the contract, and renders a Lambda function's environment variables. See [spec/IAC.md](https://github.com/Docuconf/docuconf-go/blob/main/spec/IAC.md).\n\n", g.c.Name)
	b.WriteString("| Input | Type | Required | Set with | Description |\n|---|---|---|---|---|\n")
	for _, v := range g.c.Vars {
		req := ""
		if boolean(v.Raw, "required") && !g.profileSupplies(v) {
			req = "yes"
		}
		typ := v.Type
		if v.Secret {
			typ += ", secret"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", v.Name, typ, req, "`"+strings.Join(lambdaSources(v), "`, `")+"`", strings.ReplaceAll(str(v.Raw, "description"), "|", `\|`))
	}
	if len(g.c.Files) > 0 || len(g.c.Overlays) > 0 {
		b.WriteString("\nLambda mounts no files, so the contract's optional file inputs and overlays are not part of this module.\n")
	}
	fmt.Fprintf(&b, "\nLambda resolves no secret references in its environment, so a secret input is `injected`: its `ref`, such as a Secrets Manager or Parameter Store ARN, becomes the variable's value, and the function reads the secret itself, through the AWS Parameters and Secrets Lambda Extension or an SDK, with its execution role allowed to. The secret's value never passes through Terraform. Lambda allows %d bytes of environment variables; the `lambda_environment` output refuses more.\n\n", LambdaEnvLimit)
	b.WriteString("## Usage\n\n```hcl\nmodule \"" + m + "\" {\n  source = \"./modules/" + g.c.Name + "-config\"\n")
	for _, v := range g.c.Vars {
		if !boolean(v.Raw, "required") || g.profileSupplies(v) {
			continue
		}
		if v.Secret {
			fmt.Fprintf(&b, "  %s = { injected = { provider = \"aws-parameters-and-secrets\", ref = aws_secretsmanager_secret.%s.arn } }\n", v.Name, strings.ToLower(v.Name))
		} else {
			fmt.Fprintf(&b, "  %s = { value = %s }\n", v.Name, exampleValue(v))
		}
	}
	fmt.Fprintf(&b, "}\n\nresource \"aws_lambda_function\" \"app\" {\n  # …\n  # The environment block's variables are in the plan's configuration, so\n  # this reference also binds the function to the module for docuconf lint.\n  environment {\n    variables = module.%s.lambda_environment\n  }\n}\n```\n", m)
	return []byte(b.String())
}
