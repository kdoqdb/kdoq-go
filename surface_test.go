package kdoq_test

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var updateAPI = flag.Bool("update-api", false, "rewrite testdata/api.txt from the current exported surface")

// publicPackages maps the directory of each public package to its import path
// relative to the module. Everything else is internal or test-only.
var publicPackages = map[string]string{
	".":     "kdoq",
	"value": "kdoq/value",
}

// TestPublicSurface fails when an exported identifier of a public package is
// added, removed or changes its signature without updating testdata/api.txt.
func TestPublicSurface(t *testing.T) {
	var lines []string
	for dir, path := range publicPackages {
		pkgLines, err := exportedSurface(dir, path)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, pkgLines...)
	}
	sort.Strings(lines)
	got := strings.Join(lines, "\n") + "\n"

	if *updateAPI {
		if err := os.WriteFile("testdata/api.txt", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile("testdata/api.txt")
	if err != nil {
		t.Fatal(err)
	}
	if diff := diffLines(string(want), got); diff != "" {
		t.Errorf("the exported surface differs from testdata/api.txt (run `go test -run TestPublicSurface -update-api` to accept the change):\n%s", diff)
	}
}

// TestNoUndeclaredPublicPackage fails when a package outside internal/ is added
// without being listed in publicPackages and therefore in the surface listing.
func TestNoUndeclaredPublicPackage(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "internal", "testdata", "e2e":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if _, ok := publicPackages[filepath.Dir(path)]; !ok {
			t.Errorf("%s belongs to a package that is not in publicPackages; make it internal or list it and testdata/api.txt", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSurfaceGuardDetectsChanges(t *testing.T) {
	dir := t.TempDir()
	write := func(src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	surface := func() string {
		t.Helper()
		lines, err := exportedSurface(dir, "p")
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(lines)
		return strings.Join(lines, "\n") + "\n"
	}
	write("package p\n\ntype T struct{ A int; b int }\n\nfunc (T) M() {}\n\nfunc F(x int) string { return \"\" }\n\nfunc hidden() {}\n")
	base := surface()
	for _, tc := range []struct{ name, src string }{
		{"added function", "package p\n\ntype T struct{ A int; b int }\n\nfunc (T) M() {}\n\nfunc F(x int) string { return \"\" }\n\nfunc G() {}\n"},
		{"removed method", "package p\n\ntype T struct{ A int; b int }\n\nfunc F(x int) string { return \"\" }\n"},
		{"changed signature", "package p\n\ntype T struct{ A int; b int }\n\nfunc (T) M() {}\n\nfunc F(x int64) string { return \"\" }\n"},
		{"added exported field", "package p\n\ntype T struct{ A int; B int; b int }\n\nfunc (T) M() {}\n\nfunc F(x int) string { return \"\" }\n"},
		{"added variable", "package p\n\ntype T struct{ A int; b int }\n\nfunc (T) M() {}\n\nfunc F(x int) string { return \"\" }\n\nvar V = 1\n"},
	} {
		write(tc.src)
		if diffLines(base, surface()) == "" {
			t.Errorf("%s was not detected", tc.name)
		}
	}
	write("package p\n\ntype T struct{ A int; c string }\n\nfunc (T) M() { _ = 1 }\n\nfunc F(x int) string { return \"x\" }\n\nfunc other() {}\n")
	if diff := diffLines(base, surface()); diff != "" {
		t.Errorf("an unexported or body-only change was reported:\n%s", diff)
	}
}

func diffLines(want, got string) string {
	in := func(text string) map[string]bool {
		set := map[string]bool{}
		for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
			set[l] = true
		}
		return set
	}
	wantSet, gotSet := in(want), in(got)
	var out []string
	for l := range wantSet {
		if !gotSet[l] {
			out = append(out, "- "+l)
		}
	}
	for l := range gotSet {
		if !wantSet[l] {
			out = append(out, "+ "+l)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// exportedSurface lists one line per exported identifier of the package in dir:
// functions, methods of exported types, types with their exported fields,
// constants and variables, each as `import/path: declaration`.
func exportedSurface(dir, path string) ([]string, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, err
	}
	expr := func(n ast.Node) string {
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, n); err != nil {
			panic(err)
		}
		return strings.Join(strings.Fields(buf.String()), " ")
	}
	var lines []string
	add := func(format string, args ...any) {
		lines = append(lines, path+": "+fmt.Sprintf(format, args...))
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if !d.Name.IsExported() {
						continue
					}
					header := *d
					header.Doc, header.Body = nil, nil
					if d.Recv != nil && !exportedReceiver(d.Recv) {
						continue
					}
					add("%s", expr(&header))
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if s.Name.IsExported() {
								add("type %s%s %s", s.Name.Name, typeParams(s, expr), typeBody(s, expr))
							}
						case *ast.ValueSpec:
							for _, name := range s.Names {
								if !name.IsExported() {
									continue
								}
								typ := ""
								if s.Type != nil {
									typ = " " + expr(s.Type)
								}
								add("%s %s%s", d.Tok, name.Name, typ)
							}
						}
					}
				}
			}
		}
	}
	return lines, nil
}

func exportedReceiver(recv *ast.FieldList) bool {
	t := recv.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.Ident:
			return x.IsExported()
		default:
			return false
		}
	}
}

func typeParams(s *ast.TypeSpec, expr func(ast.Node) string) string {
	if s.TypeParams == nil {
		return ""
	}
	var params []string
	for _, f := range s.TypeParams.List {
		var names []string
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
		params = append(params, strings.Join(names, ", ")+" "+expr(f.Type))
	}
	return "[" + strings.Join(params, ", ") + "]"
}

func typeBody(s *ast.TypeSpec, expr func(ast.Node) string) string {
	prefix := ""
	if s.Assign.IsValid() {
		prefix = "= "
	}
	switch t := s.Type.(type) {
	case *ast.StructType:
		var fields []string
		hidden := false
		for _, f := range t.Fields.List {
			if len(f.Names) == 0 {
				fields = append(fields, expr(f.Type))
				continue
			}
			var names []string
			for _, n := range f.Names {
				if n.IsExported() {
					names = append(names, n.Name)
				} else {
					hidden = true
				}
			}
			if len(names) > 0 {
				fields = append(fields, strings.Join(names, ", ")+" "+expr(f.Type))
			}
		}
		if hidden {
			fields = append(fields, "...")
		}
		if len(fields) == 0 {
			return prefix + "struct{}"
		}
		return prefix + "struct{ " + strings.Join(fields, "; ") + " }"
	case *ast.InterfaceType:
		var methods []string
		for _, m := range t.Methods.List {
			if len(m.Names) == 0 {
				methods = append(methods, expr(m.Type))
			} else if m.Names[0].IsExported() {
				methods = append(methods, m.Names[0].Name+strings.TrimPrefix(expr(m.Type), "func"))
			} else {
				methods = append(methods, "(sealed)")
			}
		}
		if len(methods) == 0 {
			return prefix + "interface{}"
		}
		return prefix + "interface{ " + strings.Join(methods, "; ") + " }"
	}
	return prefix + expr(s.Type)
}
