package velocity

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"slices"
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

// loggerSeamSource is one Go source file handed to the guard.
type loggerSeamSource struct {
	path string // slash-separated, relative to the module root
	src  []byte
}

// loggerSeamTree is the parsed source the guard checks: every file outside
// contract and log, and the type declarations of every file, contract and
// log included, to resolve named types.
type loggerSeamTree struct {
	files []loggerSeamFile
	types map[string]loggerSeamType // package dir + "." + type name
}

// loggerSeamType is a type declaration and the file that declares it.
type loggerSeamType struct {
	file loggerSeamFile
	spec *ast.TypeSpec
}

func parseLoggerSeamFiles(t *testing.T) *loggerSeamTree {
	t.Helper()
	var sources []loggerSeamSource
	walkNonTestGo(t, ".", func(p string, src []byte) {
		sources = append(sources, loggerSeamSource{path: p, src: src})
	})
	return buildLoggerSeamTree(t, sources)
}

func buildLoggerSeamTree(t *testing.T, sources []loggerSeamSource) *loggerSeamTree {
	t.Helper()
	tree := &loggerSeamTree{types: map[string]loggerSeamType{}}
	for _, s := range sources {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, s.path, s.src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", s.path, err)
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
		file := loggerSeamFile{path: s.path, fset: fset, file: f, imports: imports}
		for _, decl := range f.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
				for _, spec := range gd.Specs {
					ts := spec.(*ast.TypeSpec)
					tree.types[path.Dir(s.path)+"."+ts.Name.Name] = loggerSeamType{file: file, spec: ts}
				}
			}
		}
		if !loggerSeamOwner(s.path) {
			tree.files = append(tree.files, file)
		}
	}
	return tree
}

// moduleImportPath is the import path of the module the guard checks.
const moduleImportPath = "github.com/velocitykode/velocity"

// namedFuncType resolves expr, a named type (T or pkg.T) the tree declares,
// to the function type it stands for, following type-to-type declarations.
// logNamed reports whether any name on the way says it is a logger (see
// namesLogging). ft is nil when expr does not name a function type the
// tree declares.
func (tree *loggerSeamTree) namedFuncType(f loggerSeamFile, expr ast.Expr) (ft *ast.FuncType, logNamed bool) {
	for depth := 0; depth < 8; depth++ {
		var key, name string
		switch e := expr.(type) {
		case *ast.Ident:
			key, name = path.Dir(f.path)+"."+e.Name, e.Name
		case *ast.SelectorExpr:
			x, ok := e.X.(*ast.Ident)
			if !ok {
				return nil, false
			}
			ip := f.imports[x.Name]
			dir := strings.TrimPrefix(ip, moduleImportPath+"/")
			if ip == moduleImportPath {
				dir = "."
			} else if dir == ip {
				return nil, false
			}
			key, name = dir+"."+e.Sel.Name, e.Sel.Name
		default:
			return nil, false
		}
		decl, ok := tree.types[key]
		if !ok {
			return nil, false
		}
		logNamed = logNamed || namesLogging(name)
		if ft, ok := decl.spec.Type.(*ast.FuncType); ok {
			return ft, logNamed
		}
		f, expr = decl.file, decl.spec.Type
	}
	return nil, false
}

// loggingWords are the words that make an identifier name a logger.
var loggingWords = map[string]bool{
	"log": true, "logger": true, "logf": true, "logfn": true, "logfunc": true, "logging": true,
}

// namesLogging reports whether one of the words of a Go identifier (split
// at underscores and case changes: LogFunc, queryLogFn, HTTPLogger) is a
// logging word. Catalog, Dialog and Login are not.
func namesLogging(name string) bool {
	for _, part := range strings.Split(name, "_") {
		start := 0
		for i := 1; i <= len(part); i++ {
			if i < len(part) && !camelBoundary(part, i) {
				continue
			}
			if loggingWords[strings.ToLower(part[start:i])] {
				return true
			}
			start = i
		}
	}
	return false
}

// camelBoundary reports whether a new word starts at s[i]: an upper-case
// letter after a lower-case letter or digit, or the last upper-case letter
// of a run followed by a lower-case one (the L of HTTPLogger).
func camelBoundary(s string, i int) bool {
	isUpper := func(c byte) bool { return c >= 'A' && c <= 'Z' }
	isLowerOrDigit := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }
	if !isUpper(s[i]) {
		return false
	}
	return isLowerOrDigit(s[i-1]) || (isUpper(s[i-1]) && i+1 < len(s) && isLowerOrDigit(s[i+1]))
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
// expr, declared in f by a function or field called owner, is a logger seam
// that does not take contract.Logger; "" when it is not one. A named
// function type the tree declares counts as the function type it stands
// for, and as a log function when its own name says so.
func (tree *loggerSeamTree) loggerSeamOffence(f loggerSeamFile, owner, name string, expr ast.Expr) string {
	if f.isContractLogger(expr) {
		return ""
	}
	if isLoggerType(expr) {
		return "logger type " + exprString(expr)
	}
	logNamed := loggerParamName.MatchString(name) || strings.Contains(strings.ToLower(owner), "log")
	if ft, ok := expr.(*ast.FuncType); ok && isLogSink(ft) && logNamed {
		return "log function " + exprString(expr)
	}
	if ft, typeLogNamed := tree.namedFuncType(f, expr); ft != nil && isLogSink(ft) && (logNamed || typeLogNamed) {
		return "log function type " + exprString(expr)
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

// isLoggerAwareSetter reports whether a SetLogger method has the
// contract.LoggerAware shape: one contract.Logger parameter, no results.
func (f loggerSeamFile) isLoggerAwareSetter(d *ast.FuncDecl) bool {
	if d.Type.Results != nil && len(d.Type.Results.List) > 0 {
		return false
	}
	params := d.Type.Params.List
	return len(params) == 1 && len(params[0].Names) <= 1 && f.isContractLogger(params[0].Type)
}

// seamOffenders names every logger seam in the tree that does not take
// contract.Logger: a func or method parameter, exported or not, an
// exported struct field, or a SetLogger method not shaped like
// contract.LoggerAware's.
func (tree *loggerSeamTree) seamOffenders() []string {
	var offenders []string
	for _, f := range tree.files {
		for _, decl := range f.file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Type.Params == nil {
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
						if why := tree.loggerSeamOffence(f, d.Name.Name, n, p.Type); why != "" {
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
							if why := tree.loggerSeamOffence(f, n.Name, n.Name, fld.Type); why != "" {
								offenders = append(offenders, f.pos(fld)+": "+ts.Name.Name+"."+n.Name+": "+why)
							}
						}
					}
				}
			}
		}
	}
	sort.Strings(offenders)
	return offenders
}

// declarationOffenders names every logger type the tree declares: an
// interface with logger methods, a logger facet (a SetLogger method, which
// is contract.LoggerAware), an alias or defined type over a logger, or a
// log function type (a log sink whose name says it logs).
func (tree *loggerSeamTree) declarationOffenders() []string {
	var offenders []string
	for _, f := range tree.files {
		ast.Inspect(f.file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.TypeSpec:
				if _, iface := x.Type.(*ast.InterfaceType); !iface && isLoggerType(x.Type) {
					offenders = append(offenders, f.pos(x)+": type "+x.Name.Name+" over "+exprString(x.Type))
				}
				if ft, ok := x.Type.(*ast.FuncType); ok && isLogSink(ft) && namesLogging(x.Name.Name) {
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
	return offenders
}

// Every framework func or method outside contract and log that takes a
// logger, exported or not, takes contract.Logger, every exported struct
// field holding one is contract.Logger, and every SetLogger method is
// contract.LoggerAware's, so the app logger passes to each seam unchanged.
func TestFrameworkLoggerSeams_TakeContractLogger(t *testing.T) {
	if offenders := parseLoggerSeamFiles(t).seamOffenders(); len(offenders) > 0 {
		t.Errorf("logger seams that do not take contract.Logger:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// No package outside contract and log declares a logger interface: no
// interface with logger methods, no logger facet (a SetLogger method, which
// is contract.LoggerAware), no alias or defined type over a logger, and no
// log function type.
func TestFrameworkSource_DeclaresNoLoggerInterface(t *testing.T) {
	if offenders := parseLoggerSeamFiles(t).declarationOffenders(); len(offenders) > 0 {
		t.Errorf("logger interfaces declared outside contract and log (use contract.Logger / contract.LoggerAware):\n  %s", strings.Join(offenders, "\n  "))
	}
}

// TestLoggerSeamGuard_CatchesForbiddenSeams runs both guards on fixture
// sources and pins what each must name: a concrete logger parameter on an
// unexported function or method, and a named logging function type, both as
// a declaration and as the parameter type of a logger option, declared in
// the same package or imported. A fixture of seams that are fine names
// nothing.
func TestLoggerSeamGuard_CatchesForbiddenSeams(t *testing.T) {
	const module = "github.com/velocitykode/velocity"
	for _, tc := range []struct {
		name             string
		sources          map[string]string
		wantSeams        []string // substrings, one offender each
		wantDeclarations []string
	}{
		{
			name: "private concrete logger parameter",
			sources: map[string]string{"fixture/maint/maint.go": `package maint

import (
	"log/slog"

	"` + module + `/contract"
)

func markerPath(logger contract.Logger, fallback *slog.Logger) string { return "" }

type holder struct{}

func (holder) warn(sink *slog.Logger) {}

type Exported struct{}

func (Exported) warn(logger *slog.Logger) {}
`},
			wantSeams: []string{
				"fixture/maint/maint.go:9: markerPath(fallback): logger type *slog.Logger",
				"fixture/maint/maint.go:13: warn(sink): logger type *slog.Logger",
				"fixture/maint/maint.go:17: warn(logger): logger type *slog.Logger",
			},
		},
		{
			name: "named logging function type",
			sources: map[string]string{
				"fixture/sink/sink.go": `package sink

type LogFunc func(msg string, kvs ...any)

type Option func()

func WithLog(fn LogFunc) Option { return nil }

func withQueryLog(record LogFunc) {}
`,
				"fixture/user/user.go": `package user

import "` + module + `/fixture/sink"

func WithSink(s sink.LogFunc) sink.Option { return nil }
`,
			},
			wantSeams: []string{
				"fixture/sink/sink.go:7: WithLog(fn): log function type LogFunc",
				"fixture/sink/sink.go:9: withQueryLog(record): log function type LogFunc",
				"fixture/user/user.go:5: WithSink(s): log function type sink.LogFunc",
			},
			wantDeclarations: []string{
				"fixture/sink/sink.go:3: type LogFunc over func(...)",
			},
		},
		{
			name: "seams that are fine",
			sources: map[string]string{"fixture/fine/fine.go": `package fine

import "` + module + `/contract"

type HandlerFunc func(path string)

type Formatter func(value string) string

func Handle(h HandlerFunc)              {}
func format(f Formatter)                {}
func withLogger(logger contract.Logger) {}
`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sources []loggerSeamSource
			for p, src := range tc.sources {
				sources = append(sources, loggerSeamSource{path: p, src: []byte(src)})
			}
			tree := buildLoggerSeamTree(t, sources)
			for _, check := range []struct {
				kind string
				got  []string
				want []string
			}{
				{"seam", tree.seamOffenders(), tc.wantSeams},
				{"declaration", tree.declarationOffenders(), tc.wantDeclarations},
			} {
				want := slices.Sorted(slices.Values(check.want)) // offenders come sorted
				if len(check.got) != len(want) {
					t.Errorf("%s offenders = %q, want %d: %q", check.kind, check.got, len(want), want)
					continue
				}
				for i, w := range want {
					if !strings.Contains(check.got[i], w) {
						t.Errorf("%s offender %d = %q, want %q", check.kind, i, check.got[i], w)
					}
				}
			}
		})
	}
}
