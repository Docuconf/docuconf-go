package docuconf

import (
	"go/ast"
	"go/build"
	"go/doc/comment"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// docResolver reads field doc comments from Go source, so descriptions
// live where Go programmers write them. It is used at export time only;
// a deployed binary has no source and needs none.
type docResolver struct {
	dir   string                       // extra directory to search first
	files map[string][]*ast.File       // parsed files by directory
	types map[reflect.Type]*structDocs // resolved named struct types
}

// structDocs holds the doc comments of one struct type's fields, split
// into a description and details (see splitDoc).
type structDocs struct {
	docs    map[string]string
	details map[string]string
	inline  map[string]*structDocs // fields whose type is an inline struct
}

func newDocResolver(dir string) *docResolver {
	return &docResolver{dir: dir, files: map[string][]*ast.File{}, types: map[reflect.Type]*structDocs{}}
}

// forType returns the field docs of a named struct type, or nil when its
// source cannot be found.
func (r *docResolver) forType(t reflect.Type) *structDocs {
	if r == nil || t.Name() == "" {
		return nil
	}
	if sd, ok := r.types[t]; ok {
		return sd
	}
	name := t.Name()
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i] // generic instantiation
	}
	var sd *structDocs
	for _, dir := range r.candidateDirs(t.PkgPath()) {
		if sd = r.find(dir, name); sd != nil {
			break
		}
	}
	r.types[t] = sd
	return sd
}

// fieldDoc returns the description and details from the doc comment of
// the field at index path idx below root, following inline struct types.
func (r *docResolver) fieldDoc(root reflect.Type, idx []int) (desc, details string) {
	t := root
	sd := r.forType(t)
	for n, i := range idx {
		f := t.Field(i)
		if n == len(idx)-1 {
			if sd == nil {
				return "", ""
			}
			return sd.docs[f.Name], sd.details[f.Name]
		}
		next := f.Type
		if next.Kind() == reflect.Pointer {
			next = next.Elem()
		}
		if next.Name() != "" {
			sd = r.forType(next)
		} else if sd != nil {
			sd = sd.inline[f.Name]
		}
		t = next
	}
	return "", ""
}

func (r *docResolver) candidateDirs(pkgPath string) []string {
	var dirs []string
	if r.dir != "" {
		dirs = append(dirs, r.dir)
	}
	wd, _ := os.Getwd()
	if pkgPath != "" && pkgPath != "main" {
		// An external test package's path ends in _test; its files live
		// beside the package under test.
		p, err := build.Import(strings.TrimSuffix(pkgPath, "_test"), wd, build.FindOnly)
		if err == nil && p.Dir != "" {
			dirs = append(dirs, p.Dir)
		}
	}
	if wd != "" {
		dirs = append(dirs, wd)
	}
	return dirs
}

func (r *docResolver) find(dir, name string) *structDocs {
	files, ok := r.files[dir]
	if !ok {
		entries, _ := os.ReadDir(dir)
		fset := token.NewFileSet()
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ParseComments|parser.SkipObjectResolution)
			if err == nil {
				files = append(files, f)
			}
		}
		r.files[dir] = files
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				if ts.Name.Name != name {
					continue
				}
				if st, ok := ts.Type.(*ast.StructType); ok {
					return docsFromAST(st)
				}
			}
		}
	}
	return nil
}

func docsFromAST(st *ast.StructType) *structDocs {
	sd := &structDocs{docs: map[string]string{}, details: map[string]string{}, inline: map[string]*structDocs{}}
	for _, field := range st.Fields.List {
		doc, details := splitDoc(field.Doc.Text())
		if doc == "" {
			doc, details = cleanDoc(field.Comment.Text()), ""
		}
		names := field.Names
		if len(names) == 0 { // embedded field
			if id := embeddedName(field.Type); id != "" {
				names = []*ast.Ident{ast.NewIdent(id)}
			}
		}
		typ := field.Type
		if star, ok := typ.(*ast.StarExpr); ok {
			typ = star.X
		}
		for _, n := range names {
			sd.docs[n.Name] = doc
			if details != "" {
				sd.details[n.Name] = details
			}
			if inner, ok := typ.(*ast.StructType); ok {
				sd.inline[n.Name] = docsFromAST(inner)
			}
		}
	}
	return sd
}

func embeddedName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return embeddedName(x.X)
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.IndexExpr:
		return embeddedName(x.X)
	case *ast.IndexListExpr:
		return embeddedName(x.X)
	}
	return ""
}

// splitDoc splits a field's doc comment into the contract's description
// and details (SPEC §4.2):
//
//   - The first paragraph is the description, on one line, without a
//     final period: "HTTP listen port." becomes "HTTP listen port".
//   - Every later paragraph, heading, list or code block is details,
//     converted from Go doc comment syntax to Markdown: "# Heading"
//     becomes a Markdown heading, indented text a code block, and
//     [pkg.Name] doc links links to pkg.go.dev.
//
// A comment that does not start with a paragraph (it starts with a list
// or a code block) is all description, as before details existed.
func splitDoc(text string) (desc, details string) {
	if strings.TrimSpace(text) == "" {
		return "", ""
	}
	var p comment.Parser
	doc := p.Parse(text)
	if len(doc.Content) == 0 {
		return "", ""
	}
	if _, ok := doc.Content[0].(*comment.Paragraph); !ok {
		return cleanDoc(text), ""
	}
	pr := &comment.Printer{
		HeadingLevel:   1, // renderers nest details headings under the input's own
		HeadingID:      func(*comment.Heading) string { return "" },
		DocLinkBaseURL: "https://pkg.go.dev",
	}
	desc = cleanDoc(string(pr.Text(&comment.Doc{Content: doc.Content[:1], Links: doc.Links})))
	if len(doc.Content) > 1 {
		details = strings.TrimSpace(string(pr.Markdown(&comment.Doc{Content: doc.Content[1:], Links: doc.Links})))
	}
	return desc, details
}

// cleanDoc turns a doc comment into a one-line description: whitespace is
// collapsed and a final period dropped, so "HTTP listen port." becomes
// "HTTP listen port", as descriptions read in the contract.
func cleanDoc(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSuffix(s, ".")
}
