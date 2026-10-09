package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// testRegistry is an in-process OCI distribution registry, enough for
// oras-go: blobs, manifests by tag and digest, and, when referrers is set,
// the OCI 1.1 referrers API. Without it, the registry behaves like one
// that predates OCI 1.1: no OCI-Subject header and a 404 from
// /referrers, so clients fall back to the referrers tag schema.
type testRegistry struct {
	referrers bool

	mu        sync.Mutex
	blobs     map[string][]byte // repo@digest
	manifests map[string]manifestEntry
	tags      map[string]string // repo:tag -> digest
	requests  []string
}

type manifestEntry struct {
	mediaType string
	data      []byte
}

func newTestRegistry(t *testing.T, referrers bool) (*testRegistry, string) {
	r := &testRegistry{referrers: referrers, blobs: map[string][]byte{}, manifests: map[string]manifestEntry{}, tags: map[string]string{}}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_CONFIG", t.TempDir()) // no credentials from the host
	u, _ := url.Parse(srv.URL)
	return r, u.Host
}

func (r *testRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req.Method+" "+req.URL.Path)
	p := req.URL.Path
	if p == "/v2/" {
		return
	}
	for _, kind := range []string{"/manifests/", "/blobs/uploads/", "/blobs/", "/referrers/"} {
		i := strings.LastIndex(p, kind)
		if i < 0 || !strings.HasPrefix(p, "/v2/") {
			continue
		}
		repo, rest := p[len("/v2/"):i], p[i+len(kind):]
		switch kind {
		case "/manifests/":
			r.manifest(w, req, repo, rest)
		case "/blobs/uploads/":
			r.upload(w, req, repo)
		case "/blobs/":
			r.blob(w, req, repo, rest)
		case "/referrers/":
			r.listReferrers(w, req, repo, rest)
		}
		return
	}
	http.NotFound(w, req)
}

func (r *testRegistry) manifest(w http.ResponseWriter, req *http.Request, repo, ref string) {
	dgst := ref
	if !strings.HasPrefix(ref, "sha256:") {
		dgst = r.tags[repo+":"+ref]
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead:
		m, ok := r.manifests[repo+"@"+dgst]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`)
			return
		}
		w.Header().Set("Content-Type", m.mediaType)
		w.Header().Set("Docker-Content-Digest", dgst)
		w.Header().Set("Content-Length", fmt.Sprint(len(m.data)))
		if req.Method == http.MethodGet {
			w.Write(m.data)
		}
	case http.MethodPut:
		data, _ := io.ReadAll(req.Body)
		d := digest.FromBytes(data).String()
		r.manifests[repo+"@"+d] = manifestEntry{req.Header.Get("Content-Type"), data}
		if !strings.HasPrefix(ref, "sha256:") {
			r.tags[repo+":"+ref] = d
		}
		var m struct{ Subject *ocispec.Descriptor }
		if json.Unmarshal(data, &m) == nil && m.Subject != nil && r.referrers {
			w.Header().Set("OCI-Subject", m.Subject.Digest.String())
		}
		w.Header().Set("Docker-Content-Digest", d)
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		delete(r.manifests, repo+"@"+dgst)
		w.WriteHeader(http.StatusAccepted)
	}
}

func (r *testRegistry) upload(w http.ResponseWriter, req *http.Request, repo string) {
	switch req.Method {
	case http.MethodPost:
		w.Header().Set("Location", "/v2/"+repo+"/blobs/uploads/session")
		w.WriteHeader(http.StatusAccepted)
	case http.MethodPut:
		data, _ := io.ReadAll(req.Body)
		d := req.URL.Query().Get("digest")
		if digest.FromBytes(data).String() != d {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.blobs[repo+"@"+d] = data
		w.WriteHeader(http.StatusCreated)
	}
}

func (r *testRegistry) blob(w http.ResponseWriter, req *http.Request, repo, d string) {
	data, ok := r.blobs[repo+"@"+d]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.Header().Set("Docker-Content-Digest", d)
	w.Header().Set("Content-Type", "application/octet-stream")
	if req.Method == http.MethodGet {
		w.Write(data)
	}
}

func (r *testRegistry) listReferrers(w http.ResponseWriter, req *http.Request, repo, subject string) {
	if !r.referrers {
		http.NotFound(w, req)
		return
	}
	index := ocispec.Index{MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{}}
	index.SchemaVersion = 2
	for key, m := range r.manifests {
		repoOf, d, _ := strings.Cut(key, "@")
		var man ocispec.Manifest
		if repoOf != repo || json.Unmarshal(m.data, &man) != nil || man.Subject == nil || man.Subject.Digest.String() != subject {
			continue
		}
		index.Manifests = append(index.Manifests, ocispec.Descriptor{
			MediaType: m.mediaType, Digest: digest.Digest(d), Size: int64(len(m.data)),
			ArtifactType: man.ArtifactType, Annotations: man.Annotations,
		})
	}
	w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
	json.NewEncoder(w).Encode(index)
}

// addImage stores an image manifest and its config, tagged, and returns
// its digest.
func (r *testRegistry) addImage(t *testing.T, repo, tag string, labels map[string]string) string {
	t.Helper()
	cfg, _ := json.Marshal(ocispec.Image{Config: ocispec.ImageConfig{Labels: labels}})
	cfgDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromBytes(cfg), Size: int64(len(cfg))}
	m := ocispec.Manifest{MediaType: ocispec.MediaTypeImageManifest, Config: cfgDesc, Layers: []ocispec.Descriptor{}}
	m.SchemaVersion = 2
	data, _ := json.Marshal(m)
	d := digest.FromBytes(data).String()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.blobs[repo+"@"+cfgDesc.Digest.String()] = cfg
	r.manifests[repo+"@"+d] = manifestEntry{ocispec.MediaTypeImageManifest, data}
	r.tags[repo+":"+tag] = d
	return d
}

func (r *testRegistry) sawRequest(prefix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, req := range r.requests {
		if strings.HasPrefix(req, prefix) {
			return true
		}
	}
	return false
}

func TestPushPull(t *testing.T) {
	for _, referrers := range []bool{true, false} {
		t.Run(fmt.Sprintf("referrersAPI=%v", referrers), func(t *testing.T) {
			reg, host := newTestRegistry(t, referrers)
			img := reg.addImage(t, "team/orders", "1.4.0", nil)
			image := host + "/team/orders@" + img

			out, errOut, code := docuconf(t, "push", "--plain-http", "--image", image, ordersContract)
			if code != 0 {
				t.Fatalf("push: exit %d\n%s", code, errOut)
			}
			artifact := strings.TrimSpace(out)
			if !strings.HasPrefix(artifact, host+"/team/orders@sha256:") || !strings.Contains(errOut, "sign it with: cosign sign "+artifact) {
				t.Fatalf("push output:\n%s\n%s", out, errOut)
			}
			// Without the referrers API, oras-go keeps the index under the
			// referrers tag sha256-<hex>.
			fallbackTag := "PUT /v2/team/orders/manifests/sha256-" + strings.TrimPrefix(img, "sha256:")
			if reg.sawRequest(fallbackTag) == referrers {
				t.Fatalf("referrers tag schema used: %v, want %v", !referrers, !referrers)
			}

			// The artifact is what SPEC §8 says: the artifact type, the
			// image as subject, and contract.cue as its one layer.
			_, digestPart, _ := strings.Cut(artifact, "@")
			reg.mu.Lock()
			var m ocispec.Manifest
			json.Unmarshal(reg.manifests["team/orders@"+digestPart].data, &m)
			reg.mu.Unlock()
			if m.ArtifactType != contractArtifactType || m.Subject == nil || m.Subject.Digest.String() != img ||
				len(m.Layers) != 1 || m.Layers[0].MediaType != contractArtifactType ||
				m.Annotations[annotationContractName] != "orders-api" {
				t.Fatalf("artifact manifest: %+v", m)
			}

			// Pushing again finds the same artifact.
			out2, errOut, code := docuconf(t, "push", "--plain-http", "--image", image, ordersContract)
			if code != 0 || out2 != out || !strings.Contains(errOut, "already pushed") {
				t.Fatalf("second push: exit %d\n%s%s", code, out2, errOut)
			}

			// Pull by digest and by tag.
			want := read(t, ordersContract)
			for _, ref := range []string{image, host + "/team/orders:1.4.0"} {
				out, errOut, code := docuconf(t, "pull", "--plain-http", "--image", ref)
				if code != 0 || out != want {
					t.Fatalf("pull %s: exit %d\n%s", ref, code, errOut)
				}
			}
			path := t.TempDir() + "/contract.cue"
			if _, errOut, code := docuconf(t, "pull", "--plain-http", "--image", image, "-o", path); code != 0 || read(t, path) != want ||
				!strings.Contains(errOut, "orders-api: pulled the contract for "+host+"/team/orders@"+img+" from artifact sha256:") {
				t.Fatalf("pull -o: exit %d\n%s", code, errOut)
			}

			// A newer contract for the same image wins.
			newer := ordersModified(t)
			if _, errOut, code := docuconf(t, "push", "--plain-http", "--image", image, newer); code != 0 {
				t.Fatalf("push newer: %s", errOut)
			}
			out, errOut, code = docuconf(t, "pull", "--plain-http", "--image", image)
			if code != 0 || out != read(t, newer) || !strings.Contains(errOut, "2 contracts refer to this image; using the newest") {
				t.Fatalf("pull newest: exit %d\n%s", code, errOut)
			}
		})
	}
}

func TestPullLabelAndMissing(t *testing.T) {
	reg, host := newTestRegistry(t, true)
	src := read(t, ordersContract)
	labelled := reg.addImage(t, "team/orders", "labelled", map[string]string{contractLabel: base64.StdEncoding.EncodeToString([]byte(src))})
	bare := reg.addImage(t, "team/orders", "bare", nil)

	out, errOut, code := docuconf(t, "pull", "--plain-http", "--image", host+"/team/orders@"+labelled)
	if code != 0 || out != src || !strings.Contains(errOut, "from image label dev.docuconf.contract") {
		t.Fatalf("label: exit %d\n%s", code, errOut)
	}

	_, errOut, code = docuconf(t, "pull", "--plain-http", "--image", host+"/team/orders:bare")
	if code != 1 || !strings.Contains(errOut, "no contract found for "+bare) {
		t.Fatalf("missing: exit %d\n%s", code, errOut)
	}
}

func TestPushPullErrors(t *testing.T) {
	reg, host := newTestRegistry(t, true)
	reg.addImage(t, "team/orders", "1.4.0", nil)
	bad := write(t, "contract.cue", contractOf(`vars: port: {type: "int", description: "lowercase"}`))
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"push", "--image", host + "/team/orders:1.4.0", ordersContract}, "push needs the image's digest"},
		{[]string{"push", ordersContract}, "--image is required"},
		{[]string{"push", "--image", host + "/team/orders@sha256:" + strings.Repeat("0", 64)}, "want one contract.cue"},
		{[]string{"push", "--image", host + "/team/orders:1.4.0", "x.json"}, "push the contract.cue the SDK exported"},
		{[]string{"push", "--plain-http", "--image", host + "/team/orders@sha256:" + strings.Repeat("0", 64), bad}, "is not a valid contract"},
		{[]string{"push", "--plain-http", "--image", host + "/team/orders@sha256:" + strings.Repeat("0", 64), ordersContract}, "not found"},
		{[]string{"pull", "--image", "orders"}, "invalid reference"},
		{[]string{"pull", "--image", host + "/team/orders@sha256:1", "extra"}, "unexpected arguments: extra"},
	} {
		_, errOut, code := docuconf(t, tc.args...)
		if code != 2 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: exit %d, stderr:\n%s\nwant %q", tc.args, code, errOut, tc.want)
		}
	}
}
