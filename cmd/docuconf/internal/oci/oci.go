// Package oci distributes contracts with the images they describe
// (spec/SPEC.md section 8): a contract is pushed as an OCI artifact whose
// subject is the image, found again through the referrers API, or through
// the referrers tag on registries without it; failing both, it is read
// from the image's dev.docuconf.contract label.
package oci

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"cuelabs.dev/go/oci/ociregistry"
	"cuelabs.dev/go/oci/ociregistry/ociauth"
	"cuelabs.dev/go/oci/ociregistry/ociclient"
	"cuelabs.dev/go/oci/ociregistry/ociref"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	// ArtifactType is a contract artifact's type, and its layer's media type.
	ArtifactType = "application/vnd.docuconf.contract.v1alpha1+cue"
	// Label is the image label that holds a contract, base64-encoded, for
	// registries that cannot store referrers.
	Label = "dev.docuconf.contract"
	// Scheme prefixes an OCI reference where a contract file is expected.
	Scheme = "oci://"

	emptyJSON = "application/vnd.oci.empty.v1+json"
	// The most a contract may be: a contract is a few kilobytes, and
	// nothing read from a registry is trusted to be small.
	maxContract = 4 << 20
)

// Image is an image reference resolved to a digest.
type Image struct {
	Host, Repository string
	Digest           digest.Digest
	// The descriptor the reference resolved to: a manifest or an index.
	Descriptor ociregistry.Descriptor
}

// String is the image's pinned reference, HOST/REPOSITORY@DIGEST, as a
// container's image field would name it.
func (i Image) String() string { return i.Host + "/" + i.Repository + "@" + string(i.Digest) }

// Ref is a parsed image reference, before it is resolved.
type Ref struct{ ociref.Reference }

// ParseRef parses an image reference, with or without oci://, as docker
// pull reads it: a name without a registry is on Docker Hub, and a Docker
// Hub name without a namespace is in library/.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimPrefix(s, Scheme)
	r, err := ociref.ParseRelative(s)
	if err != nil {
		return Ref{}, fmt.Errorf("%s is not an image reference: %v", s, err)
	}
	if r.Host == "" {
		if first, _, ok := strings.Cut(s, "/"); ok && first == "localhost" {
			return Ref{}, fmt.Errorf("%s is not an image reference", s)
		}
		r.Host = "docker.io"
	}
	if r.Host == "docker.io" && !strings.Contains(r.Repository, "/") {
		r.Repository = "library/" + r.Repository
	}
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest"
	}
	return Ref{r}, nil
}

// Client reaches registries with the credentials docker login stored.
type Client struct {
	// Registry, if set, is used for every host instead of a remote
	// registry (in tests).
	Registry ociregistry.Interface
	// Transport, if set, carries the HTTP requests (in tests).
	Transport http.RoundTripper
}

// apiHost is where a registry's API is served.
func apiHost(host string) string {
	if host == "docker.io" {
		return "registry-1.docker.io"
	}
	return host
}

// insecure says whether a host is reached over plain HTTP: only a
// registry on this machine.
func insecure(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.HasSuffix(h, "]") {
		h = h[:i]
	}
	return h == "localhost" || h == "127.0.0.1" || h == "[::1]"
}

func (c *Client) registry(host string) (ociregistry.Interface, error) {
	if c.Registry != nil {
		return c.Registry, nil
	}
	cfg, err := ociauth.Load(nil)
	if err != nil {
		return nil, fmt.Errorf("reading registry credentials: %v", err)
	}
	return ociclient.New(apiHost(host), &ociclient.Options{
		Insecure:  insecure(host),
		Transport: ociauth.NewStdTransport(ociauth.StdTransportParams{Config: hubConfig{cfg}, Transport: c.Transport}),
	})
}

// hubConfig finds Docker Hub's credentials, which docker login stores
// under index.docker.io, for its API host.
type hubConfig struct{ ociauth.Config }

func (c hubConfig) EntryForRegistry(host string) (ociauth.ConfigEntry, error) {
	if host == "registry-1.docker.io" {
		if e, err := c.Config.EntryForRegistry("index.docker.io"); err != nil || e != (ociauth.ConfigEntry{}) {
			return e, err
		}
	}
	return c.Config.EntryForRegistry(host)
}

// Resolve finds the digest an image reference names.
func (c *Client) Resolve(ctx context.Context, ref Ref) (Image, ociregistry.Interface, error) {
	reg, err := c.registry(ref.Host)
	if err != nil {
		return Image{}, nil, err
	}
	var desc ociregistry.Descriptor
	if ref.Digest != "" {
		desc, err = reg.ResolveManifest(ctx, ref.Repository, ref.Digest)
	} else {
		desc, err = reg.ResolveTag(ctx, ref.Repository, ref.Tag)
	}
	if err != nil {
		return Image{}, nil, fmt.Errorf("resolving %s: %v", ref, err)
	}
	return Image{Host: ref.Host, Repository: ref.Repository, Digest: desc.Digest, Descriptor: desc}, reg, nil
}

// Push stores contract (its CUE source, or its JSON, which is CUE too) as
// an artifact whose subject is the image ref names, and returns the
// image and the artifact's descriptor. Pushing the same contract again
// stores nothing new.
func (c *Client) Push(ctx context.Context, ref Ref, contract []byte) (Image, ociregistry.Descriptor, error) {
	img, reg, err := c.Resolve(ctx, ref)
	if err != nil {
		return Image{}, ociregistry.Descriptor{}, err
	}
	repo := img.Repository
	config, err := pushBlob(ctx, reg, repo, emptyJSON, []byte("{}"))
	if err != nil {
		return img, ociregistry.Descriptor{}, err
	}
	layer, err := pushBlob(ctx, reg, repo, ArtifactType, contract)
	if err != nil {
		return img, ociregistry.Descriptor{}, err
	}
	layer.Annotations = map[string]string{ocispec.AnnotationTitle: "contract.cue"}
	subject := ociregistry.Descriptor{MediaType: img.Descriptor.MediaType, Digest: img.Digest, Size: img.Descriptor.Size}
	m := ocispec.Manifest{
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ArtifactType,
		Config:       config,
		Layers:       []ociregistry.Descriptor{layer},
		Subject:      &subject,
	}
	m.SchemaVersion = 2
	body, err := json.Marshal(m)
	if err != nil {
		return img, ociregistry.Descriptor{}, err
	}
	desc, err := reg.PushManifest(ctx, repo, "", body, ocispec.MediaTypeImageManifest)
	if err != nil {
		return img, ociregistry.Descriptor{}, fmt.Errorf("pushing the contract to %s/%s: %v", img.Host, repo, err)
	}
	desc.ArtifactType = ArtifactType
	if err := c.indexReferrer(ctx, reg, img, desc); err != nil {
		return img, desc, err
	}
	return img, desc, nil
}

// indexReferrer makes sure the referrers of img list desc: a registry
// with the referrers API lists it by itself; on one without, it goes in
// the index at the referrers tag, as the distribution spec says a client
// must do.
func (c *Client) indexReferrer(ctx context.Context, reg ociregistry.Interface, img Image, desc ociregistry.Descriptor) error {
	for d, err := range reg.Referrers(ctx, img.Repository, img.Digest, ArtifactType) {
		if err != nil {
			break // write the tag: it is what a client falls back to
		}
		if d.Digest == desc.Digest {
			return nil
		}
	}
	tag := strings.Replace(string(img.Digest), ":", "-", 1)
	index := ocispec.Index{MediaType: ocispec.MediaTypeImageIndex}
	index.SchemaVersion = 2
	if r, err := reg.GetTag(ctx, img.Repository, tag); err == nil {
		data, err := io.ReadAll(io.LimitReader(r, maxContract))
		r.Close()
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &index); err != nil {
			return fmt.Errorf("the referrers tag %s of %s/%s is not an index: %v", tag, img.Host, img.Repository, err)
		}
	} else if !errors.Is(err, ociregistry.ErrManifestUnknown) && !errors.Is(err, ociregistry.ErrNameUnknown) {
		return fmt.Errorf("reading the referrers tag %s of %s/%s: %v", tag, img.Host, img.Repository, err)
	}
	for _, d := range index.Manifests {
		if d.Digest == desc.Digest {
			return nil
		}
	}
	index.Manifests = append(index.Manifests, desc)
	body, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if _, err := reg.PushManifest(ctx, img.Repository, tag, body, ocispec.MediaTypeImageIndex); err != nil {
		return fmt.Errorf("updating the referrers tag %s of %s/%s: %v", tag, img.Host, img.Repository, err)
	}
	return nil
}

func pushBlob(ctx context.Context, reg ociregistry.Interface, repo, mediaType string, data []byte) (ociregistry.Descriptor, error) {
	desc := ociregistry.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
	if _, err := reg.ResolveBlob(ctx, repo, desc.Digest); err == nil {
		return desc, nil
	}
	if _, err := reg.PushBlob(ctx, repo, desc, bytes.NewReader(data)); err != nil {
		return desc, fmt.Errorf("pushing to %s: %v", repo, err)
	}
	return desc, nil
}

// Pulled is a contract read from a registry.
type Pulled struct {
	Image    Image
	Contract []byte
	// From says where it was found: "referrer" (with the artifact's
	// digest) or "label".
	From string
}

// Pull finds the contract of the image ref names.
func (c *Client) Pull(ctx context.Context, ref Ref) (Pulled, error) {
	img, reg, err := c.Resolve(ctx, ref)
	if err != nil {
		return Pulled{}, err
	}
	var found []ociregistry.Descriptor
	for d, err := range reg.Referrers(ctx, img.Repository, img.Digest, ArtifactType) {
		if err != nil {
			return Pulled{}, fmt.Errorf("listing the referrers of %s: %v", img, err)
		}
		if d.ArtifactType == ArtifactType {
			found = append(found, d)
		}
	}
	var contracts [][]byte
	for _, d := range found {
		data, err := readContractArtifact(ctx, reg, img.Repository, d)
		if err != nil {
			return Pulled{}, fmt.Errorf("reading the contract %s of %s: %v", d.Digest, img, err)
		}
		if !containsBytes(contracts, data) {
			contracts = append(contracts, data)
		}
	}
	switch len(contracts) {
	case 1:
		return Pulled{Image: img, Contract: contracts[0], From: "referrer " + string(found[0].Digest)}, nil
	case 0:
	default:
		return Pulled{}, fmt.Errorf("%s has %d different contracts attached; an image has one contract, so delete the stale ones", img, len(contracts))
	}
	data, err := readLabel(ctx, reg, img)
	if err != nil {
		return Pulled{}, err
	}
	if data == nil {
		return Pulled{}, fmt.Errorf("%s has no contract: no %s referrer, and no %s label (push one with docuconf push)", img, ArtifactType, Label)
	}
	return Pulled{Image: img, Contract: data, From: "label"}, nil
}

func containsBytes(list [][]byte, b []byte) bool {
	for _, x := range list {
		if bytes.Equal(x, b) {
			return true
		}
	}
	return false
}

func readAll(r io.ReadCloser, limit int64) ([]byte, error) {
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	return data, nil
}

func readContractArtifact(ctx context.Context, reg ociregistry.Interface, repo string, d ociregistry.Descriptor) ([]byte, error) {
	r, err := reg.GetManifest(ctx, repo, d.Digest)
	if err != nil {
		return nil, err
	}
	data, err := readAll(r, maxContract)
	if err != nil {
		return nil, err
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	var layer *ociregistry.Descriptor
	for i := range m.Layers {
		if m.Layers[i].MediaType == ArtifactType {
			if layer != nil {
				return nil, fmt.Errorf("the artifact has more than one contract layer")
			}
			layer = &m.Layers[i]
		}
	}
	if layer == nil {
		return nil, fmt.Errorf("the artifact has no %s layer", ArtifactType)
	}
	if layer.Size > maxContract {
		return nil, fmt.Errorf("the contract is %d bytes, more than %d", layer.Size, maxContract)
	}
	br, err := reg.GetBlob(ctx, repo, layer.Digest)
	if err != nil {
		return nil, err
	}
	b, err := readAll(br, maxContract)
	if err != nil {
		return nil, err
	}
	if digest.FromBytes(b) != layer.Digest {
		return nil, fmt.Errorf("the contract does not match its digest %s", layer.Digest)
	}
	return b, nil
}

// readLabel reads the contract from the image config's label; for an
// index, from its platform images, which must agree. It returns nil if
// there is no label.
func readLabel(ctx context.Context, reg ociregistry.Interface, img Image) ([]byte, error) {
	manifests := []ociregistry.Descriptor{img.Descriptor}
	if isIndex(img.Descriptor.MediaType) {
		var index ocispec.Index
		if err := getJSON(ctx, reg, img.Repository, img.Digest, true, &index); err != nil {
			return nil, err
		}
		manifests = nil
		for _, d := range index.Manifests {
			// Attestations and other artifacts in the index have no
			// runnable platform.
			if d.Platform != nil && d.Platform.OS == "unknown" {
				continue
			}
			if !isIndex(d.MediaType) {
				manifests = append(manifests, d)
			}
		}
	}
	var label *string
	for _, d := range manifests {
		var m ocispec.Manifest
		if err := getJSON(ctx, reg, img.Repository, d.Digest, true, &m); err != nil {
			return nil, err
		}
		var cfg struct {
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"config"`
		}
		if err := getJSON(ctx, reg, img.Repository, m.Config.Digest, false, &cfg); err != nil {
			return nil, err
		}
		v, ok := cfg.Config.Labels[Label]
		if !ok {
			v = ""
		}
		if label != nil && *label != v {
			return nil, fmt.Errorf("the platform images of %s have different %s labels", img, Label)
		}
		label = &v
	}
	if label == nil || *label == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*label))
	if err != nil {
		return nil, fmt.Errorf("the %s label of %s is not base64: %v", Label, img, err)
	}
	return data, nil
}

func isIndex(mediaType string) bool {
	return mediaType == ocispec.MediaTypeImageIndex || mediaType == "application/vnd.docker.distribution.manifest.list.v2+json"
}

func getJSON(ctx context.Context, reg ociregistry.Interface, repo string, d digest.Digest, manifest bool, v any) error {
	var r ociregistry.BlobReader
	var err error
	if manifest {
		r, err = reg.GetManifest(ctx, repo, d)
	} else {
		r, err = reg.GetBlob(ctx, repo, d)
	}
	if err != nil {
		return err
	}
	data, err := readAll(r, maxContract)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
