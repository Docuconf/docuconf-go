package pulumi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"strings"
)

// generateGoTarget writes a Go component for the ecs or cloudrun target:
// the typed Args of the inputs the target carries, and the component with
// the target's outputs.
func (g *gen) generateGoTarget(pkg string, meta []byte, digest string) (map[string][]byte, error) {
	c := g.c
	seen := map[string]string{}
	for _, v := range c.Vars {
		seen[v.Name] = v.Name
	}
	for _, f := range g.files {
		if other, ok := seen[goField(f.Name)]; ok {
			return nil, fmt.Errorf("%s and %s are both %s in Go; rename one", other, f.Name, goField(f.Name))
		}
		seen[goField(f.Name)] = f.Name
	}
	name := map[string]string{"ecs": "Amazon ECS", "cloudrun": "Cloud Run"}[g.target]

	var b strings.Builder
	b.WriteString(Header)
	fmt.Fprintf(&b, "\n// Package %s is the %s contract as a Pulumi component for the %s\n// target (spec/IAC.md section 6). See README.md.\npackage %s\n\n", pkg, c.Name, g.target, pkg)
	b.WriteString("import (\n\t_ \"embed\"\n\n")
	if g.target == "cloudrun" {
		b.WriteString("\tcloudrunv2 \"github.com/pulumi/pulumi-gcp/sdk/v8/go/gcp/cloudrunv2\"\n")
	}
	b.WriteString("\t\"github.com/pulumi/pulumi/sdk/v3/go/pulumi\"\n)\n\n")
	b.WriteString("// contractJSON is the contract this component was generated from.\n//\n//go:embed contract.json\nvar contractJSON []byte\n\n")
	fmt.Fprintf(&b, "// Digest is the sha256 of the contract's compact JSON.\nconst Digest = %q\n\n", digest)

	fmt.Fprintf(&b, "// Args are the arguments of %s: one per input of the %s contract the\n// %s target carries. A nil field is not set. A secret is a SecretVar:\n// Secret or Injected, never a literal.\ntype Args struct {\n", g.class, c.Name, g.target)
	for _, v := range c.Vars {
		goComment(&b, "\t", describe(v.Raw))
		fmt.Fprintf(&b, "\t%s %s\n", v.Name, goVarType(v))
	}
	for _, f := range g.files {
		goComment(&b, "\t", describe(f.Raw)+" ("+str(f.Raw, "path")+"): SecretFile or InjectedFile")
		fmt.Fprintf(&b, "\t%s %s\n", goField(f.Name), goFileType(f))
	}
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "// %s is the %s contract's inputs, checked and rendered for %s.\ntype %s struct {\n\tpulumi.ResourceState\n\n", g.class, c.Name, name, g.class)
	if g.target == "ecs" {
		b.WriteString("\t// EcsEnvironment is the container definition's environment, each {name, value}.\n\tEcsEnvironment pulumi.StringMapArrayOutput\n")
		b.WriteString("\t// EcsSecrets is the container definition's secrets, each {name, valueFrom}.\n\tEcsSecrets pulumi.StringMapArrayOutput\n")
	} else {
		b.WriteString("\t// CloudRunEnv is the container's env entries.\n\tCloudRunEnv cloudrunv2.ServiceTemplateContainerEnvArrayOutput\n")
		b.WriteString("\t// CloudRunVolumes is the revision's secret volumes for file inputs.\n\tCloudRunVolumes cloudrunv2.ServiceTemplateVolumeArrayOutput\n")
		b.WriteString("\t// CloudRunVolumeMounts is the container's volume mounts.\n\tCloudRunVolumeMounts cloudrunv2.ServiceTemplateContainerVolumeMountArrayOutput\n")
	}
	b.WriteString("\t// Bindings says, for each input, whether it is set and by which source.\n\tBindings map[string]Binding\n")
	b.WriteString("\t// Checked resolves once every value has passed its checks, and fails\n\t// with a *ConfigViolation otherwise.\n\tChecked pulumi.BoolOutput\n}\n\n")

	fmt.Fprintf(&b, `// New%[1]s checks the arguments and renders them. Arguments that break the
// contract return a *ConfigViolation listing every problem; a value that is
// an Output is checked when it resolves, and then fails every output.
func New%[1]s(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*%[1]s, error) {
	if args == nil {
		args = &Args{}
	}
	c := &%[1]s{}
	in := inputs{}
`, g.class)
	for _, v := range c.Vars {
		fmt.Fprintf(&b, "\tin.set(%q, args.%s)\n", v.Name, v.Name)
	}
	for _, f := range g.files {
		fmt.Fprintf(&b, "\tin.set(%q, args.%s)\n", f.Name, goField(f.Name))
	}
	fmt.Fprintf(&b, `	// The arguments are the component's inputs, so that a preview shows
	// them and docuconf lint pulumi can check them.
	if err := ctx.RegisterComponentResourceV2(%q, name, recorded(in, nil), c, opts...); err != nil {
		return nil, err
	}
`, "docuconf:"+c.Name+":Config")
	if g.target == "ecs" {
		b.WriteString("\tout, err := buildECS(ctx, c, in)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tc.EcsEnvironment = out.environment\n\tc.EcsSecrets = out.secrets\n")
	} else {
		b.WriteString("\tout, err := buildCloudRun(ctx, c, in)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tc.CloudRunEnv = out.env\n\tc.CloudRunVolumes = out.volumes\n\tc.CloudRunVolumeMounts = out.volumeMounts\n")
	}
	fmt.Fprintf(&b, `	c.Bindings = out.bindings
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
		"contract": pulumi.StringMap{"name": pulumi.String(%q), "digest": pulumi.String(Digest)},
	}); err != nil {
		return nil, err
	}
	return c, nil
}
`, c.Name)

	config, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, fmt.Errorf("formatting the generated component: %w\n%s", err, b.String())
	}
	pkgFile := func(file, tmpl string) ([]byte, error) {
		out, err := format.Source([]byte(strings.Replace(tmpl, "package PACKAGE", "package "+pkg, 1)))
		if err != nil {
			return nil, fmt.Errorf("the component's %s: %w", file, err)
		}
		return out, nil
	}
	files := map[string][]byte{"config.go": config, "docuconf.json": meta, "README.md": []byte(g.readmeFor("go"))}
	own := ecsGo
	if g.target == "cloudrun" {
		own = cloudrunGo
	}
	for file, tmpl := range map[string]string{"runtime.go": runtimeGo, "targets.go": targetsGo, g.target + ".go": own} {
		if files[file], err = pkgFile(file, tmpl); err != nil {
			return nil, err
		}
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, bytes.TrimSpace(c.JSON), "", "  "); err != nil {
		return nil, err
	}
	indented.WriteByte('\n')
	files["contract.json"] = indented.Bytes()
	return files, nil
}
