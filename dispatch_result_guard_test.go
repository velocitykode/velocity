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

// The guard below enforces that no framework package discards what an event
// dispatch returns: a dispatcher's error (a listener failed, or the event was
// dropped) goes to the failure policy (internal/eventemit), never to a blank
// assignment, a bare call statement, or a go or defer statement.
//
// A dispatcher is a function value of type
// func(context.Context, any) error (spelled with interface{} or any, or a
// named type standing for it), called through a parameter, a local, a
// struct field or a pointer loaded from an atomic.Pointer, plus any method
// named Dispatch or DispatchNow. Types are resolved from the source alone:
// struct fields, named types, and the first result of the package's
// functions and methods.

// dispatchResultFile is one parsed non-test framework file.
type dispatchResultFile struct {
	path    string
	dir     string
	fset    *token.FileSet
	file    *ast.File
	imports map[string]string // local name -> import path
}

// dispatchResultDecl is a declaration and the file that holds it, so names
// in it resolve against that file's imports.
type dispatchResultDecl struct {
	file *dispatchResultFile
	expr ast.Expr
}

// dispatchResultTree indexes the declarations the guard resolves types from.
type dispatchResultTree struct {
	files   []*dispatchResultFile
	types   map[string]dispatchResultDecl // dir + "." + type name
	fields  map[string]dispatchResultDecl // dir + "." + struct + "." + field
	results map[string]dispatchResultDecl // dir + "." + [recv "."] func -> first result
	vars    map[string]dispatchResultDecl // dir + "." + package-level var
}

// dispatchResultSource is one Go source file handed to the guard.
type dispatchResultSource struct {
	path string
	src  []byte
}

// literalDiscard matches the two spellings the listener-failure policy
// retired, whatever their type: a blank-assigned call of fn or dispatch.
var literalDiscard = regexp.MustCompile(`\b_\s*=\s*(fn|dispatch)\(`)

func buildDispatchResultTree(t *testing.T, sources []dispatchResultSource) *dispatchResultTree {
	t.Helper()
	tree := &dispatchResultTree{
		types:   map[string]dispatchResultDecl{},
		fields:  map[string]dispatchResultDecl{},
		results: map[string]dispatchResultDecl{},
		vars:    map[string]dispatchResultDecl{},
	}
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
		file := &dispatchResultFile{path: s.path, dir: path.Dir(s.path), fset: fset, file: f, imports: imports}
		tree.files = append(tree.files, file)
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch sp := spec.(type) {
					case *ast.TypeSpec:
						tree.types[file.dir+"."+sp.Name.Name] = dispatchResultDecl{file, sp.Type}
						if st, ok := sp.Type.(*ast.StructType); ok {
							for _, fl := range st.Fields.List {
								for _, n := range fl.Names {
									tree.fields[file.dir+"."+sp.Name.Name+"."+n.Name] = dispatchResultDecl{file, fl.Type}
								}
							}
						}
					case *ast.ValueSpec:
						if d.Tok == token.VAR && sp.Type != nil {
							for _, n := range sp.Names {
								tree.vars[file.dir+"."+n.Name] = dispatchResultDecl{file, sp.Type}
							}
						}
					}
				}
			case *ast.FuncDecl:
				if d.Type.Results == nil || len(d.Type.Results.List) == 0 {
					continue
				}
				key := file.dir + "." + d.Name.Name
				if d.Recv != nil && len(d.Recv.List) == 1 {
					key = file.dir + "." + recvTypeName(d.Recv.List[0].Type) + "." + d.Name.Name
				}
				tree.results[key] = dispatchResultDecl{file, d.Type.Results.List[0].Type}
			}
		}
	}
	return tree
}

// recvTypeName returns the type name of a receiver: T for T, *T, T[P].
func recvTypeName(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

// dirOf returns the module directory of an import path, or "" outside the
// module.
func dirOf(importPath string) string {
	if importPath == moduleImportPath {
		return "."
	}
	if rest, ok := strings.CutPrefix(importPath, moduleImportPath+"/"); ok {
		return rest
	}
	return ""
}

// named resolves a named type expression (T or pkg.T) to its declaration.
func (tree *dispatchResultTree) named(d dispatchResultDecl) (dispatchResultDecl, string, bool) {
	switch e := d.expr.(type) {
	case *ast.Ident:
		if decl, ok := tree.types[d.file.dir+"."+e.Name]; ok {
			return decl, e.Name, true
		}
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		if !ok {
			break
		}
		if dir := dirOf(d.file.imports[pkg.Name]); dir != "" {
			if decl, ok := tree.types[dir+"."+e.Sel.Name]; ok {
				return decl, e.Sel.Name, true
			}
		}
	}
	return dispatchResultDecl{}, "", false
}

// isDispatchType reports whether d's type is a dispatcher:
// func(context.Context, any) error, directly or through named types.
func (tree *dispatchResultTree) isDispatchType(d dispatchResultDecl) bool {
	for depth := 0; depth < 8 && d.expr != nil; depth++ {
		if p, ok := d.expr.(*ast.ParenExpr); ok {
			d.expr = p.X
			continue
		}
		if ft, ok := d.expr.(*ast.FuncType); ok {
			return isDispatchSignature(d.file, ft)
		}
		next, _, ok := tree.named(d)
		if !ok {
			return false
		}
		d = next
	}
	return false
}

// isDispatchSignature reports whether ft is func(context.Context, any) error.
func isDispatchSignature(f *dispatchResultFile, ft *ast.FuncType) bool {
	var params []ast.Expr
	for _, p := range ft.Params.List {
		n := len(p.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			params = append(params, p.Type)
		}
	}
	if len(params) != 2 || ft.Results == nil || len(ft.Results.List) != 1 || len(ft.Results.List[0].Names) > 1 {
		return false
	}
	ctx, ok := params[0].(*ast.SelectorExpr)
	if !ok || ctx.Sel.Name != "Context" {
		return false
	}
	if pkg, ok := ctx.X.(*ast.Ident); !ok || f.imports[pkg.Name] != "context" {
		return false
	}
	switch ev := params[1].(type) {
	case *ast.Ident:
		if ev.Name != "any" {
			return false
		}
	case *ast.InterfaceType:
		if len(ev.Methods.List) != 0 {
			return false
		}
	default:
		return false
	}
	res, ok := ft.Results.List[0].Type.(*ast.Ident)
	return ok && res.Name == "error"
}

// dispatchScope is the locals of one function declaration, flat: the guard
// does not model shadowing.
type dispatchScope struct {
	file   *dispatchResultFile
	locals map[string]dispatchResultDecl
}

// typeOf resolves the type of e from the scope and the tree, or returns a
// zero decl.
func (tree *dispatchResultTree) typeOf(sc *dispatchScope, e ast.Expr) dispatchResultDecl {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return tree.typeOf(sc, x.X)
	case *ast.Ident:
		if d, ok := sc.locals[x.Name]; ok {
			return d
		}
		if d, ok := tree.vars[sc.file.dir+"."+x.Name]; ok {
			return d
		}
	case *ast.FuncLit:
		return dispatchResultDecl{sc.file, x.Type}
	case *ast.StarExpr:
		inner := tree.typeOf(sc, x.X)
		if star, ok := inner.expr.(*ast.StarExpr); ok {
			return dispatchResultDecl{inner.file, star.X}
		}
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok {
			if _, isLocal := sc.locals[pkg.Name]; !isLocal {
				if dir := dirOf(sc.file.imports[pkg.Name]); dir != "" {
					if d, ok := tree.vars[dir+"."+x.Sel.Name]; ok {
						return d
					}
				}
			}
		}
		return tree.fieldOf(tree.typeOf(sc, x.X), x.Sel.Name)
	case *ast.CallExpr:
		switch fun := x.Fun.(type) {
		case *ast.Ident:
			if d, ok := tree.results[sc.file.dir+"."+fun.Name]; ok {
				return d
			}
		case *ast.SelectorExpr:
			recv := tree.typeOf(sc, fun.X)
			if fun.Sel.Name == "Load" {
				if elem, ok := atomicPointerElem(recv); ok {
					return dispatchResultDecl{recv.file, &ast.StarExpr{X: elem}}
				}
			}
			if decl, name, ok := tree.structOf(recv); ok {
				if d, ok := tree.results[decl.file.dir+"."+name+"."+fun.Sel.Name]; ok {
					return d
				}
			}
		}
	}
	return dispatchResultDecl{}
}

// atomicPointerElem returns T for a type atomic.Pointer[T].
func atomicPointerElem(d dispatchResultDecl) (ast.Expr, bool) {
	if d.expr == nil {
		return nil, false
	}
	ix, ok := d.expr.(*ast.IndexExpr)
	if !ok {
		return nil, false
	}
	sel, ok := ix.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Pointer" {
		return nil, false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || d.file.imports[pkg.Name] != "sync/atomic" {
		return nil, false
	}
	return ix.Index, true
}

// structOf resolves d, a named type or a pointer to one, to its declaration
// and name.
func (tree *dispatchResultTree) structOf(d dispatchResultDecl) (dispatchResultDecl, string, bool) {
	if d.expr == nil {
		return dispatchResultDecl{}, "", false
	}
	if star, ok := d.expr.(*ast.StarExpr); ok {
		d.expr = star.X
	}
	return tree.named(d)
}

// fieldOf returns the declared type of field name of the struct d names.
func (tree *dispatchResultTree) fieldOf(d dispatchResultDecl, name string) dispatchResultDecl {
	decl, typeName, ok := tree.structOf(d)
	if !ok {
		return dispatchResultDecl{}
	}
	if f, ok := tree.fields[decl.file.dir+"."+typeName+"."+name]; ok {
		return f
	}
	return dispatchResultDecl{}
}

// collectLocals records the declared types of fn's receiver, parameters,
// named results, nested function literals' parameters, var declarations
// with a type, and single-value := and = assignments the tree can resolve.
func (tree *dispatchResultTree) collectLocals(sc *dispatchScope, recv *ast.FieldList, ft *ast.FuncType, body *ast.BlockStmt) {
	addFields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				sc.locals[n.Name] = dispatchResultDecl{sc.file, f.Type}
			}
		}
	}
	addFields(recv)
	addFields(ft.Params)
	addFields(ft.Results)
	if body == nil {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.FuncLit:
			addFields(s.Type.Params)
			addFields(s.Type.Results)
		case *ast.ValueSpec:
			if s.Type != nil {
				for _, name := range s.Names {
					sc.locals[name.Name] = dispatchResultDecl{sc.file, s.Type}
				}
			}
		case *ast.AssignStmt:
			if len(s.Lhs) == len(s.Rhs) {
				for i, l := range s.Lhs {
					id, ok := l.(*ast.Ident)
					if !ok || id.Name == "_" {
						continue
					}
					if d := tree.typeOf(sc, s.Rhs[i]); d.expr != nil {
						sc.locals[id.Name] = d
					}
				}
			}
		}
		return true
	})
}

// isDispatchCall reports whether call calls a dispatcher.
func (tree *dispatchResultTree) isDispatchCall(sc *dispatchScope, call *ast.CallExpr) bool {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Dispatch" || sel.Sel.Name == "DispatchNow") {
		return true
	}
	return tree.isDispatchType(tree.typeOf(sc, call.Fun))
}

// discardedDispatches returns every dispatch call in the tree whose result
// is discarded, as "path:line: source".
func (tree *dispatchResultTree) discardedDispatches() []string {
	var offenders []string
	report := func(f *dispatchResultFile, n ast.Node) {
		pos := f.fset.Position(n.Pos())
		offenders = append(offenders, f.path+":"+strconv.Itoa(pos.Line))
	}
	for _, f := range tree.files {
		for _, decl := range f.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			sc := &dispatchScope{file: f, locals: map[string]dispatchResultDecl{}}
			tree.collectLocals(sc, fd.Recv, fd.Type, fd.Body)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				var call *ast.CallExpr
				switch s := n.(type) {
				case *ast.ExprStmt:
					call, _ = s.X.(*ast.CallExpr)
				case *ast.GoStmt:
					call = s.Call
				case *ast.DeferStmt:
					call = s.Call
				case *ast.AssignStmt:
					if len(s.Rhs) != 1 {
						return true
					}
					for _, l := range s.Lhs {
						if id, ok := l.(*ast.Ident); !ok || id.Name != "_" {
							return true
						}
					}
					call, _ = s.Rhs[0].(*ast.CallExpr)
				}
				if call != nil && tree.isDispatchCall(sc, call) {
					report(f, call)
				}
				return true
			})
		}
	}
	return offenders
}

// dispatchResultOffenders runs the guard over sources: the typed check and
// the literal spellings.
func dispatchResultOffenders(t *testing.T, sources []dispatchResultSource) []string {
	t.Helper()
	tree := buildDispatchResultTree(t, sources)
	seen := map[string]bool{}
	for _, o := range tree.discardedDispatches() {
		seen[o] = true
	}
	for _, s := range sources {
		for i, line := range strings.Split(string(s.src), "\n") {
			if literalDiscard.MatchString(line) {
				seen[s.path+":"+strconv.Itoa(i+1)] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

// No framework package discards what an event dispatch returns.
func TestFrameworkPackages_UseEveryDispatchResult(t *testing.T) {
	var sources []dispatchResultSource
	walkNonTestGo(t, ".", func(p string, src []byte) {
		sources = append(sources, dispatchResultSource{path: p, src: src})
	})
	if offenders := dispatchResultOffenders(t, sources); len(offenders) > 0 {
		t.Errorf("dispatch results discarded (hand them to the failure policy, internal/eventemit):\n  %s", strings.Join(offenders, "\n  "))
	}
}

// The guard itself: each way of discarding a dispatch result is caught, and
// a used result, a function of another type and an unrelated method named
// like a dispatch are not.
func TestDispatchResultGuard_CatchesEachDiscard(t *testing.T) {
	const pkg = `package comp

import (
	"context"
	"sync/atomic"
)

type dispatchFn func(ctx context.Context, event interface{}) error

type Comp struct {
	fn   func(ctx context.Context, event any) error
	slot atomic.Pointer[dispatchFn]
	note func(ctx context.Context, event any)
	repo *Repo
}

type Repo struct{}

func (Repo) MarkCallbackDispatched(ctx context.Context, id string) error { return nil }

func (c *Comp) current() func(ctx context.Context, event any) error { return c.fn }
`
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"bare field call", `c.fn(ctx, ev)`, true},
		{"blank local from field", `fn := c.fn
	_ = fn(ctx, ev)`, true},
		{"blank param", `_ = dispatch(ctx, ev)`, true},
		{"bare call through atomic load", `p := c.slot.Load()
	(*p)(ctx, ev)`, true},
		{"bare call of method result", `d := c.current()
	d(ctx, ev)`, true},
		{"go statement", `go c.fn(ctx, ev)`, true},
		{"blank Dispatch method", `_ = disp.Dispatch(ctx, ev)`, true},
		{"used result", `if err := c.fn(ctx, ev); err != nil {
		return
	}`, false},
		{"returned result", `err := dispatch(ctx, ev)
	_ = err`, false},
		{"function without a result", `c.note(ctx, ev)`, false},
		{"method named like a dispatch", `_ = c.repo.MarkCallbackDispatched(ctx, "b")`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := pkg + `
type disper interface{ Dispatch(ctx context.Context, event any) error }

func (c *Comp) run(ctx context.Context, ev any, dispatch func(context.Context, any) error, disp disper) {
	` + tc.body + `
}
`
			got := dispatchResultOffenders(t, []dispatchResultSource{{path: "comp/comp.go", src: []byte(src)}})
			if (len(got) > 0) != tc.want {
				t.Errorf("offenders = %v, want flagged = %v", got, tc.want)
			}
		})
	}
}
