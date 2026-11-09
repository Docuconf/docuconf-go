package terraform

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

// The cloudrun target renders a Cloud Run (v2) container's env, and the
// secret volumes and mounts of its file inputs (spec/IAC.md 5). A variable
// is a literal, a Secret Manager secret version, or injected. A file input
// comes from a Secret Manager secret, one file per secret, so a tls input,
// which is a directory of several files, cannot be mounted. Overlays need
// a ConfigMap, which Cloud Run has not.

// A Secret Manager secret, by ID or full resource name, and its version.
const (
	gcpSecret        = `^(projects/[a-z][a-z0-9-]{4,28}[a-z0-9]/secrets/)?[A-Za-z0-9_-]{1,255}$`
	gcpSecretVersion = `^(latest|[1-9][0-9]*)$`
)

var cloudrunSecretType = `object({ secret = string, version = optional(string, "latest") })`

func cloudrunSources(v Var) []string { return ecsSources(v) } // the same: value, secret, injected

func cloudrunFileSupported(f File) bool { return f.Type != "tls" }

func generateCloudRun(c *Contract, opt Options) (map[string][]byte, error) {
	g := &gen{c: c, image: opt.Image}
	for _, v := range c.Vars {
		if err := g.checkSupported(v); err != nil {
			return nil, err
		}
	}
	var required []string
	for _, f := range c.Files {
		if cloudrunFileSupported(f) {
			continue
		}
		if boolean(f.Raw, "required") {
			required = append(required, f.Name)
		} else if opt.Warn != nil {
			opt.Warn(fmt.Sprintf("%s: a tls input is a directory of several files, and a Cloud Run secret volume holds one, so the cloudrun module leaves this optional input out", f.Name))
		}
	}
	if len(required) > 0 {
		return nil, fmt.Errorf("a Cloud Run secret volume holds one file, and the contract requires tls inputs %s, which are directories; deploy it on a target that can mount them (kubernetes)", strings.Join(required, ", "))
	}
	for _, o := range c.Overlays {
		if opt.Warn != nil {
			opt.Warn(fmt.Sprintf("overlay %s: Cloud Run has no ConfigMaps, so the cloudrun module leaves this overlay out; set its variables in the environment", o.Name))
		}
	}
	digest := Digest(c.JSON)
	meta, err := metaJSON(c, opt, digest)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"versions.tf":   hclwrite.Format([]byte(Header + "\nterraform {\n  # optional() attributes, check blocks and output preconditions.\n  required_version = \">= 1.5.0\"\n}\n")),
		"variables.tf":  hclwrite.Format(g.cloudrunVariables()),
		"render.tf":     hclwrite.Format(g.cloudrunRender()),
		"outputs.tf":    hclwrite.Format(g.cloudrunOutputs(digest)),
		"docuconf.json": append(meta, '\n'),
		"README.md":     g.cloudrunReadme(),
	}, nil
}

func (g *gen) cloudrunFiles() []File {
	var out []File
	for _, f := range g.c.Files {
		if cloudrunFileSupported(f) {
			out = append(out, f)
		}
	}
	return out
}

func (g *gen) cloudrunVariables() []byte {
	var b strings.Builder
	b.WriteString(Header)
	for _, v := range g.c.Vars {
		fmt.Fprintf(&b, "\nvariable %q {\n", v.Name)
		fmt.Fprintf(&b, "  description = %s\n", hclString(describeVar(v)))
		b.WriteString("  type = object({\n")
		for _, s := range cloudrunSources(v) {
			t := ecsSourceTypes["injected"]
			switch s {
			case "value":
				t = valueType(v)
			case "secret":
				t = cloudrunSecretType
			}
			fmt.Fprintf(&b, "    %-8s = optional(%s)\n", s, t)
		}
		b.WriteString("  })\n")
		if boolean(v.Raw, "required") && !g.profileSupplies(v) {
			b.WriteString("  nullable = false\n")
		} else {
			b.WriteString("  default  = null\n")
		}
		for _, c := range g.cloudrunChecks(v, "var."+v.Name) {
			fmt.Fprintf(&b, "\n  validation {\n    condition     = %s\n    error_message = %s\n  }\n", c.cond, c.msg)
		}
		b.WriteString("}\n")
		if d, ok := v.Raw["deprecated"].(map[string]any); ok {
			writeDeprecated(&b, v.Name, "var."+v.Name+" == null", d)
		}
	}
	for _, f := range g.cloudrunFiles() {
		ref := "var." + f.Name
		fmt.Fprintf(&b, "\nvariable %q {\n", f.Name)
		fmt.Fprintf(&b, "  description = %s\n", hclString(describeFile(f)))
		fmt.Fprintf(&b, "  type = object({\n    secret   = optional(%s)\n    injected = optional(object({ provider = string }))\n  })\n", cloudrunSecretType)
		if boolean(f.Raw, "required") {
			b.WriteString("  nullable = false\n")
		} else {
			b.WriteString("  default  = null\n")
		}
		for _, c := range g.cloudrunFileChecks(f, ref) {
			fmt.Fprintf(&b, "\n  validation {\n    condition     = %s\n    error_message = %s\n  }\n", c.cond, c.msg)
		}
		b.WriteString("}\n")
		if d, ok := f.Raw["deprecated"].(map[string]any); ok {
			writeDeprecated(&b, f.Name, ref+" == null", d)
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

// secretChecks validate a Secret Manager reference: s is the attribute.
func secretChecks(n, ref, s string) []check {
	return []check{
		{
			cond: guard(ref, s, fmt.Sprintf("can(regex(%s, %s.secret))", hclString(gcpSecret), s)),
			msg:  fmt.Sprintf(`"%s: secret.secret ${jsonencode(%s.secret)} is not a Secret Manager secret ID or projects/PROJECT/secrets/SECRET"`, n, s),
		},
		{
			cond: guard(ref, s, fmt.Sprintf("can(regex(%s, %s.version))", hclString(gcpSecretVersion), s)),
			msg:  fmt.Sprintf(`"%s: secret.version ${jsonencode(%s.version)} is not latest or a version number"`, n, s),
		},
	}
}

// cloudrunChecks are a variable argument's validations on the cloudrun
// target, as ecsChecks are on ecs.
func (g *gen) cloudrunChecks(v Var, ref string) []check {
	n := v.Name
	srcs := cloudrunSources(v)
	var hint string
	switch {
	case v.Secret && !has(srcs, "secret"):
		hint = " (an indexed key set is one variable per key, which one secret version cannot carry; Terraform ignores other attributes here)"
	case v.Secret:
		hint = " (a secret is never a literal value; Terraform ignores a value attribute here)"
	default:
		hint = " (Cloud Run has no ConfigMaps or Downward API; Terraform ignores other attributes here)"
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
		out = append(out, secretChecks(n, ref, ref+".secret")...)
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

func (g *gen) cloudrunFileChecks(f File, ref string) []check {
	n := f.Name
	out := []check{
		{
			cond: fmt.Sprintf("%s == null ? true : length([for k, s in %s : k if s != null]) == 1", ref, ref),
			msg:  hclString(fmt.Sprintf("%s: set exactly one of secret or injected (Cloud Run mounts files from Secret Manager; Terraform ignores other attributes here)", n)),
		},
		{
			cond: guard(ref, ref+".injected", fmt.Sprintf(`can(regex("^[a-z0-9]([-a-z0-9.]{0,61}[a-z0-9])?$", %s.injected.provider))`, ref)),
			msg:  hclString(n + ": injected.provider must name the injector as a lowercase label, such as bank-vaults"),
		},
	}
	out = append(out, secretChecks(n, ref, ref+".secret")...)
	for i := range out {
		out[i].msg = safeMessage(out[i].msg)
	}
	return out
}

func (g *gen) cloudrunRender() []byte {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString("\n# Each input's env entries, in its wire encoding (SPEC.md 5). Cloud Run does not expand $(VAR), so $ is not escaped.\nlocals {\n")
	noEsc := func(x string) string { return x }
	var envs []string
	for _, v := range g.c.Vars {
		ref := "var." + v.Name
		n := hclString(v.Name)
		injected := fmt.Sprintf(`%s.injected.ref == null ? [] : [{ name = %s, value = %s.injected.ref, value_from = null }]`, ref, n, ref)
		expr := "[]"
		switch srcs := cloudrunSources(v); {
		case has(srcs, "secret"):
			expr = fmt.Sprintf("[{ name = %s, value = null, value_from = { secret_key_ref = { secret = %s.secret.secret, version = %s.secret.version } } }]", n, ref, ref)
		case has(srcs, "value"):
			expr = renderWire(v, ref+".value", noEsc)
		}
		fmt.Fprintf(&b, "  env_%s = %s == null ? [] : (%s.injected != null ? (%s) : (%s))\n", v.Name, ref, ref, injected, expr)
		envs = append(envs, "local.env_"+v.Name)
	}
	var vols, mounts []string
	for _, f := range g.cloudrunFiles() {
		ref := "var." + f.Name
		id := ident(f.Name)
		mode := 292 // 0444
		if f.Secret {
			mode = 256 // 0400
		}
		fmt.Fprintf(&b, "  file_volumes_%s = %s == null ? [] : (%s.secret == null ? [] : [{ name = %q, secret = { secret = %s.secret.secret, default_mode = %d, items = [{ version = %s.secret.version, path = %q }] } }])\n",
			id, ref, ref, "dc-"+f.Name, ref, mode, ref, f.fileName())
		fmt.Fprintf(&b, "  file_volume_mounts_%s = length(local.file_volumes_%s) == 0 ? [] : [{ name = %q, mount_path = %q }]\n", id, id, "dc-"+f.Name, f.mountDir())
		if pe := str(f.Raw, "pathEnv"); pe != "" {
			fmt.Fprintf(&b, "  file_env_%s = %s == null ? [] : [{ name = %s, value = %s, value_from = null }]\n", id, ref, hclString(pe), hclString(f.path()))
			envs = append(envs, "local.file_env_"+id)
		}
		vols = append(vols, "local.file_volumes_"+id)
		mounts = append(mounts, "local.file_volume_mounts_"+id)
	}
	list := func(xs []string) string {
		if len(xs) == 0 {
			return "[]"
		}
		return fmt.Sprintf("flatten([%s])", strings.Join(xs, ", "))
	}
	fmt.Fprintf(&b, "\n  cloudrun_env           = %s\n", list(envs))
	fmt.Fprintf(&b, "  cloudrun_volumes       = %s\n", list(vols))
	fmt.Fprintf(&b, "  cloudrun_volume_mounts = %s\n", list(mounts))
	b.WriteString("\n  # How each input is set (spec/IAC.md 4.5); docuconf lint reads it.\n  bindings = {\n")
	for _, v := range g.c.Vars {
		var arms []string
		for _, s := range cloudrunSources(v) {
			arms = append(arms, fmt.Sprintf("var.%s.%s != null ? %q", v.Name, s, s))
		}
		fmt.Fprintf(&b, "    %s = { set = var.%s != null, source = var.%s == null ? null : (%s : null) }\n", v.Name, v.Name, v.Name, strings.Join(arms, " : "))
	}
	for _, f := range g.cloudrunFiles() {
		fmt.Fprintf(&b, "    %s = { set = var.%s != null, source = var.%s == null ? null : (var.%s.secret != null ? \"secret\" : \"injected\") }\n", hclString(f.Name), f.Name, f.Name, f.Name)
	}
	b.WriteString("  }\n}\n")
	return []byte(b.String())
}

func (g *gen) cloudrunOutputs(digest string) []byte {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString(`
# The container's env entries, each { name, value, value_from } with value_from a
# { secret_key_ref = { secret, version } } or null. Use it in a dynamic "env" block (README.md).
output "cloudrun_env" {
  description = "The container's env entries, in the contract's order."
  value       = local.cloudrun_env
`)
	for _, v := range g.c.Vars {
		for _, c := range g.cloudrunChecks(v, "var."+v.Name) {
			fmt.Fprintf(&b, "\n  precondition {\n    condition     = %s\n    error_message = %s\n  }\n", c.cond, c.msg)
		}
	}
	for _, c := range g.requiredIfChecks("cloudrun", g.cloudrunFiles(), false) {
		fmt.Fprintf(&b, "\n  precondition {\n    condition     = %s\n    error_message = %s\n  }\n", c.cond, c.msg)
	}
	b.WriteString("}\n\n# The revision's secret volumes for file inputs: each { name, secret = { secret, default_mode, items } }.\noutput \"cloudrun_volumes\" {\n  description = \"The revision template's volumes, for a dynamic \\\"volumes\\\" block.\"\n  value       = local.cloudrun_volumes\n")
	for _, f := range g.cloudrunFiles() {
		for _, c := range g.cloudrunFileChecks(f, "var."+f.Name) {
			fmt.Fprintf(&b, "\n  precondition {\n    condition     = %s\n    error_message = %s\n  }\n", c.cond, c.msg)
		}
	}
	b.WriteString(`}

output "cloudrun_volume_mounts" {
  description = "The container's volume mounts, each { name, mount_path }, for a dynamic \"volume_mounts\" block."
  value       = local.cloudrun_volume_mounts
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

func (g *gen) cloudrunReadme() []byte {
	var b strings.Builder
	b.WriteString(MarkdownHeader)
	m := strings.ReplaceAll(g.c.Name, "-", "_") + "_config"
	fmt.Fprintf(&b, "# %s configuration module (Cloud Run)\n\n", g.c.Name)
	fmt.Fprintf(&b, "Generated by `docuconf terraform -target cloudrun` from the `%s` contract. It takes one argument per input, checks it against the contract, and renders a Cloud Run container's `env`, and the secret volumes and mounts of its file inputs. See [spec/IAC.md](https://github.com/Docuconf/docuconf-go/blob/main/spec/IAC.md).\n\n", g.c.Name)
	b.WriteString("| Input | Type | Required | Set with | Description |\n|---|---|---|---|---|\n")
	for _, v := range g.c.Vars {
		req := ""
		if boolean(v.Raw, "required") && !g.profileSupplies(v) {
			req = "yes"
		}
		if w := whenText(v.Raw); w != "" && !g.profileSupplies(v) {
			req = "when " + w
		}
		typ := v.Type
		if v.Secret {
			typ += ", secret"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", v.Name, typ, req, "`"+strings.Join(cloudrunSources(v), "`, `")+"`", strings.ReplaceAll(str(v.Raw, "description"), "|", `\|`))
	}
	for _, f := range g.cloudrunFiles() {
		req := ""
		if boolean(f.Raw, "required") {
			req = "yes"
		}
		if w := whenText(f.Raw); w != "" {
			req = "when " + w
		}
		fmt.Fprintf(&b, "| `%s` | %s file at `%s` | %s | `secret`, `injected` | %s |\n", f.Name, f.Type, f.path(), req, strings.ReplaceAll(str(f.Raw, "description"), "|", `\|`))
	}
	b.WriteString("\nA secret is a Secret Manager secret, by ID or as `projects/PROJECT/secrets/SECRET`, and a `version`, `latest` by default. The service account the revision runs as must be allowed to access each (`roles/secretmanager.secretAccessor`). A file input is mounted from a secret, one file per secret; tls inputs, which are directories, and overlays, which need a ConfigMap, are not part of this module.\n\n")
	b.WriteString("## Usage\n\n```hcl\nmodule \"" + m + "\" {\n  source = \"./modules/" + g.c.Name + "-config\"\n")
	for _, v := range g.c.Vars {
		if !boolean(v.Raw, "required") || g.profileSupplies(v) {
			continue
		}
		if v.Secret {
			fmt.Fprintf(&b, "  %s = { secret = { secret = google_secret_manager_secret.%s.secret_id } }\n", v.Name, strings.ToLower(v.Name))
		} else {
			fmt.Fprintf(&b, "  %s = { value = %s }\n", v.Name, exampleValue(v))
		}
	}
	fmt.Fprintf(&b, `}

resource "google_cloud_run_v2_service" "app" {
  # …
  template {
    containers {
      image = var.image
      dynamic "env" {
        for_each = module.%[1]s.cloudrun_env
        content {
          name  = env.value.name
          value = env.value.value
          dynamic "value_source" {
            for_each = env.value.value_from == null ? [] : [env.value.value_from]
            content {
              secret_key_ref {
                secret  = value_source.value.secret_key_ref.secret
                version = value_source.value.secret_key_ref.version
              }
            }
          }
        }
      }
      dynamic "volume_mounts" {
        for_each = module.%[1]s.cloudrun_volume_mounts
        content {
          name       = volume_mounts.value.name
          mount_path = volume_mounts.value.mount_path
        }
      }
    }
    dynamic "volumes" {
      for_each = module.%[1]s.cloudrun_volumes
      content {
        name = volumes.value.name
        secret {
          secret       = volumes.value.secret.secret
          default_mode = volumes.value.secret.default_mode
          dynamic "items" {
            for_each = volumes.value.secret.items
            content {
              version = items.value.version
              path    = items.value.path
            }
          }
        }
      }
    }
  }
}
`+"```\n", m)
	return []byte(b.String())
}
