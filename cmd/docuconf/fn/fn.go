package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"cuelang.org/go/cue"
	"github.com/crossplane/function-sdk-go/errors"
	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/request"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/resource/composed"
	"github.com/crossplane/function-sdk-go/response"
	"google.golang.org/protobuf/types/known/structpb"
	"sigs.k8s.io/yaml"

	"github.com/docuconf/docuconf-go/cmd/docuconf/fn/input/v1alpha1"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/oci"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// ConditionType is the condition the function sets on the composite
// resource and its claim (docs/PLAN.md, Phase 2).
const ConditionType = "ContractValid"

// Annotations the function writes on the workload.
const (
	annotationContract = "docuconf.dev/contract"
	annotationImage    = "docuconf.dev/image"
	annotationEnvHash  = "docuconf.dev/env-hash"
)

// ContextKey is where the function leaves what it rendered, for later
// functions in the pipeline.
const ContextKey = "docuconf.dev/rendered"

// Function is function-docuconf.
type Function struct {
	fnv1.UnimplementedFunctionRunnerServiceServer

	log      logging.Logger
	registry *oci.Client

	// The CUE runtime is not safe for concurrent use; Crossplane calls
	// the function concurrently.
	mu        sync.Mutex
	platform  *platform.Platform
	contracts map[string]*platform.Contract // by pinned image or contract text
}

// NewFunction returns the function.
func NewFunction(log logging.Logger, registry *oci.Client) (*Function, error) {
	p, err := platform.New()
	if err != nil {
		return nil, err
	}
	return &Function{log: log, registry: registry, platform: p, contracts: map[string]*platform.Contract{}}, nil
}

// rendered is #Render's output.
type rendered struct {
	Env            []any            `json:"env"`
	Volumes        []any            `json:"volumes"`
	VolumeMounts   []any            `json:"volumeMounts"`
	ConfigMaps     []map[string]any `json:"configMaps"`
	PodAnnotations map[string]any   `json:"podAnnotations"`
	PodLabels      map[string]any   `json:"podLabels"`
}

// RunFunction validates the platform's values against the image's contract
// and renders them into the target workload.
func (f *Function) RunFunction(ctx context.Context, req *fnv1.RunFunctionRequest) (*fnv1.RunFunctionResponse, error) {
	rsp := response.To(req, response.DefaultTTL)

	in := &v1alpha1.Input{}
	if err := request.GetInput(req, in); err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot read the function's input"))
		return rsp, nil
	}
	if in.Target.Resource == "" {
		response.Fatal(rsp, errors.New("input: target.resource is required: the composed workload that receives the configuration"))
		return rsp, nil
	}
	oxr, err := request.GetObservedCompositeResource(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot read the observed composite resource"))
		return rsp, nil
	}
	desired, err := request.GetDesiredComposedResources(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot read the desired composed resources"))
		return rsp, nil
	}
	observed, err := request.GetObservedComposedResources(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot read the observed composed resources"))
		return rsp, nil
	}
	target := resource.Name(in.Target.Resource)
	_, ok := desired[target]
	if !ok {
		response.Fatal(rsp, errors.Errorf("no desired composed resource %q: an earlier function in the pipeline must declare the workload", target))
		return rsp, nil
	}

	// Documents and image from the composite resource. Until they are
	// all set, wait: a value that does not exist yet is pending, not
	// invalid (docs/EDGE_CASES.md).
	get := func(path string) (any, bool) {
		v, err := oxr.Resource.GetValue(path)
		return v, err == nil && v != nil
	}
	var waiting []string
	doc := func(d v1alpha1.Document) map[string]any {
		out := map[string]any{}
		if d.FromFieldPath != "" {
			v, ok := get(d.FromFieldPath)
			if !ok {
				waiting = append(waiting, d.FromFieldPath)
			} else if m, ok := v.(map[string]any); ok {
				for k, x := range m {
					out[k] = x
				}
			}
		}
		for k, x := range d.Set {
			out[k] = x
		}
		return out
	}
	values, files, overlays := doc(in.Values), doc(in.Files), doc(in.Overlays)
	image := ""
	if in.Image != nil {
		if in.Image.FromFieldPath != "" {
			v, ok := get(in.Image.FromFieldPath)
			if s, isString := v.(string); ok && isString && s != "" {
				image = s
			} else {
				waiting = append(waiting, in.Image.FromFieldPath)
			}
		} else {
			image = in.Image.Value
		}
	}
	// The Secrets the values and files name, which Crossplane fetches
	// for the function: a first call only asks for them.
	missing, waitSecrets, asked := f.secrets(req, rsp, in, oxr, desired, values, files)
	if asked {
		return rsp, nil
	}
	waiting = append(waiting, waitSecrets...)
	if len(waiting) > 0 {
		msg := "waiting for " + strings.Join(waiting, ", ")
		response.ConditionUnknown(rsp, ConditionType, "Waiting").WithMessage(msg).TargetCompositeAndClaim()
		response.Normal(rsp, msg)
		if !f.hold(desired, observed, target, in.Target.Container) {
			// SetDesiredComposedResources only adds and replaces.
			delete(rsp.GetDesired().GetResources(), string(target))
			return rsp, nil
		}
		if err := response.SetDesiredComposedResources(rsp, desired); err != nil {
			response.Fatal(rsp, errors.Wrap(err, "cannot set the desired composed resources"))
		}
		return rsp, nil
	}

	registry, cleanup, err := f.registryFor(req)
	if err != nil {
		response.Fatal(rsp, err)
		return rsp, nil
	}
	defer cleanup()

	f.mu.Lock()
	defer f.mu.Unlock()
	c, pinned, err := f.contract(ctx, registry, in.Contract, image)
	if err != nil {
		response.ConditionFalse(rsp, ConditionType, "ContractUnavailable").WithMessage(err.Error()).TargetCompositeAndClaim()
		response.Fatal(rsp, err)
		return rsp, nil
	}
	p := f.platform
	compile := func(what string, v any) (cue.Value, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return cue.Value{}, err
		}
		return p.CompileJSON(what, b)
	}
	vals, err := compile("values", values)
	if err == nil {
		var fv, ov cue.Value
		if fv, err = compile("files", files); err == nil {
			if ov, err = compile("overlays", overlays); err == nil {
				policy, _ := p.LoadData("")
				if in.Policy != "" {
					policy, err = p.CompileJSON("policy.cue", []byte(in.Policy))
				}
				if err == nil {
					return f.check(rsp, desired, target, in, c, pinned, vals, fv, ov, policy, missing)
				}
			}
		}
	}
	response.Fatal(rsp, errors.Wrap(err, "input"))
	return rsp, nil
}

// check validates and renders, with the platform locked.
func (f *Function) check(rsp *fnv1.RunFunctionResponse, desired map[resource.Name]*resource.DesiredComposed, target resource.Name,
	in *v1alpha1.Input, c *platform.Contract, pinned string, values, files, overlays, policy cue.Value, missing []string) (*fnv1.RunFunctionResponse, error) {
	p := f.platform
	problems, warnings, err := p.ValidateWarn(c, values, files, overlays, policy)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot validate the values"))
		return rsp, nil
	}
	problems = append(problems, missing...)
	for _, w := range warnings {
		response.Warning(rsp, errors.New(w)).TargetCompositeAndClaim()
	}
	who := c.Name
	if pinned != "" {
		who = pinned
	}
	if len(problems) > 0 {
		// Nothing is applied: the running workload keeps its configuration.
		// The lines name inputs and constraints, never a secret's value.
		response.ConditionFalse(rsp, ConditionType, "Invalid").WithMessage(strings.Join(problems, "; ")).TargetCompositeAndClaim()
		response.Fatal(rsp, errors.Errorf("the values do not satisfy %s's contract:\n%s", who, strings.Join(problems, "\n")))
		return rsp, nil
	}
	out, err := p.Render(c, values, files, overlays)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot render the configuration"))
		return rsp, nil
	}
	var r rendered
	if err := yaml.Unmarshal(out, &r); err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot read the rendered configuration"))
		return rsp, nil
	}
	w := desired[target]
	if err := apply(w.Resource, in, r, c.Name, pinned); err != nil {
		response.ConditionFalse(rsp, ConditionType, "TargetRejected").WithMessage(err.Error()).TargetCompositeAndClaim()
		response.Fatal(rsp, err)
		return rsp, nil
	}
	for _, cm := range r.ConfigMaps {
		name, _ := cm["metadata"].(map[string]any)["name"].(string)
		u := composed.New()
		u.SetUnstructuredContent(cm)
		desired[resource.Name("docuconf-configmap-"+name)] = &resource.DesiredComposed{Resource: u, Ready: resource.ReadyTrue}
	}
	if err := response.SetDesiredComposedResources(rsp, desired); err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot set the desired composed resources"))
		return rsp, nil
	}
	if ctxv, err := structpb.NewValue(jsonish(r)); err == nil {
		response.SetContextKey(rsp, ContextKey, ctxv)
	}
	response.ConditionTrue(rsp, ConditionType, "Valid").WithMessage(fmt.Sprintf("the values satisfy %s's contract", who)).TargetCompositeAndClaim()
	return rsp, nil
}

// contract finds the contract: inline, oci://, or the image's own, pulled
// once per pinned image. pinned is the image pinned by digest, when the
// contract came from it.
func (f *Function) contract(ctx context.Context, registry *oci.Client, inline, image string) (*platform.Contract, string, error) {
	src := strings.TrimSpace(inline)
	switch {
	case src != "" && !strings.HasPrefix(src, oci.Scheme):
		if c, ok := f.contracts[src]; ok {
			return c, f.pin(ctx, registry, image), nil
		}
		c, err := f.platform.ParseContract("contract.cue", []byte(src))
		if err != nil {
			return nil, "", errors.Wrap(err, "input: contract")
		}
		f.contracts[src] = c
		return c, f.pin(ctx, registry, image), nil
	case src == "" && image == "":
		return nil, "", errors.New("input: set image, whose contract the function pulls, or contract")
	case src == "":
		src = image
	}
	ref, err := oci.ParseRef(src)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// A tag can move, so it is resolved every time; the contract of a
	// digest never changes.
	img, _, err := registry.Resolve(ctx, ref)
	if err != nil {
		return nil, "", err
	}
	if c, ok := f.contracts[img.String()]; ok {
		return c, img.String(), nil
	}
	pulled, err := registry.Pull(ctx, ref)
	if err != nil {
		return nil, "", err
	}
	c, err := f.platform.ParseContract("contract.cue", pulled.Contract)
	if err != nil {
		return nil, "", errors.Wrapf(err, "the contract attached to %s", pulled.Image)
	}
	f.contracts[pulled.Image.String()] = c
	return c, pulled.Image.String(), nil
}

// pin resolves an image to its digest, for an inline contract; "" when
// there is no image or it cannot be resolved (the workload keeps its tag).
func (f *Function) pin(ctx context.Context, registry *oci.Client, image string) string {
	if image == "" {
		return ""
	}
	ref, err := oci.ParseRef(image)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	img, _, err := registry.Resolve(ctx, ref)
	if err != nil {
		f.log.Info("cannot resolve the image; the workload keeps its reference", "image", image, "error", err)
		return ""
	}
	return img.String()
}

// podTemplate is where a workload kind keeps its pod template.
func podTemplate(kind string) []string {
	switch kind {
	case "Pod":
		return nil
	case "CronJob":
		return []string{"spec", "jobTemplate", "spec", "template"}
	}
	return []string{"spec", "template"}
}

func at(m map[string]any, path []string, create bool) (map[string]any, error) {
	for _, k := range path {
		next, ok := m[k].(map[string]any)
		if !ok {
			if !create {
				return nil, errors.Errorf("%s is not set", strings.Join(path, "."))
			}
			next = map[string]any{}
			m[k] = next
		}
		m = next
	}
	return m, nil
}

// container finds the app's container in a pod spec.
func container(spec map[string]any, name string) (map[string]any, error) {
	cs, _ := spec["containers"].([]any)
	for _, c := range cs {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if name == "" || m["name"] == name {
			return m, nil
		}
	}
	if name == "" {
		return nil, errors.New("the workload has no container")
	}
	return nil, errors.Errorf("the workload has no container %q", name)
}

// ours says whether a volume or mount is the function's (#Render names
// them dc-<input>).
func ours(x any) bool {
	m, _ := x.(map[string]any)
	n, _ := m["name"].(string)
	return strings.HasPrefix(n, "dc-")
}

func replaceOurs(list []any, with []any) []any {
	out := make([]any, 0, len(list)+len(with))
	for _, x := range list {
		if !ours(x) {
			out = append(out, x)
		}
	}
	return append(out, with...)
}

// apply writes the rendered configuration into the workload: the
// container's env, which the function owns, the docuconf volumes and
// mounts, the pod metadata injectors need, the image pinned by digest,
// and no service links (docs/EDGE_CASES.md).
func apply(w *composed.Unstructured, in *v1alpha1.Input, r rendered, contract, pinned string) error {
	obj := w.UnstructuredContent()
	tmpl, err := at(obj, podTemplate(w.GetKind()), false)
	if err != nil {
		return errors.Wrapf(err, "the %s %q", w.GetKind(), w.GetName())
	}
	spec, err := at(tmpl, []string{"spec"}, false)
	if err != nil {
		return errors.Wrapf(err, "the %s's pod template", w.GetKind())
	}
	c, err := container(spec, in.Target.Container)
	if err != nil {
		return err
	}
	if from, _ := c["envFrom"].([]any); len(from) > 0 && !in.AllowEnvFrom {
		return errors.New("the container takes envFrom, whose keys the contract cannot check; remove it, or set allowEnvFrom in the function's input")
	}
	c["env"] = nonNil(r.Env)
	mounts, _ := c["volumeMounts"].([]any)
	c["volumeMounts"] = replaceOurs(mounts, r.VolumeMounts)
	vols, _ := spec["volumes"].([]any)
	spec["volumes"] = replaceOurs(vols, r.Volumes)
	spec["enableServiceLinks"] = false
	if pinned != "" {
		c["image"] = pinned
	}
	meta, _ := at(tmpl, []string{"metadata"}, true)
	for k, m := range map[string]map[string]any{"annotations": r.PodAnnotations, "labels": r.PodLabels} {
		if len(m) == 0 {
			continue
		}
		dst, _ := at(meta, []string{k}, true)
		for key, v := range m {
			dst[key] = v
		}
	}
	envJSON, _ := json.Marshal(r.Env)
	sum := sha256.Sum256(envJSON)
	ann := w.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[annotationContract] = contract
	ann[annotationEnvHash] = hex.EncodeToString(sum[:8])
	if pinned != "" {
		ann[annotationImage] = pinned
	}
	w.SetAnnotations(ann)
	return nil
}

func nonNil(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}

// hold keeps a waiting workload as it runs, with its last configuration.
// It returns false when the workload does not exist yet: then it is not
// created, so it never starts with half its configuration.
func (f *Function) hold(desired map[resource.Name]*resource.DesiredComposed, observed map[resource.Name]resource.ObservedComposed, target resource.Name, name string) bool {
	o, ok := observed[target]
	if !ok {
		return false
	}
	d := desired[target].Resource.UnstructuredContent()
	ob := o.Resource.UnstructuredContent()
	kind := o.Resource.GetKind()
	dt, err1 := at(d, podTemplate(kind), false)
	ot, err2 := at(ob, podTemplate(kind), false)
	if err1 != nil || err2 != nil {
		return true
	}
	ds, err1 := at(dt, []string{"spec"}, false)
	obs, err2 := at(ot, []string{"spec"}, false)
	if err1 != nil || err2 != nil {
		return true
	}
	dc, err1 := container(ds, name)
	oc, err2 := container(obs, name)
	if err1 != nil || err2 != nil {
		return true
	}
	for _, k := range []string{"env", "volumeMounts", "image"} {
		if v, ok := oc[k]; ok {
			dc[k] = v
		}
	}
	if v, ok := obs["volumes"]; ok {
		ds["volumes"] = v
	}
	if v, ok := ot["metadata"]; ok {
		dt["metadata"] = v
	}
	return true
}

// jsonish converts through JSON, for structpb.
func jsonish(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}
