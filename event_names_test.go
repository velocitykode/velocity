package velocity

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// eventSourceTree is the framework's non-test source parsed for the event
// guards.
type eventSourceTree struct {
	files []eventSourceFile
}

// eventSourceFile is one parsed non-test framework file.
type eventSourceFile struct {
	path    string // slash-separated, relative to the module root
	file    *ast.File
	imports map[string]string // local name -> import path
}

func parseEventSourceTree(t *testing.T) *eventSourceTree {
	t.Helper()
	tree := &eventSourceTree{}
	walkNonTestGo(t, ".", func(p string, src []byte) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		imports := map[string]string{}
		for _, imp := range f.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			local := path.Base(ip)
			if imp.Name != nil {
				local = imp.Name.Name
			}
			imports[local] = ip
		}
		tree.files = append(tree.files, eventSourceFile{path: p, file: f, imports: imports})
	})
	return tree
}

// TestFrameworkPackages_DeclareNoOwnEventInterface requires the named-event
// facet to be declared once, in contract: no other package declares an
// interface whose only method is `Name() string`.
func TestFrameworkPackages_DeclareNoOwnEventInterface(t *testing.T) {
	var offenders []string
	for _, f := range parseEventSourceTree(t).files {
		if strings.HasPrefix(f.path, "contract/") {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			it, ok := spec.Type.(*ast.InterfaceType)
			if !ok || len(it.Methods.List) != 1 {
				return true
			}
			m := it.Methods.List[0]
			ft, ok := m.Type.(*ast.FuncType)
			if !ok || len(m.Names) != 1 || m.Names[0].Name != "Name" || ft.Params.NumFields() != 0 || ft.Results.NumFields() != 1 {
				return true
			}
			if id, ok := ft.Results.List[0].Type.(*ast.Ident); ok && id.Name == "string" {
				offenders = append(offenders, f.path+": "+spec.Name.Name)
			}
			return true
		})
	}
	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("%s declares its own Name() interface; use contract.Event", o)
	}
}
