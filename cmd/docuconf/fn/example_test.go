package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"cuelabs.dev/go/oci/ociregistry/ocimem"
	"cuelabs.dev/go/oci/ociregistry/ociserver"
	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/resource"
	"sigs.k8s.io/yaml"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/oci"
)

// The example composition's own input and App, with the Deployment its
// first step declares, against a registry holding the orders contract.
func TestExample(t *testing.T) {
	var comp struct {
		Spec struct {
			Pipeline []struct {
				Step  string         `json:"step"`
				Input map[string]any `json:"input"`
			} `json:"pipeline"`
		} `json:"spec"`
	}
	b, err := os.ReadFile("example/composition.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, &comp); err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	for _, s := range comp.Spec.Pipeline {
		if s.Step == "configuration" {
			input = s.Input
		}
	}
	if input == nil {
		t.Fatal("no configuration step")
	}
	var app map[string]any
	b, err = os.ReadFile("example/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, &app); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(ociserver.New(ocimem.New(), nil))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	pushImage(t, host, "orders-api", "2.3.0")
	client := &oci.Client{}
	ref, _ := oci.ParseRef(host + "/orders-api:2.3.0")
	img, _, err := client.Push(context.Background(), ref, []byte(contractText(t)))
	if err != nil {
		t.Fatal(err)
	}
	app["spec"].(map[string]any)["image"] = host + "/orders-api:2.3.0"

	f, err := NewFunction(logging.NewNopLogger(), client)
	if err != nil {
		t.Fatal(err)
	}
	inJSON, _ := json.Marshal(input)
	appJSON, _ := json.Marshal(app)
	workload := `{"apiVersion": "apps/v1", "kind": "Deployment", "spec": {"selector": {"matchLabels": {"app": "orders"}},
	  "template": {"metadata": {"labels": {"app": "orders"}}, "spec": {"containers": [{"name": "app", "image": "` + host + `/orders-api:2.3.0"}]}}}}`
	run := func(app []byte) *fnv1.RunFunctionResponse {
		rsp, err := f.RunFunction(context.Background(), &fnv1.RunFunctionRequest{
			Input:    resource.MustStructJSON(string(inJSON)),
			Observed: &fnv1.State{Composite: &fnv1.Resource{Resource: resource.MustStructJSON(string(app))}},
			Desired:  &fnv1.State{Resources: map[string]*fnv1.Resource{"deployment": {Resource: resource.MustStructJSON(workload)}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return rsp
	}
	rsp := run(appJSON)
	if m := fatal(rsp); m != "" {
		t.Fatalf("the example App is rejected: %s", m)
	}
	if c := condition(t, rsp); c.GetReason() != "Valid" {
		t.Errorf("condition: %v", c)
	}
	d := desired(t, rsp, "deployment")
	cs := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	if c := cs[0].(map[string]any); c["image"] != img.String() || len(c["env"].([]any)) == 0 {
		t.Errorf("container: %v", c)
	}

	// The example's policy holds every secret to orders- Secrets.
	bad := strings.Replace(string(appJSON), `"orders-db"`, `"billing-db"`, 1)
	if msg := fatal(run([]byte(bad))); !strings.Contains(msg, "DATABASE_URL") {
		t.Errorf("the example policy should reject billing-db: %q", msg)
	}
}
