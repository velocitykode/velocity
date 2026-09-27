package velocity

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"testing"
)

// errorContextBuilder is the one function in non-test code that constructs
// a contract.ErrorContext, and the file that declares it.
const (
	errorContextBuilderFile = "trace/error_context.go"
	errorContextBuilderFunc = "NewErrorContext"
)

// errorContextTypePkgs are the packages under which ErrorContext names the
// contract type: contract declares it and problem aliases it.
var errorContextTypePkgs = map[string]bool{
	"github.com/velocitykode/velocity/contract": true,
	"github.com/velocitykode/velocity/problem":  true,
}

// Every ErrorContext the framework builds comes from trace.NewErrorContext,
// so the ids and time on each report are filled one way: no composite
// literal, new() or value declaration of the type exists anywhere else in
// non-test code, and no other function is named NewErrorContext.
func TestErrorContext_OneConstructor(t *testing.T) {
	var offenders []string
	walkNonTestGo(t, ".", func(p string, src []byte) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		dir := path.Dir(p)
		local := dir == "contract" || dir == "problem"
		imports := map[string]bool{}
		for _, imp := range f.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			if !errorContextTypePkgs[ip] {
				continue
			}
			name := path.Base(ip)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			imports[name] = true
		}
		isErrorContext := func(expr ast.Expr) bool {
			switch e := expr.(type) {
			case *ast.Ident:
				return local && e.Name == "ErrorContext"
			case *ast.SelectorExpr:
				x, ok := e.X.(*ast.Ident)
				return ok && imports[x.Name] && e.Sel.Name == "ErrorContext"
			}
			return false
		}
		for _, decl := range f.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			allowed := isFunc && p == errorContextBuilderFile && fn.Recv == nil && fn.Name.Name == errorContextBuilderFunc
			if isFunc && fn.Name.Name == errorContextBuilderFunc && !allowed {
				offenders = append(offenders, fset.Position(fn.Pos()).String()+": another "+errorContextBuilderFunc)
			}
			if allowed {
				continue
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CompositeLit:
					if x.Type != nil && isErrorContext(x.Type) {
						offenders = append(offenders, fset.Position(x.Pos()).String()+": ErrorContext literal")
					}
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 && isErrorContext(x.Args[0]) {
						offenders = append(offenders, fset.Position(x.Pos()).String()+": new(ErrorContext)")
					}
				case *ast.ValueSpec:
					if x.Type != nil && isErrorContext(x.Type) {
						offenders = append(offenders, fset.Position(x.Pos()).String()+": ErrorContext value declaration")
					}
				}
				return true
			})
		}
	})
	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("%s: build it with trace.NewErrorContext", o)
	}
}
