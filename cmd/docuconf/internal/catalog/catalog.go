// Package catalog finds the contract of a third-party image in a catalog
// (spec/SPEC.md section 8.1): contracts written by hand for images no
// docuconf SDK built, matched by repository and version range.
package catalog

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Scheme names a catalog contract wherever a contract file is expected:
// catalog:postgres:16.
const Scheme = "catalog:"

// Index is a catalog's catalog.yaml.
const Index = "catalog.yaml"

//go:embed community
var community embed.FS

// Entry is one contract of a catalog.
type Entry struct {
	// Repository is the image repository, normalized:
	// docker.io/library/postgres.
	Repository string `yaml:"repository"`
	// Versions is the range of the image's tag versions the contract
	// covers: ">=13 <19", "16 || 17", or "*" (any tag, versioned or not).
	Versions string `yaml:"versions"`
	// Contract is the contract's path, relative to the catalog.
	Contract string `yaml:"contract"`
}

// Catalog is a directory of contracts and its index.
type Catalog struct {
	Name    string // a directory, or "community"
	FS      fs.FS
	Entries []Entry
}

// Load reads a catalog's index, and checks every entry's form.
func Load(name string, fsys fs.FS) (*Catalog, error) {
	b, err := fs.ReadFile(fsys, Index)
	if err != nil {
		return nil, fmt.Errorf("catalog %s: %w", name, err)
	}
	var doc struct {
		Entries []Entry `yaml:"entries"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("catalog %s: %s: %w", name, Index, err)
	}
	c := &Catalog{Name: name, FS: fsys, Entries: doc.Entries}
	var problems []string
	for i, e := range c.Entries {
		where := fmt.Sprintf("entry %d (%s)", i+1, e.Repository)
		if n := NormalizeRepository(e.Repository); n != e.Repository {
			problems = append(problems, fmt.Sprintf("%s: repository must be written in full, %s", where, n))
		}
		if _, err := parseRange(e.Versions); err != nil {
			problems = append(problems, fmt.Sprintf("%s: versions: %v", where, err))
		}
		if e.Contract == "" || path.IsAbs(e.Contract) || strings.HasPrefix(path.Clean(e.Contract), "..") {
			problems = append(problems, fmt.Sprintf("%s: contract must be a path inside the catalog", where))
		} else if _, err := fs.Stat(fsys, e.Contract); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", where, err))
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("catalog %s: %s", name, strings.Join(problems, "; "))
	}
	return c, nil
}

// Community is the catalog docuconf embeds.
func Community() *Catalog {
	sub, err := fs.Sub(community, "community")
	if err != nil {
		panic(err)
	}
	c, err := Load("community", sub)
	if err != nil {
		panic(err)
	}
	return c
}

// FromEnv is the catalogs a lookup searches, in order: the directories
// DOCUCONF_CATALOG lists (separated as in PATH), then the community one.
func FromEnv() ([]*Catalog, error) {
	var out []*Catalog
	for _, dir := range filepath.SplitList(os.Getenv("DOCUCONF_CATALOG")) {
		if dir == "" {
			continue
		}
		c, err := Load(dir, os.DirFS(dir))
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return append(out, Community()), nil
}

// Match is the contract a catalog has for an image.
type Match struct {
	Catalog  *Catalog
	Entry    Entry
	Contract []byte
}

func (m Match) String() string {
	return fmt.Sprintf("%s in catalog %s (%s, versions %s)", m.Entry.Contract, m.Catalog.Name, m.Entry.Repository, m.Entry.Versions)
}

// Resolve finds an image's contract: the first entry, in catalog order,
// whose repository is the image's and whose range holds its tag's
// version. An organization's catalog therefore wins over the community's.
func Resolve(catalogs []*Catalog, image string) (Match, error) {
	repo, tag := SplitImage(image)
	v, versioned := tagVersion(tag)
	var known bool
	for _, c := range catalogs {
		for _, e := range c.Entries {
			if e.Repository != repo {
				continue
			}
			known = true
			r, _ := parseRange(e.Versions)
			if !r.holds(v, versioned) {
				continue
			}
			b, err := fs.ReadFile(c.FS, e.Contract)
			if err != nil {
				return Match{}, err
			}
			return Match{Catalog: c, Entry: e, Contract: b}, nil
		}
	}
	switch {
	case !known:
		return Match{}, fmt.Errorf("no catalog has a contract for %s", repo)
	case !versioned:
		return Match{}, fmt.Errorf("the catalogs' contracts for %s cover version ranges, and the tag %q names no version; give the image with a version tag, such as %s:16", repo, tag, repo)
	}
	return Match{}, fmt.Errorf("no catalog has a contract for %s at version %s", repo, tag)
}

// SplitImage splits an image reference into its normalized repository
// and its tag, "latest" when it has none. A digest is dropped: a catalog
// matches versions, which only a tag names.
func SplitImage(image string) (repo, tag string) {
	image, _, _ = strings.Cut(image, "@")
	tag = "latest"
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image, tag = image[:i], image[i+1:]
	}
	return NormalizeRepository(image), tag
}

// NormalizeRepository writes a repository in full, as Docker reads it:
// postgres is docker.io/library/postgres, bitnami/redis is
// docker.io/bitnami/redis.
func NormalizeRepository(r string) string {
	first, rest, ok := strings.Cut(r, "/")
	switch {
	case !ok:
		return "docker.io/library/" + r
	case first == "index.docker.io":
		first = "docker.io"
	case !strings.ContainsAny(first, ".:") && first != "localhost":
		return "docker.io/" + r
	}
	if first == "docker.io" && !strings.Contains(rest, "/") {
		rest = "library/" + rest
	}
	return first + "/" + rest
}

// version is major.minor.patch; a tag's missing parts are 0.
type version [3]int

var tagVersionRe = regexp.MustCompile(`^v?([0-9]+)(?:\.([0-9]+))?(?:\.([0-9]+))?(?:[-_.+].*)?$`)

// tagVersion reads the version a tag names: 16, 16.4, v1.27.2,
// 16.4-alpine3.20. A tag such as latest or alpine names none.
func tagVersion(tag string) (version, bool) {
	m := tagVersionRe.FindStringSubmatch(tag)
	if m == nil {
		return version{}, false
	}
	var v version
	for i := range 3 {
		if m[i+1] != "" {
			v[i], _ = strconv.Atoi(m[i+1])
		}
	}
	return v, true
}

func (v version) cmp(o version) int {
	for i := range 3 {
		if v[i] != o[i] {
			if v[i] < o[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// versionRange is alternatives separated by ||, each a list of
// comparisons that must all hold.
type versionRange struct {
	any  bool
	alts [][]comparison
}

type comparison struct {
	op    string // >= > <= < =
	v     version
	parts int // how many parts the range wrote: = compares only those
}

var comparisonRe = regexp.MustCompile(`^(>=|<=|>|<|=)?([0-9]+(?:\.[0-9]+){0,2})$`)

func parseRange(s string) (versionRange, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return versionRange{}, fmt.Errorf("a version range is required, such as \">=13 <19\", or \"*\" for every tag")
	}
	if s == "*" {
		return versionRange{any: true}, nil
	}
	var r versionRange
	for _, alt := range strings.Split(s, "||") {
		var cs []comparison
		for _, f := range strings.Fields(alt) {
			m := comparisonRe.FindStringSubmatch(f)
			if m == nil {
				return versionRange{}, fmt.Errorf("%q is not a comparison such as >=13 or <1.27", f)
			}
			v, _ := tagVersion(m[2])
			op := m[1]
			if op == "" {
				op = "="
			}
			cs = append(cs, comparison{op, v, strings.Count(m[2], ".") + 1})
		}
		if len(cs) == 0 {
			return versionRange{}, fmt.Errorf("%q has an empty alternative", s)
		}
		r.alts = append(r.alts, cs)
	}
	return r, nil
}

func (r versionRange) holds(v version, versioned bool) bool {
	if r.any {
		return true
	}
	if !versioned {
		return false
	}
	for _, alt := range r.alts {
		ok := true
		for _, c := range alt {
			d := v.cmp(c.v)
			switch c.op {
			case ">=":
				ok = ok && d >= 0
			case ">":
				ok = ok && d > 0
			case "<=":
				ok = ok && d <= 0
			case "<":
				ok = ok && d < 0
			case "=":
				// 16 holds 16.4: only the parts the range writes count.
				for i := range c.parts {
					ok = ok && v[i] == c.v[i]
				}
			}
		}
		if ok {
			return true
		}
	}
	return false
}
