package terraform

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

// The containerapps target renders an Azure Container App's secrets, its
// container's env, and the secret volume and mounts of its file inputs
// (spec/IAC.md 5). A variable is a literal, a Key Vault secret, or
// injected. A secret input becomes one of the app's secrets, named by the
// module, that references the Key Vault secret through a managed identity.
//
// Container Apps runs containers on Kubernetes, which expands $(VAR) in env
// values, so $ is doubled as on the kubernetes target. A file input is one
// secret in the app's Secret volume, mounted at its path with sub_path; a
// tls input, which is a directory of several files, cannot be. Overlays
// need a ConfigMap, which Container Apps has not, and a Secret volume
// cannot set file modes.

// A Key Vault secret ID, versioned or not, in any Azure cloud, and the
// identity that reads it: System, or a user-assigned identity's resource ID.
const (
	keyVaultSecretID = `^https://[0-9A-Za-z-]{3,24}\.vault\.[0-9a-z.-]+/secrets/[0-9A-Za-z-]{1,127}(/[0-9A-Fa-f]{32})?/?$`
	azureIdentity    = `^(System|/(?i)subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\.ManagedIdentity/userAssignedIdentities/[^/]+)$`
)

// ContainerAppsVolume holds every file input: a Secret volume mounts all the
// app's secrets, one file each, named by secret.
const ContainerAppsVolume = "dc-secrets"

var containerappsSecretType = `object({ key_vault_secret_id = string, identity = optional(string, "System") })`

func containerappsSources(v Var) []string { return ecsSources(v) } // the same: value, secret, injected

func containerappsFileSupported(f File) bool { return f.Type != "tls" }

// An app secret's name: lower case letters, digits and -, starting and
// ending with a letter or digit, at most 253 (Container Apps' rule).
var nonSecretName = regexp.MustCompile(`[^a-z0-9]+`)

func appSecretName(prefix, input string) string {
	s := strings.Trim(nonSecretName.ReplaceAllString(strings.ToLower(input), "-"), "-")
	return prefix + s
}

// VarSecretName and FileSecretName are the names of the app secrets the
// module adds for a secret input.
func VarSecretName(v Var) string   { return appSecretName("dc-", v.Name) }
func FileSecretName(f File) string { return appSecretName("dc-file-", f.Name) }

func generateContainerApps(c *Contract, opt Options) (map[string][]byte, error) {
	g := &gen{c: c, image: opt.Image}
	for _, v := range c.Vars {
		if err := g.checkSupported(v); err != nil {
			return nil, err
		}
	}
	var required []string
	for _, f := range c.Files {
		if containerappsFileSupported(f) {
			continue
		}
		if boolean(f.Raw, "required") {
			required = append(required, f.Name)
		} else if opt.Warn != nil {
			opt.Warn(fmt.Sprintf("%s: a tls input is a directory of several files, and a Container Apps secret holds one, so the containerapps module leaves this optional input out", f.Name))
		}
	}
	if len(required) > 0 {
		return nil, fmt.Errorf("a Container Apps secret holds one file, and the contract requires tls inputs %s, which are directories; deploy it on a target that can mount them (kubernetes)", strings.Join(required, ", "))
	}
	for _, o := range c.Overlays {
		if opt.Warn != nil {
			opt.Warn(fmt.Sprintf("overlay %s: Container Apps has no ConfigMaps, so the containerapps module leaves this overlay out; set its variables in the environment", o.Name))
		}
	}
	// Two inputs whose names fold to one secret name would share a secret.
	seen := map[string]string{}
	claim := func(secret, input string) error {
		if other, ok := seen[secret]; ok {
			return fmt.Errorf("inputs %s and %s would both be the Container Apps secret %s; rename one", other, input, secret)
		}
		seen[secret] = input
		return nil
	}
	for _, v := range c.Vars {
		if has(containerappsSources(v), "secret") {
			if err := claim(VarSecretName(v), v.Name); err != nil {
				return nil, err
			}
		}
	}
	for _, f := range g.containerappsFiles() {
		if err := claim(FileSecretName(f), f.Name); err != nil {
			return nil, err
		}
	}
	digest := Digest(c.JSON)
	meta, err := metaJSON(c, opt, digest)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"versions.tf":   hclwrite.Format([]byte(Header + "\nterraform {\n  # optional() attributes, check blocks and output preconditions.\n  required_version = \">= 1.5.0\"\n}\n")),
		"variables.tf":  hclwrite.Format(g.containerappsVariables()),
		"render.tf":     hclwrite.Format(g.containerappsRender()),
		"outputs.tf":    hclwrite.Format(g.containerappsOutputs(digest)),
		"docuconf.json": append(meta, '\n'),
		"README.md":     g.containerappsReadme(),
	}, nil
}

func (g *gen) containerappsFiles() []File {
	var out []File
	for _, f := range g.c.Files {
		if containerappsFileSupported(f) {
			out = append(out, f)
		}
	}
	return out
}

func (g *gen) containerappsVariables() []byte {
	var b strings.Builder
	b.WriteString(Header)
	for _, v := range g.c.Vars {
		fmt.Fprintf(&b, "\nvariable %q {\n", v.Name)
		fmt.Fprintf(&b, "  description = %s\n", hclString(describeVar(v)))
		b.WriteString("  type = object({\n")
		for _, s := range containerappsSources(v) {
			t := ecsSourceTypes["injected"]
			switch s {
			case "value":
				t = valueType(v)
			case "secret":
				t = containerappsSecretType
			}
			fmt.Fprintf(&b, "    %-8s = optional(%s)\n", s, t)
		}
		b.WriteString("  })\n")
		if boolean(v.Raw, "required") && !g.profileSupplies(v) {
			b.WriteString("  nullable = false\n")
		} else {
			b.WriteString("  default  = null\n")
		}
		for _, c := range g.containerappsChecks(v, "var."+v.Name) {
			fmt.Fprintf(&b, "\n  validation {\n    condition     = %s\n    error_message = %s\n  }\n", c.cond, c.msg)
		}
		b.WriteString("}\n")
		if d, ok := v.Raw["deprecated"].(map[string]any); ok {
			writeDeprecated(&b, v.Name, "var."+v.Name+" == null", d)
		}
	}
	for _, f := range g.containerappsFiles() {
		ref := "var." + f.Name
		fmt.Fprintf(&b, "\nvariable %q {\n", f.Name)
		fmt.Fprintf(&b, "  description = %s\n", hclString(describeFile(f)))
		fmt.Fprintf(&b, "  type = object({\n    secret   = optional(%s)\n    injected = optional(object({ provider = string }))\n  })\n", containerappsSecretType)
		if boolean(f.Raw, "required") {
			b.WriteString("  nullable = false\n")
		} else {
			b.WriteString("  default  = null\n")
		}
		for _, c := range g.containerappsFileChecks(f, ref) {
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

// keyVaultChecks validate a Key Vault reference: s is the attribute.
func keyVaultChecks(n, ref, s string) []check {
	return []check{
		{
			cond: guard(ref, s, fmt.Sprintf("can(regex(%s, %s.key_vault_secret_id))", hclString(keyVaultSecretID), s)),
			msg:  fmt.Sprintf(`"%s: secret.key_vault_secret_id ${jsonencode(%s.key_vault_secret_id)} is not a Key Vault secret ID, such as https://VAULT.vault.azure.net/secrets/NAME"`, n, s),
		},
		{
			cond: guard(ref, s, fmt.Sprintf("can(regex(%s, %s.identity))", hclString(azureIdentity), s)),
			msg:  fmt.Sprintf(`"%s: secret.identity ${jsonencode(%s.identity)} is not System or a user-assigned identity's resource ID"`, n, s),
		},
	}
}

// containerappsChecks are a variable argument's validations on the
// containerapps target, as cloudrunChecks are on cloudrun.
func (g *gen) containerappsChecks(v Var, ref string) []check {
	n := v.Name
	srcs := containerappsSources(v)
	var hint string
	switch {
	case v.Secret && !has(srcs, "secret"):
		hint = " (an indexed key set is one variable per key, which one secret cannot carry; Terraform ignores other attributes here)"
	case v.Secret:
		hint = " (a secret is never a literal value; Terraform ignores a value attribute here)"
	default:
		hint = " (Container Apps has no ConfigMaps or Downward API; Terraform ignores other attributes here)"
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
		out = append(out, keyVaultChecks(n, ref, ref+".secret")...)
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

func (g *gen) containerappsFileChecks(f File, ref string) []check {
	n := f.Name
	out := []check{
		{
			cond: fmt.Sprintf("%s == null ? true : length([for k, s in %s : k if s != null]) == 1", ref, ref),
			msg:  hclString(fmt.Sprintf("%s: set exactly one of secret or injected (Container Apps mounts files from Key Vault secrets; Terraform ignores other attributes here)", n)),
		},
		{
			cond: guard(ref, ref+".injected", fmt.Sprintf(`can(regex("^[a-z0-9]([-a-z0-9.]{0,61}[a-z0-9])?$", %s.injected.provider))`, ref)),
			msg:  hclString(n + ": injected.provider must name the injector as a lowercase label, such as bank-vaults"),
		},
	}
	out = append(out, keyVaultChecks(n, ref, ref+".secret")...)
	for i := range out {
		out[i].msg = safeMessage(out[i].msg)
	}
	return out
}

func (g *gen) containerappsRender() []byte {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString("\n# Each input's env entries, in its wire encoding (SPEC.md 5). Container Apps expands $(VAR) as the kubelet does, so $ is doubled.\nlocals {\n")
	// renderWire's entries carry value_from; Container Apps takes secret_name.
	aca := func(expr string) string {
		return fmt.Sprintf("[for e in %s : { name = e.name, value = e.value, secret_name = null }]", expr)
	}
	var envs, secrets []string
	for _, v := range g.c.Vars {
		ref := "var." + v.Name
		n := hclString(v.Name)
		injected := fmt.Sprintf(`%s.injected.ref == null ? [] : [{ name = %s, value = %s, secret_name = null }]`, ref, n, esc(ref+".injected.ref"))
		expr := "[]"
		switch srcs := containerappsSources(v); {
		case has(srcs, "secret"):
			sn := hclString(VarSecretName(v))
			expr = fmt.Sprintf("[{ name = %s, value = null, secret_name = %s }]", n, sn)
			fmt.Fprintf(&b, "  secret_%s = try(%s.secret, null) == null ? [] : [{ name = %s, key_vault_secret_id = %s.secret.key_vault_secret_id, identity = %s.secret.identity }]\n", v.Name, ref, sn, ref, ref)
			secrets = append(secrets, "local.secret_"+v.Name)
		case has(srcs, "value"):
			expr = aca(renderWire(v, ref+".value", esc))
		}
		fmt.Fprintf(&b, "  env_%s = %s == null ? [] : (%s.injected != null ? (%s) : (%s))\n", v.Name, ref, ref, injected, expr)
		envs = append(envs, "local.env_"+v.Name)
	}
	var mounts []string
	for _, f := range g.containerappsFiles() {
		ref := "var." + f.Name
		id := ident(f.Name)
		sn := hclString(FileSecretName(f))
		fmt.Fprintf(&b, "  file_secret_%s = try(%s.secret, null) == null ? [] : [{ name = %s, key_vault_secret_id = %s.secret.key_vault_secret_id, identity = %s.secret.identity }]\n", id, ref, sn, ref, ref)
		fmt.Fprintf(&b, "  file_volume_mounts_%s = length(local.file_secret_%s) == 0 ? [] : [{ name = %q, path = %q, sub_path = %s }]\n", id, id, ContainerAppsVolume, f.path(), sn)
		if pe := str(f.Raw, "pathEnv"); pe != "" {
			fmt.Fprintf(&b, "  file_env_%s = %s == null ? [] : [{ name = %s, value = %s, secret_name = null }]\n", id, ref, hclString(pe), hclString(strings.ReplaceAll(f.path(), "$", "$$")))
			envs = append(envs, "local.file_env_"+id)
		}
		secrets = append(secrets, "local.file_secret_"+id)
		mounts = append(mounts, "local.file_volume_mounts_"+id)
	}
	list := func(xs []string) string {
		if len(xs) == 0 {
			return "[]"
		}
		return fmt.Sprintf("flatten([%s])", strings.Join(xs, ", "))
	}
	fmt.Fprintf(&b, "\n  containerapp_env           = %s\n", list(envs))
	fmt.Fprintf(&b, "  containerapp_secrets       = %s\n", list(secrets))
	fmt.Fprintf(&b, "  containerapp_volume_mounts = %s\n", list(mounts))
	fmt.Fprintf(&b, "  containerapp_volumes       = length(local.containerapp_volume_mounts) == 0 ? [] : [{ name = %q, storage_type = \"Secret\" }]\n", ContainerAppsVolume)
	b.WriteString("\n  # How each input is set (spec/IAC.md 4.5); docuconf lint reads it.\n  bindings = {\n")
	for _, v := range g.c.Vars {
		var arms []string
		for _, s := range containerappsSources(v) {
			arms = append(arms, fmt.Sprintf("var.%s.%s != null ? %q", v.Name, s, s))
		}
		fmt.Fprintf(&b, "    %s = { set = var.%s != null, source = var.%s == null ? null : (%s : null) }\n", v.Name, v.Name, v.Name, strings.Join(arms, " : "))
	}
	for _, f := range g.containerappsFiles() {
		fmt.Fprintf(&b, "    %s = { set = var.%s != null, source = var.%s == null ? null : (var.%s.secret != null ? \"secret\" : \"injected\") }\n", hclString(f.Name), f.Name, f.Name, f.Name)
	}
	b.WriteString("  }\n}\n")
	return []byte(b.String())
}

func (g *gen) containerappsOutputs(digest string) []byte {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString(`
# The container's env entries, each { name, value, secret_name }: a literal
# value, or the name of one of the app's secrets. Use it in a dynamic "env" block (README.md).
output "containerapp_env" {
  description = "The container's env entries, in the contract's order."
  value       = local.containerapp_env
`)
	for _, v := range g.c.Vars {
		for _, c := range g.containerappsChecks(v, "var."+v.Name) {
			fmt.Fprintf(&b, "\n  precondition {\n    condition     = %s\n    error_message = %s\n  }\n", c.cond, c.msg)
		}
	}
	for _, c := range g.requiredIfChecks("containerapps", g.containerappsFiles(), false) {
		fmt.Fprintf(&b, "\n  precondition {\n    condition     = %s\n    error_message = %s\n  }\n", c.cond, c.msg)
	}
	b.WriteString(`}

# The app's secrets for secret inputs: each { name, key_vault_secret_id, identity }.
output "containerapp_secrets" {
  description = "The app's secrets, for a dynamic \"secret\" block."
  value       = local.containerapp_secrets
`)
	for _, f := range g.containerappsFiles() {
		for _, c := range g.containerappsFileChecks(f, "var."+f.Name) {
			fmt.Fprintf(&b, "\n  precondition {\n    condition     = %s\n    error_message = %s\n  }\n", c.cond, c.msg)
		}
	}
	b.WriteString(`}

output "containerapp_volumes" {
  description = "The template's volumes, each { name, storage_type }, for a dynamic \"volume\" block: one Secret volume when a file input is set."
  value       = local.containerapp_volumes
}

output "containerapp_volume_mounts" {
  description = "The container's volume mounts, each { name, path, sub_path }, for a dynamic \"volume_mounts\" block."
  value       = local.containerapp_volume_mounts
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

func (g *gen) containerappsReadme() []byte {
	var b strings.Builder
	b.WriteString(MarkdownHeader)
	m := strings.ReplaceAll(g.c.Name, "-", "_") + "_config"
	fmt.Fprintf(&b, "# %s configuration module (Azure Container Apps)\n\n", g.c.Name)
	fmt.Fprintf(&b, "Generated by `docuconf terraform -target containerapps` from the `%s` contract. It takes one argument per input, checks it against the contract, and renders a container app's secrets, its container's `env`, and the secret volume and mounts of its file inputs. See [spec/IAC.md](https://github.com/Docuconf/docuconf-go/blob/main/spec/IAC.md).\n\n", g.c.Name)
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
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", v.Name, typ, req, "`"+strings.Join(containerappsSources(v), "`, `")+"`", strings.ReplaceAll(str(v.Raw, "description"), "|", `\|`))
	}
	for _, f := range g.containerappsFiles() {
		req := ""
		if boolean(f.Raw, "required") {
			req = "yes"
		}
		if w := whenText(f.Raw); w != "" {
			req = "when " + w
		}
		fmt.Fprintf(&b, "| `%s` | %s file at `%s` | %s | `secret`, `injected` | %s |\n", f.Name, f.Type, f.path(), req, strings.ReplaceAll(str(f.Raw, "description"), "|", `\|`))
	}
	b.WriteString("\nA secret is a Key Vault secret ID, versioned or not, and the `identity` that reads it: `System` (the default) for the app's system-assigned identity, or a user-assigned identity's resource ID. That identity must be able to read the secret (the Key Vault Secrets User role). The module names the app secret it adds for each input (`dc-…`); add `containerapp_secrets` to the app's own secrets. A file input is one secret in a Secret volume, mounted at its path, and Container Apps sets its permissions; tls inputs, which are directories, and overlays, which need a ConfigMap, are not part of this module.\n\n")
	b.WriteString("## Usage\n\n```hcl\nmodule \"" + m + "\" {\n  source = \"./modules/" + g.c.Name + "-config\"\n")
	for _, v := range g.c.Vars {
		if !boolean(v.Raw, "required") || g.profileSupplies(v) {
			continue
		}
		if v.Secret {
			fmt.Fprintf(&b, "  %s = { secret = { key_vault_secret_id = azurerm_key_vault_secret.%s.versionless_id } }\n", v.Name, strings.ToLower(v.Name))
		} else {
			fmt.Fprintf(&b, "  %s = { value = %s }\n", v.Name, exampleValue(v))
		}
	}
	fmt.Fprintf(&b, `}

resource "azurerm_container_app" "app" {
  # …
  # The dynamic blocks are not in the plan's configuration, so this
  # reference is what binds the app to the module for docuconf lint.
  tags = { app = module.%[1]s.contract.name }
  identity {
    type = "SystemAssigned"
  }
  dynamic "secret" {
    for_each = module.%[1]s.containerapp_secrets
    content {
      name                = secret.value.name
      key_vault_secret_id = secret.value.key_vault_secret_id
      identity            = secret.value.identity
    }
  }
  template {
    container {
      image = var.image
      # …
      dynamic "env" {
        for_each = module.%[1]s.containerapp_env
        content {
          name        = env.value.name
          value       = env.value.value
          secret_name = env.value.secret_name
        }
      }
      dynamic "volume_mounts" {
        for_each = module.%[1]s.containerapp_volume_mounts
        content {
          name     = volume_mounts.value.name
          path     = volume_mounts.value.path
          sub_path = volume_mounts.value.sub_path
        }
      }
    }
    dynamic "volume" {
      for_each = module.%[1]s.containerapp_volumes
      content {
        name         = volume.value.name
        storage_type = volume.value.storage_type
      }
    }
  }
}
`+"```\n\nA system-assigned identity is created with the app, so it cannot be granted the secrets before the app's first revision reads them; with Key Vault references, a user-assigned identity granted in advance avoids a failed first deploy.\n", m)
	return []byte(b.String())
}
