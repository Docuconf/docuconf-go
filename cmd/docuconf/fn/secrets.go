package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/crossplane/function-sdk-go/errors"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/request"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/response"

	"github.com/docuconf/docuconf-go/cmd/docuconf/fn/input/v1alpha1"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/oci"
)

// RegistryCredentials is the name of the step's credentials the function
// pulls contracts with: a Secret of type kubernetes.io/dockerconfigjson.
const RegistryCredentials = "registry"

// registryFor is the registry client for a request: with the step's
// registry credentials, when it has them, written where only this
// request reads them.
func (f *Function) registryFor(req *fnv1.RunFunctionRequest) (*oci.Client, func(), error) {
	if _, ok := req.GetCredentials()[RegistryCredentials]; !ok {
		return f.registry, func() {}, nil
	}
	creds, err := request.GetCredentials(req, RegistryCredentials)
	if err != nil {
		return nil, nil, err
	}
	config, ok := creds.Data[".dockerconfigjson"]
	if !ok {
		config, ok = creds.Data["config.json"]
	}
	if !ok {
		return nil, nil, errors.Errorf("the %s credentials have no .dockerconfigjson: use a Secret of type kubernetes.io/dockerconfigjson", RegistryCredentials)
	}
	dir, err := os.MkdirTemp("", "docuconf-registry-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.WriteFile(filepath.Join(dir, "config.json"), config, 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	c := *f.registry
	c.Env = []string{"DOCKER_CONFIG=" + dir}
	return &c, cleanup, nil
}

// secretUse is one input that reads a Secret, and the key it reads; ""
// for a whole Secret (a file input's directory).
type secretUse struct{ input, key string }

// secretUses lists the Secrets the values and file sources name.
func secretUses(values, files map[string]any) map[string][]secretUse {
	out := map[string][]secretUse{}
	for input, v := range values {
		m, _ := v.(map[string]any)
		ref, _ := m["secretKeyRef"].(map[string]any)
		if name, _ := ref["name"].(string); name != "" {
			key, _ := ref["key"].(string)
			out[name] = append(out[name], secretUse{input, key})
		}
	}
	for input, v := range files {
		m, _ := v.(map[string]any)
		ref, _ := m["secret"].(map[string]any)
		if name, _ := ref["name"].(string); name != "" {
			key, _ := ref["key"].(string)
			out[name] = append(out[name], secretUse{input, key})
		}
	}
	return out
}

func namespaceOf(xr *resource.Composite) string {
	if ns := xr.Resource.GetNamespace(); ns != "" {
		return ns
	}
	// A claim's composite is cluster-scoped; its claim's namespace is the
	// workload's.
	return xr.Resource.GetLabels()["crossplane.io/claim-namespace"]
}

// createdHere says whether the composition itself declares the Secret, or
// an ExternalSecret that makes it: then a missing one is syncing, not
// absent.
func createdHere(desired map[resource.Name]*resource.DesiredComposed, name string) bool {
	for _, d := range desired {
		u := d.Resource
		if u.GetKind() == "Secret" && u.GetName() == name {
			return true
		}
		if u.GetKind() == "ExternalSecret" {
			target, _ := u.GetString("spec.target.name")
			if target == name || (target == "" && u.GetName() == name) {
				return true
			}
		}
	}
	return false
}

// secrets checks that each Secret the values and file sources name exists,
// with its key (docs/EDGE_CASES.md). It asks Crossplane for them as
// required resources; asked is true while Crossplane has not supplied them
// yet, and the function returns at once. missing are problem lines, never
// with a Secret's data; waiting names Secrets the composition creates and
// that are not there yet.
func (f *Function) secrets(req *fnv1.RunFunctionRequest, rsp *fnv1.RunFunctionResponse, in *v1alpha1.Input, xr *resource.Composite,
	desired map[resource.Name]*resource.DesiredComposed, values, files map[string]any) (missing, waiting []string, asked bool) {
	if in.CheckSecrets != nil && !*in.CheckSecrets {
		return nil, nil, false
	}
	ns := namespaceOf(xr)
	uses := secretUses(values, files)
	if ns == "" || len(uses) == 0 {
		return nil, nil, false
	}
	// A Crossplane that cannot supply them would be asked forever.
	if !request.HasCapability(req, fnv1.Capability_CAPABILITY_REQUIRED_RESOURCES) {
		response.Warning(rsp, errors.New("this Crossplane does not supply required resources to functions, so the Secrets the values name are not checked; set checkSecrets: false to silence this")).TargetComposite()
		return nil, nil, false
	}
	names := make([]string, 0, len(uses))
	for n := range uses {
		names = append(names, n)
	}
	sort.Strings(names)
	if rsp.Requirements == nil {
		rsp.Requirements = &fnv1.Requirements{}
	}
	if rsp.Requirements.Resources == nil {
		rsp.Requirements.Resources = map[string]*fnv1.ResourceSelector{}
	}
	for _, n := range names {
		key := "docuconf-secret-" + n
		rsp.Requirements.Resources[key] = &fnv1.ResourceSelector{
			ApiVersion: "v1", Kind: "Secret", Namespace: &ns,
			Match: &fnv1.ResourceSelector_MatchName{MatchName: n},
		}
		items, resolved, err := request.GetRequiredResource(req, key)
		if err != nil || !resolved {
			asked = true
			continue
		}
		if len(items) == 0 {
			if createdHere(desired, n) {
				waiting = append(waiting, fmt.Sprintf("Secret %s, which this composition creates", n))
				continue
			}
			for _, u := range uses[n] {
				missing = append(missing, fmt.Sprintf("%s: Secret %s does not exist in namespace %s", u.input, n, ns))
			}
			continue
		}
		data, _ := items[0].Resource.Object["data"].(map[string]any)
		for _, u := range uses[n] {
			if _, ok := data[u.key]; u.key != "" && !ok {
				missing = append(missing, fmt.Sprintf("%s: Secret %s has no key %s", u.input, n, u.key))
			}
		}
	}
	sort.Strings(missing)
	return missing, waiting, asked
}
