package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"

	catalogs "github.com/docuconf/docuconf-go/cmd/docuconf/internal/catalog"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/oci"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// resolveCatalog finds an image's contract in DOCUCONF_CATALOG's
// catalogs, then the community one (spec/SPEC.md section 8.1).
func resolveCatalog(image string) (catalogs.Match, error) {
	cats, err := catalogs.FromEnv()
	if err != nil {
		return catalogs.Match{}, err
	}
	return catalogs.Resolve(cats, image)
}

const catalogUsage = `usage:
  docuconf catalog resolve <image>   which contract the catalogs have for the image
  docuconf catalog push [-for <upstream image>] <image>
                                     attach that contract to the image, as docuconf push
                                     does; -for picks it by the image a mirror copies
  docuconf catalog list              every catalog's entries
  docuconf catalog check <dir>       check a catalog: its index and every contract

Catalogs hold contracts written by hand for third-party images. The ones
DOCUCONF_CATALOG lists (directories, separated as in PATH) are searched
first, then the community catalog docuconf embeds. Anywhere a contract
file is expected, catalog:<image> reads the catalog's contract.
`

// runCatalog finds, lists and checks catalog contracts, and attaches
// them to images.
func runCatalog(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || slices.Contains([]string{"-h", "-help", "--help", "help"}, args[0]) {
		fmt.Fprint(stderr, catalogUsage)
		return flag.ErrHelp
	}
	sub, args := args[0], args[1:]
	needOne := func() error {
		if len(args) != 1 {
			fmt.Fprint(stderr, catalogUsage)
			return flag.ErrHelp
		}
		return nil
	}
	switch sub {
	case "resolve":
		if err := needOne(); err != nil {
			return err
		}
		m, err := resolveCatalog(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s\n", m)
		return nil
	case "push":
		fs := flag.NewFlagSet("catalog push", flag.ContinueOnError)
		fs.SetOutput(stderr)
		upstream := fs.String("for", "", "the image whose contract to attach, when <image> is a mirror's copy of it (docker.io/library/postgres:16)")
		if err := parseInterspersed(fs, args); err != nil {
			return err
		}
		args = fs.Args()
		if err := needOne(); err != nil {
			return err
		}
		match := args[0]
		if *upstream != "" {
			match = *upstream
		}
		m, err := resolveCatalog(match)
		if err != nil {
			return err
		}
		p, err := platform.New()
		if err != nil {
			return err
		}
		c, err := p.ParseContract(m.Entry.Contract, m.Contract)
		if err != nil {
			return fmt.Errorf("%s: %v", m, err)
		}
		ref, err := oci.ParseRef(args[0])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), registryTimeout)
		defer cancel()
		img, desc, err := registry.Push(ctx, ref, m.Contract)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "docuconf catalog: attached the %s contract (%s) to %s as %s\n", c.Name, m, img, desc.Digest)
		fmt.Fprintf(stdout, "%s%s\n", oci.Scheme, img)
		return nil
	case "list":
		if len(args) != 0 {
			fmt.Fprint(stderr, catalogUsage)
			return flag.ErrHelp
		}
		cats, err := catalogs.FromEnv()
		if err != nil {
			return err
		}
		for _, c := range cats {
			for _, e := range c.Entries {
				fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", c.Name, e.Repository, e.Versions, e.Contract)
			}
		}
		return nil
	case "check":
		if err := needOne(); err != nil {
			return err
		}
		return checkCatalog(args[0], stdout)
	}
	fmt.Fprint(stderr, catalogUsage)
	return flag.ErrHelp
}

// checkCatalog checks a catalog's index and that every contract it names
// satisfies the meta-schema.
func checkCatalog(dir string, stdout io.Writer) error {
	c, err := catalogs.Load(dir, osDirFS(dir))
	if err != nil {
		return err
	}
	p, err := platform.New()
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range c.Entries {
		b, err := readFS(c, e.Contract)
		if err == nil {
			_, err = p.ParseContract(e.Contract, b)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s (%s): %v", e.Contract, e.Repository, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	fmt.Fprintf(stdout, "%s: %d contracts ok\n", dir, len(c.Entries))
	return nil
}

func osDirFS(dir string) fs.FS { return os.DirFS(dir) }

func readFS(c *catalogs.Catalog, name string) ([]byte, error) { return fs.ReadFile(c.FS, name) }
