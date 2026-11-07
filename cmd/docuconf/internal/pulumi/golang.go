package pulumi

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"go/format"
	"regexp"
	"strings"
	"unicode"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/terraform"
)

// The runtime every Go component shares, and each target's part of it.
var (
	//go:embed gocomp/runtime.go.tmpl
	runtimeGo string
	//go:embed gocomp/kubernetes.go.tmpl
	kubernetesGo string
	//go:embed gocomp/targets.go.tmpl
	targetsGo string
	//go:embed gocomp/ecs.go.tmpl
	ecsGo string
	//go:embed gocomp/cloudrun.go.tmpl
	cloudrunGo string
	//go:embed gocomp/containerapps.go.tmpl
	containerappsGo string
	//go:embed gocomp/lambda.go.tmpl
	lambdaGo string
)

// Versions of the modules a generated Go component imports.
const (
	goSDKModule    = "github.com/docuconf/docuconf-go"
	goPulumiModule = "github.com/pulumi/pulumi/sdk/v3"
	goK8sModule    = "github.com/pulumi/pulumi-kubernetes/sdk/v4"
)

var pkgRe = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// goPackage is a package name for the component: the one given, or the
// contract's name, lowercased, without separators, then "config".
func goPackage(given, contract string) (string, error) {
	if given != "" {
		if !pkgRe.MatchString(given) {
			return "", fmt.Errorf("-package %q: want a Go package name of lowercase letters and digits", given)
		}
		return given, nil
	}
	var b strings.Builder
	for _, r := range strings.ToLower(contract) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	p := b.String() + "config"
	if !pkgRe.MatchString(p) {
		p = "service" + p
	}
	return p, nil
}

// initialisms are written in capitals in a Go name ("serving-tls" is ServingTLS).
var initialisms = map[string]string{"api": "API", "ca": "CA", "db": "DB", "dns": "DNS", "http": "HTTP", "https": "HTTPS", "id": "ID", "json": "JSON", "tls": "TLS", "toml": "TOML", "url": "URL", "uri": "URI", "yaml": "YAML"}

// goField is a file input's field: its name in Go's MixedCaps. A
// variable's field keeps its environment name, which is already exported.
func goField(name string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		if up, ok := initialisms[part]; ok {
			b.WriteString(up)
			continue
		}
		r := []rune(part)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	return b.String()
}

func goVarType(v terraform.Var) string {
	if v.Secret {
		return "SecretVar"
	}
	switch v.Type {
	case "string":
		return "StringVar"
	case "url", "enum", "duration":
		return "TextVar"
	case "int":
		return "IntVar"
	case "float":
		return "FloatVar"
	case "bool":
		return "BoolVar"
	case "list":
		return "ListVar"
	case "json":
		return "JSONVar"
	}
	return "StringVar"
}

func goFileType(f terraform.File) string {
	switch {
	case f.Type == "tls":
		return "TLSFile"
	case f.Secret:
		return "SecretFileInput"
	case f.Type == "config":
		return "ConfigFile"
	case f.Type == "binary":
		return "BinaryFile"
	}
	return "TextFile"
}

func goComment(b *strings.Builder, indent, text string) {
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		fmt.Fprintf(b, "%s// %s\n", indent, line)
	}
}

func generateGo(c *terraform.Contract, opt Options, meta []byte, digest string) (map[string][]byte, error) {
	pkg, err := goPackage(opt.Package, c.Name)
	if err != nil {
		return nil, err
	}
	g := &gen{c: c, class: className(c.Name), target: opt.Target}
	if err := g.targetFiles(nil); err != nil {
		return nil, err
	}
	if g.target != "kubernetes" {
		return g.generateGoTarget(pkg, meta, digest)
	}
	seen := map[string]string{}
	field := func(input, f string) error {
		if other, ok := seen[f]; ok {
			return fmt.Errorf("%s and %s are both %s in Go; rename one", other, input, f)
		}
		seen[f] = input
		return nil
	}
	for _, v := range c.Vars {
		if err := field(v.Name, v.Name); err != nil {
			return nil, err
		}
	}
	for _, f := range c.Files {
		if err := field(f.Name, goField(f.Name)); err != nil {
			return nil, err
		}
	}
	for _, reserved := range []string{"PodMetadata", "OverlayValues"} {
		if other, ok := seen[reserved]; ok {
			return nil, fmt.Errorf("%s is %s in Go, which the component's own argument uses; rename it", other, reserved)
		}
	}

	var b strings.Builder
	b.WriteString(Header)
	fmt.Fprintf(&b, "\n// Package %s is the %s contract as a Pulumi component for the kubernetes\n// target (spec/IAC.md section 6). See README.md.\npackage %s\n\n", pkg, c.Name, pkg)
	b.WriteString("import (\n\t_ \"embed\"\n\n\tcorev1 \"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1\"\n\t\"github.com/pulumi/pulumi/sdk/v3/go/pulumi\"\n)\n\n")
	b.WriteString("// contractJSON is the contract this component was generated from.\n//\n//go:embed contract.json\nvar contractJSON []byte\n\n")
	fmt.Fprintf(&b, "// Digest is the sha256 of the contract's compact JSON.\nconst Digest = %q\n\n", digest)

	fmt.Fprintf(&b, "// Args are the arguments of %s: one per input of the %s contract.\n", g.class, c.Name)
	b.WriteString("// A nil field is not set. Each field accepts only the sources its input\n// allows: a secret has no literal.\n")
	fmt.Fprintf(&b, "type Args struct {\n")
	for _, v := range c.Vars {
		goComment(&b, "\t", describe(v.Raw))
		fmt.Fprintf(&b, "\t%s %s\n", v.Name, goVarType(v))
	}
	for _, f := range c.Files {
		goComment(&b, "\t", describe(f.Raw)+" ("+str(f.Raw, "path")+")")
		fmt.Fprintf(&b, "\t%s %s\n", goField(f.Name), goFileType(f))
	}
	b.WriteString("\t// PodMetadata is pod annotations and labels for the workload, beside the\n\t// ones injected inputs ask for.\n\tPodMetadata *PodMetadata\n")
	if len(c.Overlays) > 0 {
		var names []string
		for _, o := range c.Overlays {
			names = append(names, o.Name)
		}
		fmt.Fprintf(&b, "\t// OverlayValues are the values to write into the contract's overlay\n\t// files (%s), by overlay, then by variable: literals, never Outputs.\n\tOverlayValues map[string]map[string]any\n", strings.Join(names, ", "))
	}
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, `// %[1]s is the %[2]s contract's inputs, checked and rendered for a
// Kubernetes workload. Put KubernetesEnv, KubernetesVolumes and
// KubernetesVolumeMounts in the container, PodAnnotations and PodLabels on
// the pod template (never the workload), and declare a ConfigMap for each
// of KubernetesConfigMaps.
type %[1]s struct {
	pulumi.ResourceState

	// KubernetesEnv is the container's env entries, in the contract's order.
	KubernetesEnv corev1.EnvVarArrayOutput
	// KubernetesVolumes is the pod's volumes for file inputs and overlays.
	KubernetesVolumes corev1.VolumeArrayOutput
	// KubernetesVolumeMounts is the container's volume mounts.
	KubernetesVolumeMounts corev1.VolumeMountArrayOutput
	// KubernetesConfigMaps is the ConfigMaps for inline file content and
	// overlays, known at construction: inline content and overlays are
	// literals.
	KubernetesConfigMaps []ConfigMapSpec
	// RestartTriggers is the ConfigMaps, Secrets and SecretProviderClasses,
	// each {kind, name}, whose change must restart the pods.
	RestartTriggers pulumi.ArrayOutput
	// PodAnnotations is the annotations for the pod template's metadata.
	PodAnnotations pulumi.StringMapOutput
	// PodLabels is the labels for the pod template's metadata.
	PodLabels pulumi.StringMapOutput
	// Bindings says, for each input, whether it is set and by which source.
	Bindings map[string]Binding
	// Checked resolves once every value has passed its checks, and fails
	// with a *ConfigViolation otherwise.
	Checked pulumi.BoolOutput
}

// New%[1]s checks the arguments and renders them. Arguments that break the
// contract return a *ConfigViolation listing every problem; a value that is
// an Output is checked when it resolves, and then fails every output.
func New%[1]s(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*%[1]s, error) {
	if args == nil {
		args = &Args{}
	}
	c := &%[1]s{}
	in := inputs{}
`, g.class, c.Name, "docuconf:"+c.Name+":Config")
	for _, v := range c.Vars {
		fmt.Fprintf(&b, "\tin.set(%q, args.%s)\n", v.Name, v.Name)
	}
	for _, f := range c.Files {
		fmt.Fprintf(&b, "\tin.set(%q, args.%s)\n", f.Name, goField(f.Name))
	}
	overlays := "nil"
	if len(c.Overlays) > 0 {
		overlays = "args.OverlayValues"
	}
	fmt.Fprintf(&b, `	// The arguments are the component's inputs, so that a preview shows
	// them and docuconf lint pulumi can check them.
	extra := map[string]any{}
	if args.PodMetadata != nil {
		extra["podMetadata"] = map[string]any{"annotations": args.PodMetadata.Annotations, "labels": args.PodMetadata.Labels}
	}
`)
	if len(c.Overlays) > 0 {
		b.WriteString("\tif args.OverlayValues != nil {\n\t\textra[\"overlayValues\"] = args.OverlayValues\n\t}\n")
	}
	fmt.Fprintf(&b, `	props := recorded("kubernetes", in, extra)
	if err := ctx.RegisterComponentResourceV2(%q, name, props, c, opts...); err != nil {
		return nil, err
	}
`, "docuconf:"+c.Name+":Config")
	fmt.Fprintf(&b, `	out, err := build(ctx, c, in, %s, args.PodMetadata)
	if err != nil {
		return nil, err
	}
	c.KubernetesEnv = out.env
	c.KubernetesVolumes = out.volumes
	c.KubernetesVolumeMounts = out.volumeMounts
	c.KubernetesConfigMaps = out.configMaps
	c.RestartTriggers = out.restartTriggers
	c.PodAnnotations = out.podAnnotations
	c.PodLabels = out.podLabels
	c.Bindings = out.bindings
	c.Checked = out.checked
	// Registered so that a value that fails its checks when it resolves
	// fails the deployment, once, even if nothing uses the outputs.
	bindings := pulumi.Map{}
	for n, b := range out.bindings {
		bindings[n] = pulumi.Map{"set": pulumi.Bool(b.Set), "source": pulumi.String(b.Source)}
	}
	if err := ctx.RegisterResourceOutputs(c, pulumi.Map{
		"bindings": bindings,
		"checked":  out.checked,
		"contract": props["contract"],
		// The arguments again, for the policy pack: a stack policy sees a
		// component's outputs, not its inputs.
		"arguments": props["arguments"],
	}); err != nil {
		return nil, err
	}
	return c, nil
}
`, overlays)

	config, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, fmt.Errorf("formatting the generated component: %w\n%s", err, b.String())
	}
	pkgFile := func(name, tmpl string) ([]byte, error) {
		b, err := format.Source([]byte(strings.Replace(tmpl, "package PACKAGE", "package "+pkg, 1)))
		if err != nil {
			return nil, fmt.Errorf("the component's %s: %w", name, err)
		}
		return b, nil
	}
	runtime, err := pkgFile("runtime.go", runtimeGo)
	if err != nil {
		return nil, err
	}
	target, err := pkgFile("kubernetes.go", kubernetesGo)
	if err != nil {
		return nil, err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, bytes.TrimSpace(c.JSON), "", "  "); err != nil {
		return nil, err
	}
	indented.WriteByte('\n')
	return map[string][]byte{
		"config.go":     config,
		"runtime.go":    runtime,
		"kubernetes.go": target,
		"contract.json": indented.Bytes(),
		"docuconf.json": meta,
		"README.md":     []byte(g.readmeGo(pkg)),
	}, nil
}

func (g *gen) readmeGo(pkg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s# %s configuration component (Pulumi, Go, Kubernetes)\n\n", terraform.MarkdownHeader, g.c.Name)
	fmt.Fprintf(&b, "Generated by `docuconf pulumi -lang go -target kubernetes` from the `%s` contract. `New%s` takes one argument per input, checks it against the contract, and renders a Kubernetes container's env, volumes and mounts, the ConfigMaps for inline content and overlays, and the pod metadata injected inputs ask for. See [spec/IAC.md](https://github.com/Docuconf/docuconf-go/blob/main/spec/IAC.md).\n\n", g.c.Name, g.class)
	b.WriteString("Values are checked with the Go SDK, the engine the app runs at boot, so the messages are the ones the app would print. A literal is checked when the component is constructed; an Output when it resolves, during `pulumi up`, before any resource that uses the outputs changes.\n\n")
	b.WriteString("| Input | Field | Type | Set with | Description |\n|---|---|---|---|---|\n")
	for _, v := range g.c.Vars {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | %s |\n", v.Name, v.Name, goVarType(v), codeList(goVarSources(v)), cell(describe(v.Raw)))
	}
	for _, f := range g.c.Files {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | %s |\n", f.Name, goField(f.Name), goFileType(f), codeList(goFileSources(f)), cell(describe(f.Raw)+" at `"+str(f.Raw, "path")+"`"))
	}
	fmt.Fprintf(&b, `
## Install

Generate it into a package of the Pulumi program's module, and add the modules it imports:

`+"```sh"+`
go get %s %s %s
`+"```"+`

## Usage

`+"```go"+`
config, err := %s.New%s(ctx, "%s", &%s.Args{
%s})
if err != nil {
	return err
}
for _, cm := range config.KubernetesConfigMaps {
	if _, err := corev1.NewConfigMap(ctx, cm.Name, &corev1.ConfigMapArgs{
		Metadata:  &metav1.ObjectMetaArgs{Name: pulumi.String(cm.Name), Namespace: pulumi.String(namespace)},
		Data:      pulumi.ToStringMap(cm.Data),
		Immutable: pulumi.Bool(cm.Immutable),
	}); err != nil {
		return err
	}
}
// In the Deployment's pod template: Annotations: config.PodAnnotations,
// Env: config.KubernetesEnv, VolumeMounts: config.KubernetesVolumeMounts,
// Volumes: config.KubernetesVolumes.
`+"```"+`
`, goSDKModule, goPulumiModule, goK8sModule, pkg, g.class, g.c.Name, pkg, g.goExample(pkg))
	return b.String()
}

func goVarSources(v terraform.Var) []string {
	if v.Secret {
		return []string{"SecretKeyRef", "Injected"}
	}
	switch v.Type {
	case "string":
		return []string{"String", "ConfigMapKeyRef", "FieldRef", "Injected"}
	case "url", "enum", "duration":
		return []string{"String", "ConfigMapKeyRef", "Injected"}
	case "int":
		return []string{"Int", "ConfigMapKeyRef", "ResourceFieldRef", "Injected"}
	case "float":
		return []string{"Float", "ConfigMapKeyRef", "Injected"}
	case "bool":
		return []string{"Bool", "ConfigMapKeyRef", "Injected"}
	case "list":
		if str(v.Raw, "items") == "int" {
			return []string{"Ints", "Injected"}
		}
		return []string{"Strings", "Injected"}
	case "json":
		return []string{"JSON", "ConfigMapKeyRef", "Injected"}
	}
	return nil
}

func goFileSources(f terraform.File) []string {
	switch goFileType(f) {
	case "TLSFile":
		return []string{"SecretDirectory", "Certificate", "CSI", "InjectedFile"}
	case "SecretFileInput":
		return []string{"SecretFile", "CSI", "InjectedFile"}
	case "ConfigFile":
		return []string{"Inline", "InlineText", "ConfigMapFile", "SecretFile", "CSI", "Image", "InjectedFile"}
	case "BinaryFile":
		return []string{"ConfigMapFile", "SecretFile", "CSI", "Image", "InjectedFile"}
	}
	return []string{"InlineText", "ConfigMapFile", "SecretFile", "CSI", "Image", "InjectedFile"}
}

func (g *gen) goExample(pkg string) string {
	var b strings.Builder
	for _, v := range g.c.Vars {
		if !g.required(v) {
			continue
		}
		if v.Secret {
			fmt.Fprintf(&b, "\t%s: %s.SecretKeyRef(pulumi.String(%q), pulumi.String(\"value\")),\n", v.Name, pkg, strings.ToLower(strings.ReplaceAll(v.Name, "_", "-")))
			continue
		}
		fmt.Fprintf(&b, "\t%s: %s.%s(%s),\n", v.Name, pkg, goVarSources(v)[0], goExampleValue(v))
	}
	if b.Len() == 0 {
		b.WriteString("\t// every input is optional\n")
	}
	return b.String()
}

func goExampleValue(v terraform.Var) string {
	switch v.Type {
	case "int":
		return "pulumi.Int(1)"
	case "float":
		return "pulumi.Float64(1)"
	case "bool":
		return "pulumi.Bool(true)"
	case "list":
		if str(v.Raw, "items") == "int" {
			return "pulumi.IntArray{}"
		}
		return "pulumi.StringArray{}"
	case "json":
		return "map[string]any{}"
	case "duration":
		return `pulumi.String("30s")`
	case "url":
		return `pulumi.String("https://example.com")`
	case "enum":
		if vs := strs(v.Raw, "values"); len(vs) > 0 {
			return fmt.Sprintf("pulumi.String(%q)", vs[0])
		}
	}
	return `pulumi.String("…")`
}
