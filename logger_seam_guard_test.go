package velocity

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// contractImportPath is the one package whose Logger every framework
// logger seam accepts.
const contractImportPath = "github.com/velocitykode/velocity/contract"

// loggerSeamOwner reports whether path belongs to a package allowed to
// declare its own logger types: contract (the shared contract) and log
// (the implementations).
func loggerSeamOwner(path string) bool {
	return strings.HasPrefix(path, "contract/") || strings.HasPrefix(path, "log/")
}

// loggerMethodNames are the method names that make an interface a logger.
var loggerMethodNames = map[string]bool{
	"Debug": true, "Info": true, "Warn": true, "Error": true, "Fatal": true,
	"Debugf": true, "Infof": true, "Warnf": true, "Errorf": true, "Fatalf": true,
	"Print": true, "Printf": true, "Println": true,
}

// loggerParamName matches a parameter or field name that says it holds a
// logger, whatever its type.
var loggerParamName = regexp.MustCompile(`(?i)^(log|logger|logfn|logfunc|logf|[a-z]*logger)$`)

// loggerSeamFile is one parsed non-test framework file.
type loggerSeamFile struct {
	path    string
	fset    *token.FileSet
	file    *ast.File
	imports map[string]string // local name -> import path
}

func parseLoggerSeamFiles(t *testing.T) []loggerSeamFile {
	t.Helper()
	var files []loggerSeamFile
	walkNonTestGo(t, ".", func(p string, src []byte) {
		if loggerSeamOwner(p) {
			return
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		imports := map[string]string{}
		for _, imp := range f.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			name := path.Base(ip)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			imports[name] = ip
		}
		files = append(files, loggerSeamFile{path: p, fset: fset, file: f, imports: imports})
	})
	return files
}

func (f loggerSeamFile) pos(n ast.Node) string {
	p := f.fset.Position(n.Pos())
	return f.path + ":" + strconv.Itoa(p.Line)
}

// isContractLogger reports whether expr is exactly contract.Logger.
func (f loggerSeamFile) isContractLogger(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Logger" {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && f.imports[x.Name] == contractImportPath
}

// isLoggerType reports whether expr names a logger: a type whose name ends
// in Logger (local, the log package's, slog's, stdlib log's), an interface
// with logger methods, or a function that takes a message string and
// returns nothing (a log sink).
func isLoggerType(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return isLoggerType(e.X)
	case *ast.Ident:
		return strings.HasSuffix(strings.ToLower(e.Name), "logger")
	case *ast.SelectorExpr:
		return strings.HasSuffix(e.Sel.Name, "Logger")
	case *ast.InterfaceType:
		return isLoggerInterface(e)
	}
	return false
}

// isLogSink reports whether ft takes a leading string and returns nothing,
// the shape of a log function (func(string), func(msg string, kvs ...any),
// func(query string, argCount int)).
func isLogSink(ft *ast.FuncType) bool {
	if ft.Results != nil && len(ft.Results.List) > 0 {
		return false
	}
	if ft.Params == nil || len(ft.Params.List) == 0 {
		return false
	}
	id, ok := ft.Params.List[0].Type.(*ast.Ident)
	return ok && id.Name == "string"
}

// isLoggerInterface reports whether it declares a logger method, embeds a
// logger, or is a logger facet (a SetLogger method). A testing.TB subset
// (it declares Helper) reports test failures, not log lines.
func isLoggerInterface(it *ast.InterfaceType) bool {
	if it.Methods == nil {
		return false
	}
	for _, m := range it.Methods.List {
		for _, n := range m.Names {
			if n.Name == "Helper" {
				return false
			}
		}
	}
	for _, m := range it.Methods.List {
		if len(m.Names) == 0 {
			if isLoggerType(m.Type) {
				return true
			}
			continue
		}
		ft, ok := m.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		for _, n := range m.Names {
			if n.Name == "SetLogger" {
				return true
			}
			if loggerMethodNames[n.Name] && ft.Params != nil && len(ft.Params.List) > 0 {
				if id, ok := ft.Params.List[0].Type.(*ast.Ident); ok && id.Name == "string" {
					return true
				}
			}
		}
	}
	return false
}

// loggerSeamOffence reports why a parameter or field named name with type
// expr, declared by a function or field called owner, is a logger seam
// that does not take contract.Logger; "" when it is not one.
func (f loggerSeamFile) loggerSeamOffence(owner, name string, expr ast.Expr) string {
	if f.isContractLogger(expr) {
		return ""
	}
	if isLoggerType(expr) {
		return "logger type " + exprString(expr)
	}
	if ft, ok := expr.(*ast.FuncType); ok && isLogSink(ft) {
		if loggerParamName.MatchString(name) || strings.Contains(strings.ToLower(owner), "log") {
			return "log function " + exprString(expr)
		}
	}
	if name != "" && loggerParamName.MatchString(name) && isUntyped(expr) {
		return "logger-named " + exprString(expr)
	}
	return ""
}

// isUntyped reports whether expr is any or an empty interface, the type a
// logger-named value carries when it accepts anything.
func isUntyped(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "any"
	case *ast.InterfaceType:
		return e.Methods == nil || len(e.Methods.List) == 0
	}
	return false
}

func exprString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprString(e.X)
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.InterfaceType:
		return "interface{...}"
	case *ast.FuncType:
		return "func(...)"
	}
	return "?"
}

// receiverExported reports whether the method receiver's base type is
// exported (T, *T, T[K], *T[K]).
func receiverExported(fd *ast.FuncDecl) bool {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return true
	}
	expr := fd.Recv.List[0].Type
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
			continue
		case *ast.IndexExpr:
			expr = e.X
			continue
		case *ast.IndexListExpr:
			expr = e.X
			continue
		case *ast.Ident:
			return e.IsExported()
		}
		return false
	}
}

// isLoggerAwareSetter reports whether a SetLogger method has the
// contract.LoggerAware shape: one contract.Logger parameter, no results.
func (f loggerSeamFile) isLoggerAwareSetter(d *ast.FuncDecl) bool {
	if d.Type.Results != nil && len(d.Type.Results.List) > 0 {
		return false
	}
	params := d.Type.Params.List
	return len(params) == 1 && len(params[0].Names) <= 1 && f.isContractLogger(params[0].Type)
}

// Every exported framework func or method outside contract and log that
// takes a logger takes contract.Logger, every exported struct field
// holding one is contract.Logger, and every SetLogger method is
// contract.LoggerAware's, so the app logger passes to each seam unchanged.
func TestFrameworkLoggerSeams_TakeContractLogger(t *testing.T) {
	var offenders []string
	for _, f := range parseLoggerSeamFiles(t) {
		for _, decl := range f.file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() || !receiverExported(d) || d.Type.Params == nil {
					continue
				}
				if d.Recv != nil && d.Name.Name == "SetLogger" && !f.isLoggerAwareSetter(d) {
					offenders = append(offenders, f.pos(d)+": SetLogger: not contract.LoggerAware's SetLogger(contract.Logger)")
				}
				for _, p := range d.Type.Params.List {
					names := []string{""}
					if len(p.Names) > 0 {
						names = names[:0]
						for _, n := range p.Names {
							names = append(names, n.Name)
						}
					}
					for _, n := range names {
						if why := f.loggerSeamOffence(d.Name.Name, n, p.Type); why != "" {
							offenders = append(offenders, f.pos(p)+": "+d.Name.Name+"("+n+"): "+why)
						}
					}
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !ts.Name.IsExported() {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						continue
					}
					for _, fld := range st.Fields.List {
						for _, n := range fld.Names {
							if !n.IsExported() {
								continue
							}
							if why := f.loggerSeamOffence(n.Name, n.Name, fld.Type); why != "" {
								offenders = append(offenders, f.pos(fld)+": "+ts.Name.Name+"."+n.Name+": "+why)
							}
						}
					}
				}
			}
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("logger seams that do not take contract.Logger:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// No package outside contract and log declares a logger interface: no
// interface with logger methods, no logger facet (a SetLogger method, which
// is contract.LoggerAware), and no alias or defined type over a logger.
func TestFrameworkSource_DeclaresNoLoggerInterface(t *testing.T) {
	var offenders []string
	for _, f := range parseLoggerSeamFiles(t) {
		ast.Inspect(f.file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.TypeSpec:
				if _, iface := x.Type.(*ast.InterfaceType); !iface && isLoggerType(x.Type) {
					offenders = append(offenders, f.pos(x)+": type "+x.Name.Name+" over "+exprString(x.Type))
				}
			case *ast.InterfaceType:
				if isLoggerInterface(x) {
					offenders = append(offenders, f.pos(x)+": logger interface")
				}
			}
			return true
		})
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("logger interfaces declared outside contract and log (use contract.Logger / contract.LoggerAware):\n  %s", strings.Join(offenders, "\n  "))
	}
}
