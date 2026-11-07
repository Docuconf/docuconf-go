package terraform

import (
	"fmt"
	"strings"
)

// The helm_values output (spec/IAC.md 4.5): the module's arguments in the
// vocabulary of the Helm library chart's docuconf: values, for a
// helm_release that uses the chart instead of a kubernetes_deployment_v1.
// The chart's values.schema.json allows no attribute it does not declare,
// so an attribute the argument left null is left out, not set to null.

// compact is an HCL object of the given attributes without the null ones.
func compact(attrs [][2]string) string {
	parts := make([]string, len(attrs))
	for i, a := range attrs {
		parts[i] = a[0] + " = " + a[1]
	}
	return "{ for k, x in { " + strings.Join(parts, ", ") + " } : k => x if x != null }"
}

// nonEmpty is compact for maps that default to {}: those are left out too.
func nonEmpty(attrs [][2]string) string {
	parts := make([]string, len(attrs))
	for i, a := range attrs {
		parts[i] = a[0] + " = " + a[1]
	}
	return "{ for k, x in { " + strings.Join(parts, ", ") + " } : k => x if x != null && try(length(x) > 0, true) }"
}

// helmInjected is an injected source in the chart's words.
func helmInjected(src string, ref bool) string {
	attrs := [][2]string{{"provider", src + ".provider"}}
	if ref {
		attrs = append(attrs, [2]string{"ref", src + ".ref"})
	}
	attrs = append(attrs, [2]string{"podAnnotations", src + ".pod_annotations"}, [2]string{"podLabels", src + ".pod_labels"})
	return nonEmpty(attrs)
}

// helmVarSource is one source of a variable argument, as the chart's
// value: the literal itself, or a source object. It is null when the
// argument does not use the source.
func helmVarSource(ref, s string) string {
	src := ref + "." + s
	var obj string
	switch s {
	case "value":
		return src
	case "secret":
		obj = "{ secretKeyRef = " + src + " }"
	case "config_map":
		obj = "{ configMapKeyRef = " + src + " }"
	case "field":
		obj = "{ fieldRef = { fieldPath = " + src + ".field_path } }"
	case "resource_field":
		obj = "{ resourceFieldRef = " + compact([][2]string{{"resource", src + ".resource"}, {"divisor", src + ".divisor"}, {"containerName", src + ".container_name"}}) + " }"
	case "injected":
		obj = "{ injected = " + helmInjected(src, true) + " }"
	default:
		panic("helm: unknown source " + s)
	}
	return src + " == null ? null : " + obj
}

// helmFileSource is one source of a file argument, as the chart's source
// object, or null.
func helmFileSource(ref, s string) string {
	src := ref + "." + s
	var obj string
	switch s {
	case "inline":
		obj = "{ inline = " + src + " }"
	case "config_map":
		obj = "{ configMap = " + src + " }"
	case "secret":
		obj = "{ secret = " + compact([][2]string{{"name", src + ".name"}, {"key", "try(" + src + ".key, null)"}}) + " }"
	case "certificate":
		key := src + ".private_key == null ? null : " + compact([][2]string{{"algorithm", src + ".private_key.algorithm"}})
		obj = "{ certificate = " + compact([][2]string{{"name", src + ".name"}, {"secretName", src + ".secret_name"}, {"dnsNames", src + ".dns_names"},
			{"privateKey", "(" + key + ")"}, {"duration", src + ".duration"}, {"renewBefore", src + ".renew_before"}}) + " }"
	case "csi":
		obj = "{ csi = " + compact([][2]string{{"driver", src + ".driver"}, {"secretProviderClass", src + ".secret_provider_class"}}) + " }"
	case "image":
		obj = "{ image = " + compact([][2]string{{"reference", src + ".reference"}, {"pullPolicy", src + ".pull_policy"}}) + " }"
	case "injected":
		obj = "{ injected = " + helmInjected(src, false) + " }"
	default:
		panic("helm: unknown file source " + s)
	}
	return src + " == null ? null : " + obj
}

// firstSet picks the source an argument uses: the arguments' validations
// let exactly one be set. The candidates have different types (a value
// and an object), which a conditional cannot return, so they go through
// a tuple.
func firstSet(ref string, candidates []string) string {
	return fmt.Sprintf("%s == null ? null : try([for c in [%s] : c if c != null][0], null)", ref, strings.Join(candidates, ", "))
}

// helm writes helm.tf: the helm_values output and its locals.
func (g *gen) helm(pre func(*strings.Builder, []check)) []byte {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString("\n# The arguments in the vocabulary of the Helm library chart's values (helm/README.md).\nlocals {\n")
	var vars [][2]string
	for _, v := range g.c.Vars {
		ref := "var." + v.Name
		var cs []string
		for _, s := range g.sources(v) {
			cs = append(cs, helmVarSource(ref, s))
		}
		fmt.Fprintf(&b, "  helm_value_%s = %s\n", v.Name, firstSet(ref, cs))
		vars = append(vars, [2]string{hclKey(v.Name), "local.helm_value_" + v.Name})
	}
	var files [][2]string
	for _, f := range g.c.Files {
		ref := "var." + f.Name
		var cs []string
		for _, s := range fileSources(f) {
			cs = append(cs, helmFileSource(ref, s))
		}
		fmt.Fprintf(&b, "  helm_file_%s = %s\n", ident(f.Name), firstSet(ref, cs))
		files = append(files, [2]string{hclKey(f.Name), "local.helm_file_" + ident(f.Name)})
	}
	// Pod metadata every injected input shares goes beside the values.
	meta := nonEmpty([][2]string{{"podAnnotations", "var.docuconf_options.pod_annotations"}, {"podLabels", "var.docuconf_options.pod_labels"}})
	fmt.Fprintf(&b, "  helm_values = merge(%s, %s)\n", compact(vars), meta)
	out := [][2]string{{"values", "local.helm_values"}}
	if len(files) > 0 {
		fmt.Fprintf(&b, "  helm_files = %s\n", compact(files))
		out = append(out, [2]string{"files", "local.helm_files"})
	}
	if len(g.c.Overlays) > 0 {
		var ovs [][2]string
		for _, o := range g.c.Overlays {
			ovs = append(ovs, [2]string{hclKey(o.Name), "var.overlay_" + o.Name})
		}
		fmt.Fprintf(&b, "  helm_overlays = %s\n", compact(ovs))
		out = append(out, [2]string{"overlays", "local.helm_overlays"})
	}
	b.WriteString("}\n")
	b.WriteString(`
# The docuconf: values of the Helm library chart, for a helm_release whose chart
# depends on it: values = [yamlencode({ docuconf = module.<name>.helm_values })].
# The chart renders and checks them again, with its values.schema.json.
output "helm_values" {
  description = "The arguments as the docuconf Helm library chart's docuconf: values."
  value       = {
`)
	for _, o := range out {
		fmt.Fprintf(&b, "    %s = %s\n", o[0], o[1])
	}
	b.WriteString("  }\n")
	var cs []check
	for _, v := range g.c.Vars {
		cs = append(cs, g.checks(v, "var."+v.Name)...)
	}
	cs = append(cs, g.suppliedChecks()...)
	for _, f := range g.c.Files {
		cs = append(cs, g.fileChecks(f, "var."+f.Name)...)
	}
	for _, o := range g.c.Overlays {
		cs = append(cs, g.overlayChecks(o)...)
	}
	pre(&b, cs)
	b.WriteString("}\n")
	return []byte(b.String())
}

// hclKey is an object key: quoted unless it is an identifier.
func hclKey(name string) string {
	for i, r := range name {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return hclString(name)
		}
	}
	return name
}
