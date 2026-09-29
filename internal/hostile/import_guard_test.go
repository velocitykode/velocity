package hostile

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePath  = "github.com/velocitykode/velocity"
	hostilePath = modulePath + "/internal/hostile"
)

// moduleRoot returns the directory holding the module's go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// TestOnlyTestFilesImportHostile keeps the hostile fakes out of framework
// code: only _test.go files may import this package.
func TestOnlyTestFilesImportHostile(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == hostilePath {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s imports internal/hostile; only _test.go files may", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestHostileImportsStdlibAndContractOnly keeps the package a leaf every
// framework package's tests can import without a cycle.
func TestHostileImportsStdlibAndContractOnly(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if p == modulePath+"/contract" {
					continue
				}
				if first, _, _ := strings.Cut(p, "/"); strings.Contains(first, ".") {
					t.Errorf("%s imports %s; the package may import the standard library and contract only", name, p)
				}
			}
		}
	}
}
