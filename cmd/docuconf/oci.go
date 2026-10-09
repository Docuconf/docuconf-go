package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// contractArtifactType is the OCI artifact type of a contract pushed beside
// its image (SPEC §8). It is also the media type of the one layer, which
// holds contract.cue as the SDK exported it. The format freeze changes
// v1alpha1 to v1beta1 here, and nowhere else.
const contractArtifactType = "application/vnd.docuconf.contract.v1alpha1+cue"

// contractLabel is the image label fallback of SPEC §8: the contract,
// base64-encoded, for registries without referrers support.
const contractLabel = "dev.docuconf.contract"

// annotationContractName records metadata.name on the artifact manifest.
const annotationContractName = "dev.docuconf.contract.name"

// errNoContract means pull found no contract for the image.
var errNoContract = errors.New("no contract found")

const pushUsage = `Usage: docuconf push --image <registry/repo@sha256:...> [--plain-http] <contract.cue>

Pushes a contract as an OCI artifact (type ` + contractArtifactType + `)
whose subject is the image, by the OCI 1.1 referrers API. On a registry
without it, the referrers tag schema is used instead (a sha256-<digest> tag
holding an index of referrers), as oras does.

The image must be given by digest, so the contract is tied to one build.
Credentials come from the Docker config ($DOCKER_CONFIG/config.json or
~/.docker/config.json) and its credential helpers, as for docker login.

The artifact's reference (registry/repo@sha256:...) is printed on standard
output; sign it like the image:

  cosign sign $(docuconf push --image ... contract.cue)

`

const pullUsage = `Usage: docuconf pull --image <registry/repo@sha256:... | registry/repo:tag> [-o contract.cue] [--plain-http]

Fetches the contract pushed for an image (docuconf push) and checks that it
is a valid contract. A tag is resolved to a digest first. When no artifact
refers to the image, the image label ` + contractLabel + ` is read instead.
The contract goes to -o, or to standard output. Exits 1 when the image has
no contract.

`

type ociFlags struct {
	image     string
	plainHTTP bool
}

func (f *ociFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.image, "image", "", "the image, as registry/repo@sha256:... (pull also takes registry/repo:tag)")
	fs.BoolVar(&f.plainHTTP, "plain-http", false, "talk to the registry over HTTP instead of HTTPS (a local test registry)")
}

// httpClient is the client used for registries; tests replace it.
var httpClient = retry.DefaultClient

// repository opens the image's repository with credentials from the
// Docker config and its credential helpers.
func (f *ociFlags) repository() (*remote.Repository, registry.Reference, error) {
	if f.image == "" {
		return nil, registry.Reference{}, errors.New("--image is required")
	}
	ref, err := registry.ParseReference(f.image)
	if err != nil {
		return nil, registry.Reference{}, fmt.Errorf("--image %q: %w", f.image, err)
	}
	if ref.Reference == "" {
		return nil, ref, fmt.Errorf("--image %q has no tag or digest", f.image)
	}
	repo, err := remote.NewRepository(ref.Registry + "/" + ref.Repository)
	if err != nil {
		return nil, ref, err
	}
	if ref.Registry == "docker.io" {
		repo.Reference.Registry = "registry-1.docker.io"
	}
	repo.PlainHTTP = f.plainHTTP
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return nil, ref, fmt.Errorf("docker config: %w", err)
	}
	client := &auth.Client{
		Client:     httpClient,
		Cache:      auth.NewCache(),
		Credential: credentials.Credential(store),
		Header:     http.Header{"User-Agent": {"docuconf"}},
	}
	repo.Client = client
	return repo, ref, nil
}

// parseOne parses flags given before or after one positional argument.
func parseOne(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func interruptible() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func runPush(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, pushUsage)
		fs.PrintDefaults()
	}
	var of ociFlags
	of.register(fs)
	positional, err := parseOne(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		fs.Usage()
		return fmt.Errorf("want one contract.cue, got %d arguments", len(positional))
	}
	file := positional[0]
	if !strings.EqualFold(filepath.Ext(file), ".cue") {
		return fmt.Errorf("%s: push the contract.cue the SDK exported; the artifact type is %s", file, contractArtifactType)
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	p, err := platform.New()
	if err != nil {
		return err
	}
	c, err := p.ParseContract(file, src)
	if err != nil {
		return err
	}

	repo, ref, err := of.repository()
	if err != nil {
		return err
	}
	if _, err := ref.Digest(); err != nil {
		return fmt.Errorf("--image %q: push needs the image's digest (registry/repo@sha256:...), so the contract is tied to one build", of.image)
	}
	ctx, cancel := interruptible()
	defer cancel()
	subject, err := repo.Resolve(ctx, ref.Reference)
	if err != nil {
		return fmt.Errorf("image %s: %w", of.image, err)
	}

	layer := content.NewDescriptorFromBytes(contractArtifactType, src)
	layer.Annotations = map[string]string{ocispec.AnnotationTitle: "contract.cue"}

	// Pushing the same contract twice finds the first artifact instead of
	// adding a second referrer.
	existing, err := contractReferrers(ctx, repo, subject)
	if err != nil {
		return err
	}
	for _, d := range existing {
		m, err := fetchManifest(ctx, repo, d)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(m.Layers, func(l ocispec.Descriptor) bool { return l.Digest == layer.Digest }) {
			fmt.Fprintf(stderr, "%s: this contract is already pushed for %s\n", c.Name, of.image)
			printPushed(stdout, stderr, ref, d.Digest)
			return nil
		}
	}

	// The artifact is packed in memory and then copied, so its digest is
	// known even when the copy reports an error after the manifest is in.
	staged := memory.New()
	if err := staged.Push(ctx, layer, bytes.NewReader(src)); err != nil {
		return err
	}
	annotations := map[string]string{
		annotationContractName: c.Name,
		// oras-go would write whole seconds; pull picks the newest of
		// several contracts by this, so keep the sub-second part.
		ocispec.AnnotationCreated: time.Now().UTC().Format(time.RFC3339Nano),
	}
	manifest, err := oras.PackManifest(ctx, staged, oras.PackManifestVersion1_1, contractArtifactType, oras.PackManifestOptions{
		Subject:             &subject,
		Layers:              []ocispec.Descriptor{layer},
		ManifestAnnotations: annotations,
	})
	if err != nil {
		return fmt.Errorf("packing the artifact manifest: %w", err)
	}
	// The subject is already in the registry, so CopyGraph skips it and
	// pushes the layer, the empty config and the manifest.
	if err := oras.CopyGraph(ctx, staged, repo, manifest, oras.DefaultCopyGraphOptions); err != nil {
		// Without the referrers API, oras-go replaces the index under the
		// referrers tag and then deletes the old one. Registries that refuse
		// manifest deletes (distribution's default) fail only that last
		// step: the artifact and the new index are in place, as with oras.
		var re *remote.ReferrersError
		if !errors.As(err, &re) || !re.IsReferrersIndexDelete() {
			return fmt.Errorf("pushing the artifact: %w", err)
		}
		fmt.Fprintf(stderr, "warning: the registry kept the previous referrers index, as it does not allow deletes: %v\n", err)
	}
	fmt.Fprintf(stderr, "%s: pushed the contract for %s\n", c.Name, of.image)
	printPushed(stdout, stderr, ref, manifest.Digest)
	return nil
}

func printPushed(stdout, stderr io.Writer, image registry.Reference, artifact digest.Digest) {
	ref := image.Registry + "/" + image.Repository + "@" + artifact.String()
	fmt.Fprintln(stdout, ref)
	fmt.Fprintf(stderr, "sign it with: cosign sign %s\n", ref)
}

func runPull(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, pullUsage)
		fs.PrintDefaults()
	}
	var of ociFlags
	of.register(fs)
	out := fs.String("o", "", "write the contract to this file instead of standard output")
	positional, err := parseOne(fs, args)
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected arguments: %s", strings.Join(positional, " "))
	}
	repo, ref, err := of.repository()
	if err != nil {
		return err
	}
	ctx, cancel := interruptible()
	defer cancel()
	subject, err := repo.Resolve(ctx, ref.Reference)
	if err != nil {
		return fmt.Errorf("image %s: %w", of.image, err)
	}
	if _, err := ref.Digest(); err != nil {
		fmt.Fprintf(stderr, "resolved %s to %s\n", of.image, subject.Digest)
	}

	src, source, err := pullContract(ctx, repo, subject, stderr)
	if errors.Is(err, errNoContract) {
		// Exit status 1 is not followed by a message; print it here.
		fmt.Fprintf(stderr, "docuconf pull: %v\n", err)
		return err
	} else if err != nil {
		return err
	}
	p, err := platform.New()
	if err != nil {
		return err
	}
	c, err := p.ParseContract("contract.cue", src)
	if err != nil {
		return fmt.Errorf("the contract pulled for %s: %w", of.image, err)
	}
	fmt.Fprintf(stderr, "%s: pulled the contract for %s@%s from %s\n", c.Name, ref.Registry+"/"+ref.Repository, subject.Digest, source)
	if *out != "" {
		return os.WriteFile(*out, src, 0o644)
	}
	_, err = stdout.Write(src)
	return err
}

// pullContract finds the contract for subject: the newest referrer of the
// contract artifact type, or else the image label. It returns the contract
// and where it came from, for messages.
func pullContract(ctx context.Context, repo *remote.Repository, subject ocispec.Descriptor, stderr io.Writer) ([]byte, string, error) {
	referrers, err := contractReferrers(ctx, repo, subject)
	if err != nil {
		return nil, "", err
	}
	if len(referrers) > 0 {
		// Newest first, by the created annotation.
		created := func(d ocispec.Descriptor) time.Time {
			t, _ := time.Parse(time.RFC3339Nano, d.Annotations[ocispec.AnnotationCreated])
			return t
		}
		slices.SortStableFunc(referrers, func(a, b ocispec.Descriptor) int {
			return created(b).Compare(created(a))
		})
		d := referrers[0]
		if len(referrers) > 1 {
			fmt.Fprintf(stderr, "%d contracts refer to this image; using the newest, %s\n", len(referrers), d.Digest)
		}
		m, err := fetchManifest(ctx, repo, d)
		if err != nil {
			return nil, "", err
		}
		for _, l := range m.Layers {
			if l.MediaType == contractArtifactType {
				data, err := content.FetchAll(ctx, repo.Blobs(), l)
				if err != nil {
					return nil, "", fmt.Errorf("fetching the contract: %w", err)
				}
				return data, "artifact " + d.Digest.String(), nil
			}
		}
		return nil, "", fmt.Errorf("artifact %s holds no %s layer", d.Digest, contractArtifactType)
	}

	// Fallback: the label on an image (not an index, whose platforms each
	// have their own config).
	if subject.MediaType == ocispec.MediaTypeImageManifest || subject.MediaType == "application/vnd.docker.distribution.manifest.v2+json" {
		m, err := fetchManifest(ctx, repo, subject)
		if err != nil {
			return nil, "", err
		}
		raw, err := content.FetchAll(ctx, repo.Blobs(), m.Config)
		if err != nil {
			return nil, "", fmt.Errorf("fetching the image config: %w", err)
		}
		var cfg ocispec.Image
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, "", fmt.Errorf("image config: %w", err)
		}
		if label, ok := cfg.Config.Labels[contractLabel]; ok {
			data, err := base64.StdEncoding.DecodeString(label)
			if err != nil {
				return nil, "", fmt.Errorf("image label %s is not base64: %w", contractLabel, err)
			}
			return data, "image label " + contractLabel, nil
		}
	}
	return nil, "", fmt.Errorf("%w for %s: no %s artifact refers to it, and it has no %s label", errNoContract, subject.Digest, contractArtifactType, contractLabel)
}

// contractReferrers lists the contract artifacts whose subject is the
// image. oras-go uses the referrers API, or the referrers tag schema where
// the registry has no referrers API.
func contractReferrers(ctx context.Context, repo *remote.Repository, subject ocispec.Descriptor) ([]ocispec.Descriptor, error) {
	var out []ocispec.Descriptor
	err := repo.Referrers(ctx, subject, contractArtifactType, func(r []ocispec.Descriptor) error {
		out = append(out, r...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing referrers of %s: %w", subject.Digest, err)
	}
	return out, nil
}

func fetchManifest(ctx context.Context, repo *remote.Repository, d ocispec.Descriptor) (*ocispec.Manifest, error) {
	data, err := content.FetchAll(ctx, repo, d)
	if err != nil {
		return nil, fmt.Errorf("fetching manifest %s: %w", d.Digest, err)
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", d.Digest, err)
	}
	return &m, nil
}
