package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	sdk "github.com/docuconf/docuconf-go"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/oci"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// registry reaches the registries -contract oci://, push and pull name;
// tests point it at an in-memory one.
var registry = &oci.Client{}

// registryTimeout bounds every registry operation of one command.
const registryTimeout = 2 * time.Minute

// contractSource is where a -contract came from.
type contractSource struct {
	// Image is the image an oci:// contract belongs to, pinned by digest;
	// empty for a file.
	Image string
}

// loadContract reads -contract: a file, or oci://IMAGE, the contract
// attached to an image (spec/SPEC.md section 8).
func loadContract(p *platform.Platform, ref string) (*platform.Contract, contractSource, error) {
	if !strings.HasPrefix(ref, oci.Scheme) {
		c, err := p.LoadContract(ref)
		return c, contractSource{}, err
	}
	pulled, err := pullContract(ref)
	if err != nil {
		return nil, contractSource{}, err
	}
	c, err := p.ParseContract("contract.cue", pulled.Contract)
	if err != nil {
		return nil, contractSource{}, fmt.Errorf("the contract attached to %s: %v", pulled.Image, err)
	}
	return c, contractSource{Image: pulled.Image.String()}, nil
}

func pullContract(ref string) (oci.Pulled, error) {
	r, err := oci.ParseRef(ref)
	if err != nil {
		return oci.Pulled{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), registryTimeout)
	defer cancel()
	return registry.Pull(ctx, r)
}

// runPush attaches a contract to the image it describes, as an OCI
// artifact whose subject is the image (spec/SPEC.md section 8).
func runPush(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(stderr)
	contract := fs.String("contract", "contract.cue", "the contract to attach")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: docuconf push [-contract contract.cue] <image>\n\nAttaches the contract to the image, as an %s artifact the registry lists among the image's referrers.\nCredentials are docker login's. Flags:\n", oci.ArtifactType)
		fs.PrintDefaults()
	}
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return flag.ErrHelp
	}
	p, err := platform.New()
	if err != nil {
		return err
	}
	c, err := p.LoadContract(*contract)
	if err != nil {
		return err
	}
	doc, err := p.ContractJSON(c)
	if err != nil {
		return err
	}
	src, err := pushSource(p, *contract, doc)
	if err != nil {
		return err
	}
	if src == nil {
		fmt.Fprintf(stderr, "docuconf push: warning: %s does not read back as itself, so the canonical form is pushed; modules generated from the image get another contract digest than ones generated from the file\n", *contract)
		if src, err = sdk.ContractCUE(doc, ""); err != nil {
			return err
		}
	}
	ref, err := oci.ParseRef(fs.Arg(0))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), registryTimeout)
	defer cancel()
	img, desc, err := registry.Push(ctx, ref, src)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "docuconf push: attached the %s contract to %s as %s\n", c.Name, img, desc.Digest)
	fmt.Fprintf(stdout, "%s%s\n", oci.Scheme, img)
	return nil
}

// pushSource is the contract file's own source when it reads back, alone,
// as the same contract JSON: then a module generated from the image has
// the same contract digest as one generated from the file. nil when it
// does not (a JSON contract may not).
func pushSource(p *platform.Platform, file string, doc []byte) ([]byte, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	c, err := p.ParseContract("contract.cue", src)
	if err != nil {
		return nil, nil
	}
	back, err := p.ContractJSON(c)
	if err != nil || !bytes.Equal(back, doc) {
		return nil, nil
	}
	return src, nil
}

// runPull writes the contract attached to an image.
func runPull(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "the file to write (default: standard output)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: docuconf pull <image> [-o contract.cue]\n\nReads the contract attached to the image: its %s referrer, or else its %s label.\nFlags:\n", oci.ArtifactType, oci.Label)
		fs.PrintDefaults()
	}
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return flag.ErrHelp
	}
	pulled, err := pullContract(fs.Arg(0))
	if err != nil {
		return err
	}
	p, err := platform.New()
	if err != nil {
		return err
	}
	if _, err := p.ParseContract("contract.cue", pulled.Contract); err != nil {
		return fmt.Errorf("the contract attached to %s: %v", pulled.Image, err)
	}
	fmt.Fprintf(stderr, "docuconf pull: %s, from its %s\n", pulled.Image, pulled.From)
	if *out == "" {
		_, err = stdout.Write(pulled.Contract)
		return err
	}
	return os.WriteFile(*out, pulled.Contract, 0o644)
}

// parseInterspersed parses flags given before or after the arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) error {
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return fs.Parse(rest)
}
