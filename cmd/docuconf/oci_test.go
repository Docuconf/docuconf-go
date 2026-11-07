package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cuelabs.dev/go/oci/ociregistry"
	"cuelabs.dev/go/oci/ociregistry/ociclient"
	"cuelabs.dev/go/oci/ociregistry/ocimem"
	"cuelabs.dev/go/oci/ociregistry/ociserver"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/oci"
)

// testRegistry serves an in-memory registry over HTTP on 127.0.0.1, which
// docuconf reaches without TLS, and returns its host and a client.
func testRegistry(t *testing.T, referrersAPI bool) (string, ociregistry.Interface) {
	t.Helper()
	srv := httptest.NewServer(ociserver.New(ocimem.New(), &ociserver.Options{DisableReferrersAPI: !referrersAPI}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	c, err := ociclient.New(host, &ociclient.Options{Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	return host, c
}

func pushBlob(t *testing.T, reg ociregistry.Interface, repo, mediaType string, data []byte) ociregistry.Descriptor {
	t.Helper()
	d := ociregistry.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
	if _, err := reg.PushBlob(context.Background(), repo, d, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	return d
}

func pushJSON(t *testing.T, reg ociregistry.Interface, repo, tag, mediaType string, v any) ociregistry.Descriptor {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	d, err := reg.PushManifest(context.Background(), repo, tag, data, mediaType)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// pushImage pushes a one-layer image whose config has labels, tagged tag.
func pushImage(t *testing.T, reg ociregistry.Interface, repo, tag string, labels map[string]string) ociregistry.Descriptor {
	t.Helper()
	cfg, _ := json.Marshal(map[string]any{"architecture": "amd64", "os": "linux", "config": map[string]any{"Labels": labels}})
	m := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    pushBlob(t, reg, repo, ocispec.MediaTypeImageConfig, cfg),
		Layers:    []ociregistry.Descriptor{pushBlob(t, reg, repo, ocispec.MediaTypeImageLayerGzip, []byte(tag))},
	}
	m.SchemaVersion = 2
	return pushJSON(t, reg, repo, tag, ocispec.MediaTypeImageManifest, m)
}

func referrers(t *testing.T, reg ociregistry.Interface, repo string, d digest.Digest) []ociregistry.Descriptor {
	t.Helper()
	got, err := ociregistry.All(reg.Referrers(context.Background(), repo, d, oci.ArtifactType))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPushPull(t *testing.T) {
	for _, api := range []bool{true, false} {
		name := map[bool]string{true: "referrers_api", false: "referrers_tag"}[api]
		t.Run(name, func(t *testing.T) {
			host, reg := testRegistry(t, api)
			img := pushImage(t, reg, "orders", "1.4.0", nil)
			pinned := host + "/orders@" + string(img.Digest)

			stdout, stderr, code := docuconf(t, "push", "-contract", ordersContract, host+"/orders:1.4.0")
			if code != 0 {
				t.Fatalf("push: exit %d: %s", code, stderr)
			}
			if stdout != "oci://"+pinned+"\n" {
				t.Errorf("push printed %q, want the pinned reference oci://%s", stdout, pinned)
			}
			// Pushing again stores nothing new.
			if _, stderr, code := docuconf(t, "push", host+"/orders@"+string(img.Digest), "-contract", ordersContract); code != 0 {
				t.Fatalf("push again: exit %d: %s", code, stderr)
			}
			if got := referrers(t, reg, "orders", img.Digest); len(got) != 1 {
				t.Errorf("after two pushes, want one referrer, got %v", got)
			}

			out := filepath.Join(t.TempDir(), "contract.cue")
			if _, stderr, code := docuconf(t, "pull", "oci://"+host+"/orders:1.4.0", "-o", out); code != 0 {
				t.Fatalf("pull: exit %d: %s", code, stderr)
			} else if !strings.Contains(stderr, "from its referrer sha256:") {
				t.Errorf("pull should say it read the referrer, got %q", stderr)
			}
			// The pulled contract is the same contract: vet of the file and
			// of the image agree, and so do the generated modules.
			want, _, _ := docuconf(t, "docs", ordersContract, "--format", "model")
			got, stderr, code := docuconf(t, "docs", out, "--format", "model")
			if code != 0 || got != want {
				t.Errorf("the pulled contract's docs model differs (exit %d, %s)", code, stderr)
			}
			if got, _, _ := docuconf(t, "docs", "oci://"+pinned, "--format", "model"); got != want {
				t.Errorf("docs of oci://%s differs from the file's", pinned)
			}
			values := write(t, "values.yaml", "LOG_LEVEL: verbose\n")
			wantOut, wantErr, wantCode := docuconf(t, "vet", "-contract", ordersContract, "-values", values)
			gotOut, gotErr, gotCode := docuconf(t, "vet", "-contract", "oci://"+pinned, "-values", values)
			if gotCode != wantCode || gotOut != wantOut || gotErr != wantErr {
				t.Errorf("vet -contract oci:// differs from the file's: exit %d, want %d\n%s%s\nwant\n%s%s", gotCode, wantCode, gotOut, gotErr, wantOut, wantErr)
			}
		})
	}
}

func TestOCIContractRecordsImage(t *testing.T) {
	host, reg := testRegistry(t, true)
	img := pushImage(t, reg, "team/orders", "1.4.0", nil)
	if _, stderr, code := docuconf(t, "push", "-contract", ordersContract, host+"/team/orders:1.4.0"); code != 0 {
		t.Fatalf("push: exit %d: %s", code, stderr)
	}
	pinned := host + "/team/orders@" + string(img.Digest)

	fromFile := t.TempDir()
	if _, stderr, code := docuconf(t, "terraform", "-contract", ordersContract, "-target", "ecs", "-out", fromFile); code != 0 {
		t.Fatal(stderr)
	}
	dir := t.TempDir()
	if _, stderr, code := docuconf(t, "terraform", "-contract", "oci://"+host+"/team/orders:1.4.0", "-target", "ecs", "-out", dir); code != 0 {
		t.Fatalf("terraform -contract oci://: exit %d: %s", code, stderr)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dir, "docuconf.json"))), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["image"] != pinned {
		t.Errorf("docuconf.json image = %v, want %s", meta["image"], pinned)
	}
	// The same contract, so the same digest: lint -contract and a policy
	// pack from the file agree with a module from the image.
	var fileMeta map[string]any
	if err := json.Unmarshal([]byte(read(t, filepath.Join(fromFile, "docuconf.json"))), &fileMeta); err != nil {
		t.Fatal(err)
	}
	if meta["contractDigest"] != fileMeta["contractDigest"] {
		t.Errorf("contractDigest from the image %v, from the file %v", meta["contractDigest"], fileMeta["contractDigest"])
	}
	if outputs := read(t, filepath.Join(dir, "outputs.tf")); !strings.Contains(outputs, `image  = "`+pinned+`"`) {
		t.Errorf("the contract output does not record the image:\n%s", outputs)
	}
	if strings.Contains(read(t, filepath.Join(fromFile, "outputs.tf")), "image  =") || strings.Contains(read(t, filepath.Join(fromFile, "docuconf.json")), `"image"`) {
		t.Errorf("a module generated from a file records an image")
	}
	// The module is otherwise the one the file generates.
	for _, f := range []string{"variables.tf", "render.tf"} {
		if read(t, filepath.Join(dir, f)) != read(t, filepath.Join(fromFile, f)) {
			t.Errorf("%s differs between the file and the image's contract", f)
		}
	}
	// -check compares against the image's contract too.
	if _, stderr, code := docuconf(t, "terraform", "-contract", "oci://"+pinned, "-target", "ecs", "-out", dir, "-check"); code != 0 {
		t.Errorf("terraform -check of the same image: exit %d: %s", code, stderr)
	}

	comp := t.TempDir()
	if _, stderr, code := docuconf(t, "pulumi", "-contract", "oci://"+pinned, "-lang", "typescript", "-target", "ecs", "-out", comp); code != 0 {
		t.Fatalf("pulumi -contract oci://: exit %d: %s", code, stderr)
	}
	if !strings.Contains(read(t, filepath.Join(comp, "docuconf.json")), `"image": "`+pinned+`"`) {
		t.Errorf("the component's docuconf.json does not record the image")
	}
}

func TestPullLabel(t *testing.T) {
	host, reg := testRegistry(t, true)
	src, err := os.ReadFile(ordersContract)
	if err != nil {
		t.Fatal(err)
	}
	label := map[string]string{oci.Label: base64.StdEncoding.EncodeToString(src)}

	// A single image, and a multi-platform index whose images carry the
	// same label beside an attestation, which has none.
	pushImage(t, reg, "orders", "single", label)
	amd := pushImage(t, reg, "orders", "amd64", label)
	arm := pushImage(t, reg, "orders", "arm64", label)
	att := pushImage(t, reg, "orders", "att", nil)
	amd.Platform = &ocispec.Platform{OS: "linux", Architecture: "amd64"}
	arm.Platform = &ocispec.Platform{OS: "linux", Architecture: "arm64"}
	att.Platform = &ocispec.Platform{OS: "unknown", Architecture: "unknown"}
	index := ocispec.Index{MediaType: ocispec.MediaTypeImageIndex, Manifests: []ociregistry.Descriptor{amd, arm, att}}
	index.SchemaVersion = 2
	pushJSON(t, reg, "orders", "multi", ocispec.MediaTypeImageIndex, index)

	for _, tag := range []string{"single", "multi"} {
		stdout, stderr, code := docuconf(t, "pull", host+"/orders:"+tag)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", tag, code, stderr)
		}
		if stdout != string(src) || !strings.Contains(stderr, "from its label") {
			t.Errorf("%s: want the label's contract, got stderr %q", tag, stderr)
		}
	}

	// A referrer wins over the label.
	if _, stderr, code := docuconf(t, "push", "-contract", ordersContract, host+"/orders:multi"); code != 0 {
		t.Fatal(stderr)
	}
	if _, stderr, _ := docuconf(t, "pull", host+"/orders:multi"); !strings.Contains(stderr, "referrer") {
		t.Errorf("want the referrer, got %q", stderr)
	}

	// Platform images that disagree.
	other := pushImage(t, reg, "orders", "other", map[string]string{oci.Label: base64.StdEncoding.EncodeToString([]byte("x"))})
	other.Platform = &ocispec.Platform{OS: "linux", Architecture: "arm64"}
	mixed := ocispec.Index{MediaType: ocispec.MediaTypeImageIndex, Manifests: []ociregistry.Descriptor{amd, other}}
	mixed.SchemaVersion = 2
	pushJSON(t, reg, "orders", "mixed", ocispec.MediaTypeImageIndex, mixed)
	if _, stderr, code := docuconf(t, "pull", host+"/orders:mixed"); code != 2 || !strings.Contains(stderr, "have different dev.docuconf.contract labels") {
		t.Errorf("mixed labels: exit %d: %s", code, stderr)
	}
}

func TestPullErrors(t *testing.T) {
	host, reg := testRegistry(t, true)
	img := pushImage(t, reg, "orders", "bare", nil)
	if _, stderr, code := docuconf(t, "pull", host+"/orders:bare"); code != 2 || !strings.Contains(stderr, "has no contract: no application/vnd.docuconf.contract.v1alpha1+cue referrer, and no dev.docuconf.contract label (push one with docuconf push)") {
		t.Errorf("no contract: exit %d: %s", code, stderr)
	}
	if _, stderr, code := docuconf(t, "terraform", "-contract", "oci://"+host+"/orders:missing", "-out", t.TempDir()); code != 2 || !strings.Contains(stderr, "resolving "+host+"/orders:missing") {
		t.Errorf("unknown tag: exit %d: %s", code, stderr)
	}
	if _, stderr, code := docuconf(t, "pull", "Not A Ref"); code != 2 || !strings.Contains(stderr, "is not an image reference") {
		t.Errorf("bad reference: exit %d: %s", code, stderr)
	}
	if _, _, code := docuconf(t, "pull"); code != 0 && code != 2 {
		t.Errorf("pull with no image: exit %d", code)
	}

	// Two different contracts attached to one image.
	if _, stderr, code := docuconf(t, "push", "-contract", ordersContract, host+"/orders:bare"); code != 0 {
		t.Fatal(stderr)
	}
	if _, stderr, code := docuconf(t, "push", "-contract", billing, host+"/orders:bare"); code != 0 {
		t.Fatal(stderr)
	}
	if _, stderr, code := docuconf(t, "pull", host+"/orders@"+string(img.Digest)); code != 2 || !strings.Contains(stderr, "has 2 different contracts attached") {
		t.Errorf("two contracts: exit %d: %s", code, stderr)
	}

	// An invalid contract is not pushed.
	bad := write(t, "contract.cue", "apiVersion: \"docuconf.dev/v1alpha1\"\nkind: \"Contract\"\nmetadata: name: \"Bad Name\"\n")
	if _, _, code := docuconf(t, "push", "-contract", bad, host+"/orders:bare"); code == 0 {
		t.Errorf("an invalid contract was pushed")
	}
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string]string{
		"orders":                "docker.io/library/orders:latest",
		"oci://acme/orders:1":   "docker.io/acme/orders:1",
		"ghcr.io/acme/orders:1": "ghcr.io/acme/orders:1",
		"localhost:5000/orders@sha256:" + strings.Repeat("a", 64): "localhost:5000/orders@sha256:" + strings.Repeat("a", 64),
	} {
		r, err := oci.ParseRef(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if r.String() != want {
			t.Errorf("%s: got %s, want %s", in, r, want)
		}
	}
}
