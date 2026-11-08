package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"cuelabs.dev/go/oci/ociregistry"
	"cuelabs.dev/go/oci/ociregistry/ociclient"
	"cuelabs.dev/go/oci/ociregistry/ocimem"
	"cuelabs.dev/go/oci/ociregistry/ociserver"
	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/oci"
)

const ordersContract = "../../../examples/orders/contract.cue"

// deployment is what an earlier function (patch-and-transform, a Go
// template) declares: the workload without its configuration.
const deployment = `{
  "apiVersion": "apps/v1", "kind": "Deployment",
  "metadata": {"name": "orders"},
  "spec": {
    "selector": {"matchLabels": {"app": "orders"}},
    "template": {
      "metadata": {"labels": {"app": "orders"}},
      "spec": {"containers": [
        {"name": "sidecar", "image": "envoy:1"},
        {"name": "orders-api", "image": "registry.example.com/orders-api:2.3.0", "volumeMounts": [{"name": "cache", "mountPath": "/cache"}]}
      ], "volumes": [{"name": "cache", "emptyDir": {}}]}
    }
  }
}`

const goodValues = `{
  "DATABASE_URL": {"secretKeyRef": {"name": "orders-db", "key": "url"}},
  "WEBHOOK_KEYS": {"secretKeyRef": {"name": "orders-webhooks", "key": "keys"}},
  "LOG_LEVEL": "warn",
  "PORT": 8080
}`

type tc struct {
	input    map[string]any
	xr       string // the composite resource's spec, JSON
	observed map[string]string
	edit     func(map[string]any)
}

func contractText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(ordersContract)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func run(t *testing.T, f *Function, c tc) *fnv1.RunFunctionResponse {
	t.Helper()
	in := map[string]any{"apiVersion": "docuconf.fn.crossplane.io/v1alpha1", "kind": "Input",
		"target": map[string]any{"resource": "deployment", "container": "orders-api"}}
	for k, v := range c.input {
		in[k] = v
	}
	if c.edit != nil {
		c.edit(in)
	}
	inJSON, _ := json.Marshal(in)
	req := &fnv1.RunFunctionRequest{
		Input: resource.MustStructJSON(string(inJSON)),
		Observed: &fnv1.State{Composite: &fnv1.Resource{Resource: resource.MustStructJSON(
			`{"apiVersion": "example.org/v1", "kind": "XApp", "metadata": {"name": "orders"}, "spec": ` + c.xr + `}`)}},
		Desired: &fnv1.State{Resources: map[string]*fnv1.Resource{"deployment": {Resource: resource.MustStructJSON(deployment)}}},
	}
	if c.observed != nil {
		req.Observed.Resources = map[string]*fnv1.Resource{}
		for k, v := range c.observed {
			req.Observed.Resources[k] = &fnv1.Resource{Resource: resource.MustStructJSON(v)}
		}
	}
	rsp, err := f.RunFunction(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return rsp
}

func newFunction(t *testing.T) *Function {
	t.Helper()
	f, err := NewFunction(logging.NewNopLogger(), &oci.Client{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func condition(t *testing.T, rsp *fnv1.RunFunctionResponse) *fnv1.Condition {
	t.Helper()
	for _, c := range rsp.GetConditions() {
		if c.GetType() == ConditionType {
			return c
		}
	}
	t.Fatalf("no %s condition; results: %v", ConditionType, rsp.GetResults())
	return nil
}

func fatal(rsp *fnv1.RunFunctionResponse) string {
	for _, r := range rsp.GetResults() {
		if r.GetSeverity() == fnv1.Severity_SEVERITY_FATAL {
			return r.GetMessage()
		}
	}
	return ""
}

func desired(t *testing.T, rsp *fnv1.RunFunctionResponse, name string) map[string]any {
	t.Helper()
	r, ok := rsp.GetDesired().GetResources()[name]
	if !ok {
		return nil
	}
	b, err := r.GetResource().MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func appContainer(t *testing.T, d map[string]any) map[string]any {
	t.Helper()
	cs := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	for _, c := range cs {
		if m := c.(map[string]any); m["name"] == "orders-api" {
			return m
		}
	}
	t.Fatal("no orders-api container")
	return nil
}

func envNames(c map[string]any) []string {
	var out []string
	for _, e := range c["env"].([]any) {
		out = append(out, e.(map[string]any)["name"].(string))
	}
	return out
}

func TestValid(t *testing.T) {
	f := newFunction(t)
	rsp := run(t, f, tc{
		input: map[string]any{
			"contract": contractText(t),
			"values":   map[string]any{"fromFieldPath": "spec.config.values", "set": map[string]any{"LOG_LEVEL": "error"}},
			"files": map[string]any{"set": map[string]any{
				"discounts":   map[string]any{"inline": map[string]any{"codes": map[string]any{"WELCOME10": 10}}},
				"serving-tls": map[string]any{"secret": map[string]any{"name": "orders-tls"}},
			}},
		},
		xr: `{"config": {"values": ` + goodValues + `}}`,
	})
	if m := fatal(rsp); m != "" {
		t.Fatalf("fatal: %s", m)
	}
	if c := condition(t, rsp); c.GetStatus() != fnv1.Status_STATUS_CONDITION_TRUE || c.GetReason() != "Valid" {
		t.Errorf("condition: %v", c)
	}
	d := desired(t, rsp, "deployment")
	c := appContainer(t, d)
	if got := strings.Join(envNames(c), ","); got != "DATABASE_URL,LOG_LEVEL,PORT,WEBHOOK_KEYS" {
		t.Errorf("env: %s", got)
	}
	// The set entry wins over the composite resource's.
	env, _ := json.Marshal(c["env"])
	if !strings.Contains(string(env), `{"name":"LOG_LEVEL","value":"error"}`) || !strings.Contains(string(env), `"secretKeyRef":{"key":"url","name":"orders-db"}`) {
		t.Errorf("env: %s", env)
	}
	// The app's own mounts and volumes stay; docuconf's are added.
	mounts, _ := json.Marshal(c["volumeMounts"])
	if !strings.Contains(string(mounts), `"/cache"`) || !strings.Contains(string(mounts), `"dc-discounts"`) || !strings.Contains(string(mounts), `"dc-serving-tls"`) {
		t.Errorf("mounts: %s", mounts)
	}
	spec := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if spec["enableServiceLinks"] != false {
		t.Errorf("enableServiceLinks: %v", spec["enableServiceLinks"])
	}
	if c["image"] != "registry.example.com/orders-api:2.3.0" {
		t.Errorf("an image that cannot be resolved keeps its reference, got %v", c["image"])
	}
	ann := d["metadata"].(map[string]any)["annotations"].(map[string]any)
	if ann[annotationContract] != "orders-api" || ann[annotationEnvHash] == nil {
		t.Errorf("annotations: %v", ann)
	}
	var cm map[string]any
	for name := range rsp.GetDesired().GetResources() {
		if strings.HasPrefix(name, "docuconf-configmap-orders-api-discounts-") {
			cm = desired(t, rsp, name)
		}
	}
	if cm == nil || cm["kind"] != "ConfigMap" || cm["immutable"] != true {
		t.Errorf("no discounts ConfigMap: %v", rsp.GetDesired().GetResources())
	}
	if _, ok := rsp.GetContext().GetFields()[ContextKey]; !ok {
		t.Errorf("no %s context key", ContextKey)
	}
}

func TestInvalid(t *testing.T) {
	f := newFunction(t)
	bad := strings.Replace(goodValues, `"PORT": 8080`, `"PORT": 70000, "LOG_LEVL": "warn"`, 1)
	bad = strings.Replace(bad, `{"secretKeyRef": {"name": "orders-db", "key": "url"}}`, `"postgres://orders:hunter2@db/orders"`, 1)
	rsp := run(t, f, tc{
		input: map[string]any{"contract": contractText(t), "values": map[string]any{"fromFieldPath": "spec.values"}},
		xr:    `{"values": ` + bad + `}`,
	})
	msg := fatal(rsp)
	c := condition(t, rsp)
	for _, want := range []string{"PORT: 70000 is above max 65535", "LOG_LEVL: is not declared in the contract", "DATABASE_URL:"} {
		if !strings.Contains(msg, want) || !strings.Contains(c.GetMessage(), want) {
			t.Errorf("no %q in\n fatal: %s\n condition: %s", want, msg, c.GetMessage())
		}
	}
	if strings.Contains(msg+c.GetMessage(), "hunter2") {
		t.Errorf("a secret's value is in the message: %s", msg)
	}
	if c.GetStatus() != fnv1.Status_STATUS_CONDITION_FALSE || c.GetReason() != "Invalid" {
		t.Errorf("condition: %v", c)
	}
}

func TestPolicy(t *testing.T) {
	f := newFunction(t)
	rsp := run(t, f, tc{
		input: map[string]any{"contract": contractText(t), "values": map[string]any{"fromFieldPath": "spec.values"},
			"policy": `LOG_LEVEL?: "warn" | "error"` + "\n" + `secretReferences: [string]: secretKeyRef?: name: =~"^orders-"`},
		xr: `{"values": ` + strings.Replace(goodValues, `"name": "orders-db"`, `"name": "billing-db"`, 1) + `}`,
	})
	if msg := fatal(rsp); !strings.Contains(msg, "DATABASE_URL") || !strings.Contains(msg, "orders-") {
		t.Errorf("the policy should reject billing-db: %q", msg)
	}
}

func TestWaiting(t *testing.T) {
	f := newFunction(t)
	in := map[string]any{"contract": contractText(t), "values": map[string]any{"fromFieldPath": "spec.values"}}
	// Not created yet: the workload is held back.
	rsp := run(t, f, tc{input: in, xr: `{}`})
	if m := fatal(rsp); m != "" {
		t.Fatalf("fatal: %s", m)
	}
	c := condition(t, rsp)
	if c.GetStatus() != fnv1.Status_STATUS_CONDITION_UNKNOWN || c.GetReason() != "Waiting" || !strings.Contains(c.GetMessage(), "spec.values") {
		t.Errorf("condition: %v", c)
	}
	if desired(t, rsp, "deployment") != nil {
		t.Errorf("a workload whose values are not set yet must not be created")
	}
	// Running: it keeps the configuration it has.
	observed := strings.Replace(deployment, `"volumeMounts": [{"name": "cache", "mountPath": "/cache"}]`,
		`"env": [{"name": "LOG_LEVEL", "value": "info"}], "volumeMounts": [{"name": "cache", "mountPath": "/cache"}]`, 1)
	rsp = run(t, f, tc{input: in, xr: `{}`, observed: map[string]string{"deployment": observed}})
	d := desired(t, rsp, "deployment")
	if d == nil || strings.Join(envNames(appContainer(t, d)), ",") != "LOG_LEVEL" {
		t.Errorf("a running workload keeps its env while waiting: %v", d)
	}
}

func TestEnvFrom(t *testing.T) {
	f := newFunction(t)
	c := tc{input: map[string]any{"contract": contractText(t), "values": map[string]any{"set": jsonMap(t, goodValues)}}, xr: `{}`}
	// A container that bulk-loads env.
	withEnvFrom := strings.Replace(deployment, `"name": "orders-api",`, `"name": "orders-api", "envFrom": [{"configMapRef": {"name": "everything"}}],`, 1)
	rsp := runWith(t, f, c, withEnvFrom)
	if msg := fatal(rsp); !strings.Contains(msg, "envFrom") || condition(t, rsp).GetReason() != "TargetRejected" {
		t.Errorf("envFrom: %q", msg)
	}
	c.edit = func(in map[string]any) { in["allowEnvFrom"] = true }
	if rsp := runWith(t, f, c, withEnvFrom); fatal(rsp) != "" {
		t.Errorf("allowEnvFrom: %s", fatal(rsp))
	}
}

func runWith(t *testing.T, f *Function, c tc, workload string) *fnv1.RunFunctionResponse {
	t.Helper()
	in := map[string]any{"apiVersion": "docuconf.fn.crossplane.io/v1alpha1", "kind": "Input",
		"target": map[string]any{"resource": "deployment", "container": "orders-api"}}
	for k, v := range c.input {
		in[k] = v
	}
	if c.edit != nil {
		c.edit(in)
	}
	inJSON, _ := json.Marshal(in)
	rsp, err := f.RunFunction(context.Background(), &fnv1.RunFunctionRequest{
		Input:    resource.MustStructJSON(string(inJSON)),
		Observed: &fnv1.State{Composite: &fnv1.Resource{Resource: resource.MustStructJSON(`{"apiVersion": "example.org/v1", "kind": "XApp", "metadata": {"name": "orders"}, "spec": ` + c.xr + `}`)}},
		Desired:  &fnv1.State{Resources: map[string]*fnv1.Resource{"deployment": {Resource: resource.MustStructJSON(workload)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rsp
}

func jsonMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// The contract pulled from the image's registry, and the image pinned by
// digest in the workload.
func TestFromImage(t *testing.T) {
	srv := httptest.NewServer(ociserver.New(ocimem.New(), nil))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	client := &oci.Client{}
	pushImage(t, host, "orders-api", "2.3.0")
	ref, _ := oci.ParseRef(host + "/orders-api:2.3.0")
	img, _, err := client.Push(context.Background(), ref, []byte(contractText(t)))
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewFunction(logging.NewNopLogger(), client)
	if err != nil {
		t.Fatal(err)
	}
	rsp := run(t, f, tc{
		input: map[string]any{"image": map[string]any{"fromFieldPath": "spec.image"}, "values": map[string]any{"fromFieldPath": "spec.values"}},
		xr:    `{"image": "` + host + `/orders-api:2.3.0", "values": ` + goodValues + `}`,
	})
	if m := fatal(rsp); m != "" {
		t.Fatalf("fatal: %s", m)
	}
	d := desired(t, rsp, "deployment")
	if got := appContainer(t, d)["image"]; got != img.String() {
		t.Errorf("image: got %v, want %s, pinned", got, img)
	}
	if !strings.Contains(condition(t, rsp).GetMessage(), img.String()) {
		t.Errorf("condition: %v", condition(t, rsp))
	}
	// An image without a contract is an explicit error.
	pushImage(t, host, "bare", "1.0.0")
	rsp = run(t, f, tc{
		input: map[string]any{"image": map[string]any{"value": host + "/bare:1.0.0"}, "values": map[string]any{"set": jsonMap(t, goodValues)}},
		xr:    `{}`,
	})
	if msg := fatal(rsp); !strings.Contains(msg, "has no contract") || condition(t, rsp).GetReason() != "ContractUnavailable" {
		t.Errorf("no contract: %q", msg)
	}
}

func ociclientFor(host string) (ociregistry.Interface, error) {
	return ociclient.New(host, &ociclient.Options{Insecure: true})
}

func pushImage(t *testing.T, host, repo, tag string) {
	t.Helper()
	ctx := context.Background()
	reg, err := ociclientFor(host)
	if err != nil {
		t.Fatal(err)
	}
	blob := func(mt string, data []byte) ociregistry.Descriptor {
		d := ociregistry.Descriptor{MediaType: mt, Digest: digest.FromBytes(data), Size: int64(len(data))}
		if _, err := reg.PushBlob(ctx, repo, d, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		return d
	}
	m := ocispec.Manifest{MediaType: ocispec.MediaTypeImageManifest,
		Config: blob(ocispec.MediaTypeImageConfig, []byte(`{"architecture":"amd64","os":"linux"}`)),
		Layers: []ociregistry.Descriptor{blob(ocispec.MediaTypeImageLayerGzip, []byte("layer of "+repo))}}
	m.SchemaVersion = 2
	b, _ := json.Marshal(m)
	if _, err := reg.PushManifest(ctx, repo, tag, b, ocispec.MediaTypeImageManifest); err != nil {
		t.Fatal(err)
	}
}

// answer runs the function as Crossplane does: while the response
// requires resources the request lacks, it fetches them from cluster, a
// set of Secrets by name, and calls again.
func answer(t *testing.T, f *Function, req *fnv1.RunFunctionRequest, cluster map[string]map[string]string) *fnv1.RunFunctionResponse {
	t.Helper()
	for i := 0; ; i++ {
		rsp, err := f.RunFunction(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		reqs := rsp.GetRequirements().GetResources()
		done := true
		for k := range reqs {
			if _, ok := req.GetRequiredResources()[k]; !ok {
				done = false
			}
		}
		if done {
			return rsp
		}
		if i == 3 {
			t.Fatal("the requirements never settle")
		}
		req.RequiredResources = map[string]*fnv1.Resources{}
		for k, sel := range reqs {
			res := &fnv1.Resources{}
			if data, ok := cluster[sel.GetMatchName()]; ok {
				d := map[string]any{}
				for key := range data {
					d[key] = "c2VjcmV0" // base64 of "secret": never read
				}
				obj, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Secret",
					"metadata": map[string]any{"name": sel.GetMatchName(), "namespace": sel.GetNamespace()}, "data": d})
				res.Items = []*fnv1.Resource{{Resource: resource.MustStructJSON(string(obj))}}
			}
			req.RequiredResources[k] = res
		}
	}
}

func secretsRequest(t *testing.T, input map[string]any, extra map[string]string) *fnv1.RunFunctionRequest {
	t.Helper()
	in := map[string]any{"apiVersion": "docuconf.fn.crossplane.io/v1alpha1", "kind": "Input", "contract": contractText(t),
		"values": map[string]any{"fromFieldPath": "spec.values"}, "target": map[string]any{"resource": "deployment", "container": "orders-api"}}
	for k, v := range input {
		in[k] = v
	}
	inJSON, _ := json.Marshal(in)
	resources := map[string]*fnv1.Resource{"deployment": {Resource: resource.MustStructJSON(deployment)}}
	for k, v := range extra {
		resources[k] = &fnv1.Resource{Resource: resource.MustStructJSON(v)}
	}
	return &fnv1.RunFunctionRequest{
		Meta:  capable,
		Input: resource.MustStructJSON(string(inJSON)),
		Observed: &fnv1.State{Composite: &fnv1.Resource{Resource: resource.MustStructJSON(
			`{"apiVersion": "example.org/v1", "kind": "App", "metadata": {"name": "orders", "namespace": "shop"}, "spec": {"values": ` + goodValues + `}}`)}},
		Desired: &fnv1.State{Resources: resources},
	}
}

// capable is a Crossplane that supplies required resources.
var capable = &fnv1.RequestMeta{Capabilities: []fnv1.Capability{fnv1.Capability_CAPABILITY_CAPABILITIES, fnv1.Capability_CAPABILITY_REQUIRED_RESOURCES}}

// An older Crossplane is never asked: the Secrets are not checked, and it
// says so.
func TestSecretsUnsupported(t *testing.T) {
	f := newFunction(t)
	req := secretsRequest(t, nil, nil)
	req.Meta = nil
	rsp, err := f.RunFunction(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	warned := false
	for _, r := range rsp.GetResults() {
		warned = warned || r.GetSeverity() == fnv1.Severity_SEVERITY_WARNING && strings.Contains(r.GetMessage(), "does not supply required resources")
	}
	if len(rsp.GetRequirements().GetResources()) != 0 || !warned || condition(t, rsp).GetReason() != "Valid" {
		t.Errorf("unsupported: requirements %v, results %v", rsp.GetRequirements(), rsp.GetResults())
	}
}

func TestSecrets(t *testing.T) {
	f := newFunction(t)
	both := map[string]map[string]string{"orders-db": {"url": ""}, "orders-webhooks": {"keys": ""}}

	// The first call only asks for the Secrets, in the App's namespace.
	rsp, err := f.RunFunction(context.Background(), secretsRequest(t, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	sel := rsp.GetRequirements().GetResources()["docuconf-secret-orders-db"]
	if sel.GetKind() != "Secret" || sel.GetNamespace() != "shop" || sel.GetMatchName() != "orders-db" || len(rsp.GetConditions()) != 0 {
		t.Errorf("first call: %v, conditions %v", rsp.GetRequirements(), rsp.GetConditions())
	}

	if rsp := answer(t, f, secretsRequest(t, nil, nil), both); fatal(rsp) != "" || condition(t, rsp).GetReason() != "Valid" {
		t.Errorf("both Secrets: %s", fatal(rsp))
	}

	rsp = answer(t, f, secretsRequest(t, nil, nil), map[string]map[string]string{"orders-db": {"password": ""}})
	msg := fatal(rsp)
	for _, want := range []string{"DATABASE_URL: Secret orders-db has no key url", "WEBHOOK_KEYS: Secret orders-webhooks does not exist in namespace shop"} {
		if !strings.Contains(msg, want) || !strings.Contains(condition(t, rsp).GetMessage(), want) {
			t.Errorf("no %q in %q", want, msg)
		}
	}
	if condition(t, rsp).GetReason() != "Invalid" {
		t.Errorf("condition: %v", condition(t, rsp))
	}

	// An ExternalSecret this composition creates is syncing: wait.
	es := `{"apiVersion": "external-secrets.io/v1", "kind": "ExternalSecret", "metadata": {"name": "orders-webhooks"}, "spec": {}}`
	rsp = answer(t, f, secretsRequest(t, nil, map[string]string{"webhooks": es}), map[string]map[string]string{"orders-db": {"url": ""}})
	if c := condition(t, rsp); c.GetReason() != "Waiting" || !strings.Contains(c.GetMessage(), "Secret orders-webhooks, which this composition creates") || fatal(rsp) != "" {
		t.Errorf("syncing: %v %s", c, fatal(rsp))
	}

	// Turned off, nothing is asked.
	rsp, err = f.RunFunction(context.Background(), secretsRequest(t, map[string]any{"checkSecrets": false}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rsp.GetRequirements().GetResources()) != 0 || condition(t, rsp).GetReason() != "Valid" {
		t.Errorf("checkSecrets false: %v", rsp.GetRequirements())
	}
}

// Registry credentials from the step's credentials: written for the
// request, removed after it.
func TestRegistryCredentials(t *testing.T) {
	f := newFunction(t)
	req := secretsRequest(t, map[string]any{"checkSecrets": false}, nil)
	req.Credentials = map[string]*fnv1.Credentials{RegistryCredentials: {Source: &fnv1.Credentials_CredentialData{
		CredentialData: &fnv1.CredentialData{Data: map[string][]byte{".dockerconfigjson": []byte(`{"auths": {"registry.example.com": {"auth": "dXNlcjpwYXNz"}}}`)}}}}}
	c, cleanup, err := f.registryFor(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Env) != 1 || !strings.HasPrefix(c.Env[0], "DOCKER_CONFIG=") {
		t.Fatalf("env: %v", c.Env)
	}
	dir := strings.TrimPrefix(c.Env[0], "DOCKER_CONFIG=")
	if b, err := os.ReadFile(dir + "/config.json"); err != nil || !strings.Contains(string(b), "registry.example.com") {
		t.Errorf("config.json: %v %s", err, b)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the credentials outlive the request: %v", err)
	}
	if f.registry.Env != nil {
		t.Errorf("the shared client was changed")
	}
	req.Credentials[RegistryCredentials] = &fnv1.Credentials{Source: &fnv1.Credentials_CredentialData{CredentialData: &fnv1.CredentialData{Data: map[string][]byte{"token": []byte("x")}}}}
	if _, _, err := f.registryFor(req); err == nil || !strings.Contains(err.Error(), "kubernetes.io/dockerconfigjson") {
		t.Errorf("credentials without a docker config: %v", err)
	}
}
