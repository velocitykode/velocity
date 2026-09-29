// check-lock-held-calls reports calls to user code made while framework
// code holds a lock or runs inside a sync.Once. User code (a logger, a func
// value the caller supplied, a value formatted through its own String or
// Error) can panic, block, or call back into the component that called it;
// under the component's lock a call back deadlocks for good, and inside a
// Once it deadlocks every later caller.
//
// Held regions (per function, following statement order):
//
//   - x.Lock() / x.RLock() on a sync.Mutex or sync.RWMutex up to the
//     matching x.Unlock() / x.RUnlock() in the same block; nested blocks
//     inherit what is held, and an unlock inside a nested block ends the
//     region for the rest of that block only;
//   - defer x.Unlock(): held to the end of the function, including the
//     deferred calls registered after it (they run first, lock still held);
//   - once.Do(f), sync.OnceFunc(f), sync.OnceValue(f), sync.OnceValues(f):
//     the body of f (a literal, or a named function of the module).
//
// A func literal that is not invoked in place, and a go statement's body,
// start with nothing held.
//
// Calls flagged inside a held region:
//
//   - logger: Debug, Info, Warn, Error, Fatal or With on a value with the
//     logger method set, except the concrete fallback logger
//     (internal/fallbacklog.Logger, framework code writing to stderr);
//   - func: a call through a func-typed variable, field, parameter, map or
//     slice element, or call result (not a declared function or method,
//     not a literal);
//   - format: an fmt call with an interface-typed argument other than
//     error, and Error() or String() called on an interface value;
//   - reach: a call to a function of the module whose body makes one of the
//     calls above, directly or through other module functions (goroutine
//     bodies it starts excluded).
//
// Known limits: an error argument to fmt.Errorf is not flagged (wrapping is
// everywhere, and a framework error formats framework text); a plain
// interface method call is not flagged (io.Writer, hash.Hash, drivers); a
// lock taken inside a helper method is not tracked; files excluded by the
// current GOOS build constraints are not read.
//
// Suppression: a call that is fine under the lock carries a same-line
// `//lock-held-ok: <rationale>` comment, the rationale at least 5
// characters. A bare `//lock-held-ok:` does not suppress.
//
// Scope: the non-test files of the module's packages, except test
// infrastructure, excluded by directory: any directory whose name ends in
// "test" (cachetest, queuetest, fallbacklogtest, ...) or is "testing"
// (httpclient/testing, orm/testing, storage/testing: fakes and helpers for
// tests), internal/hostile, and scripts/.
//
// Type information comes from `go list -export` and the standard library
// importer, so the tool needs no dependency outside the standard library.
//
// Usage: go run ./scripts/ci/check-lock-held-calls [packages]
// Prints "file:line: kind: call while holding lock" per offender and exits
// 1 when there is any; prints nothing and exits 0 otherwise. -all prints
// every call to user code, held or not (for inventories).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func main() {
	all := flag.Bool("all", false, "print every call to user code, held or not")
	flag.Parse()
	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	hits, err := check(".", patterns, *all)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-lock-held-calls:", err)
		os.Exit(2)
	}
	for _, h := range hits {
		fmt.Println(h)
	}
	if len(hits) > 0 {
		os.Exit(1)
	}
}

type listedPackage struct {
	ImportPath      string
	Dir             string
	Export          string
	CompiledGoFiles []string
	Module          *struct{ Path, Dir string }
	Error           *struct{ Err string }
}

// check type-checks the module packages patterns name, run in dir, and
// returns the offenders, sorted.
func check(dir string, patterns []string, all bool) ([]string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	modOut, err := goCmd(absDir, "list", "-m", "-json")
	if err != nil {
		return nil, err
	}
	var mod struct{ Path, Dir string }
	if err := json.Unmarshal(modOut, &mod); err != nil {
		return nil, fmt.Errorf("go list -m: %w", err)
	}

	listOut, err := goCmd(absDir, append([]string{"list", "-e", "-export", "-compiled", "-deps", "-json"}, patterns...)...)
	if err != nil {
		return nil, err
	}
	exports := map[string]string{}
	var targets []listedPackage
	dec := json.NewDecoder(bytes.NewReader(listOut))
	for {
		var p listedPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list: %w", err)
		}
		if p.Error != nil {
			return nil, fmt.Errorf("go list %s: %s", p.ImportPath, p.Error.Err)
		}
		exports[p.ImportPath] = p.Export
		if p.Module != nil && p.Module.Path == mod.Path && !excluded(mod.Path, p.ImportPath) {
			targets = append(targets, p)
		}
	}

	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		f := exports[path]
		if f == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(f)
	})
	a := &analysis{fset: fset, root: mod.Dir, funcs: map[string]*funcSummary{}, all: all}
	for _, p := range targets {
		u := &unit{info: &types.Info{
			Types:      map[ast.Expr]types.TypeAndValue{},
			Uses:       map[*ast.Ident]types.Object{},
			Defs:       map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
		}}
		for _, f := range p.CompiledGoFiles {
			if !filepath.IsAbs(f) {
				f = filepath.Join(p.Dir, f)
			}
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			af, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
			if err != nil {
				return nil, err
			}
			u.files = append(u.files, af)
		}
		var typeErr error
		conf := types.Config{Importer: imp, Error: func(err error) {
			if typeErr == nil {
				typeErr = err
			}
		}}
		if _, err := conf.Check(p.ImportPath, fset, u.files, u.info); err != nil && typeErr == nil {
			typeErr = err
		}
		if typeErr != nil {
			return nil, fmt.Errorf("type-check %s: %w", p.ImportPath, typeErr)
		}
		a.units = append(a.units, u)
	}
	return a.run(), nil
}

func goCmd(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return out, nil
}

// excluded reports whether a package is test infrastructure (see the
// package comment).
func excluded(module, path string) bool {
	rel := strings.TrimPrefix(strings.TrimPrefix(path, module), "/")
	if rel == "internal/hostile" || strings.HasPrefix(rel, "internal/hostile/") ||
		rel == "scripts" || strings.HasPrefix(rel, "scripts/") {
		return true
	}
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasSuffix(seg, "test") || seg == "testing" {
			return true
		}
	}
	return false
}

type unit struct {
	files []*ast.File
	info  *types.Info
}

// funcSummary is what one declared function does that counts as user code.
type funcSummary struct {
	direct  string   // the first user-code call it makes itself, "" if none
	callees []string // the module functions it calls
	reach   string   // how it reaches user code, "" if it does not
}

type analysis struct {
	fset  *token.FileSet
	root  string
	units []*unit
	funcs map[string]*funcSummary
	all   bool
	hits  map[string]bool
}

func (a *analysis) run() []string {
	a.hits = map[string]bool{}
	a.summarize()
	for _, u := range a.units {
		for _, f := range u.files {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
					w := &walker{a: a, u: u}
					w.walkFunc(fd.Body, held{})
				}
			}
		}
	}
	out := make([]string, 0, len(a.hits))
	for h := range a.hits {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// summarize records, for every declared function, the user code it calls
// and the module functions it calls, then propagates reach to a fixpoint.
func (a *analysis) summarize() {
	for _, u := range a.units {
		for _, f := range u.files {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				obj, _ := u.info.Defs[fd.Name].(*types.Func)
				if obj == nil {
					continue
				}
				s := &funcSummary{}
				a.funcs[funcKey(obj)] = s
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if _, ok := n.(*ast.GoStmt); ok {
						return false
					}
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if k, d := classify(u, call); k != "" && k != kindIface && s.direct == "" {
						s.direct = k + " " + d
					}
					if c := staticCallee(u, call); c != nil {
						s.callees = append(s.callees, funcKey(c))
					}
					return true
				})
				s.reach = s.direct
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, s := range a.funcs {
			if s.reach != "" {
				continue
			}
			for _, c := range s.callees {
				if cs, ok := a.funcs[c]; ok && cs.reach != "" {
					s.reach = shortName(c) + " -> " + cs.reach
					if len(s.reach) > 160 {
						s.reach = s.reach[:160] + "..."
					}
					changed = true
					break
				}
			}
		}
	}
}

const (
	kindLogger = "logger"
	kindFunc   = "func"
	kindFormat = "format"
	kindReach  = "reach"
	kindIface  = "iface" // interface method call: listed by -all only
)

// classify reports the kind of user code call is, "" when it is none.
func classify(u *unit, call *ast.CallExpr) (kind, desc string) {
	fun := ast.Unparen(call.Fun)
	if tv, ok := u.info.Types[fun]; ok && (tv.IsType() || tv.IsBuiltin()) {
		return "", ""
	}
	switch f := fun.(type) {
	case *ast.FuncLit:
		return "", ""
	case *ast.Ident:
		if _, ok := u.info.Uses[f].(*types.Var); ok {
			return kindFunc, f.Name
		}
		return "", ""
	case *ast.SelectorExpr:
		if sel, ok := u.info.Selections[f]; ok {
			switch sel.Kind() {
			case types.FieldVal:
				return kindFunc, types.ExprString(f)
			case types.MethodVal:
				recv, name := sel.Recv(), f.Sel.Name
				if isLoggerMethod(name) && isUserLogger(recv) {
					return kindLogger, types.ExprString(f)
				}
				if types.IsInterface(recv) {
					if name == "Error" || name == "String" {
						return kindFormat, types.ExprString(f)
					}
					return kindIface, types.ExprString(f)
				}
			}
			return "", ""
		}
		switch obj := u.info.Uses[f.Sel].(type) {
		case *types.Func:
			if obj.Pkg() != nil && obj.Pkg().Path() == "fmt" && formatsInterface(u, call) {
				return kindFormat, types.ExprString(f)
			}
		case *types.Var:
			return kindFunc, types.ExprString(f)
		}
		return "", ""
	case *ast.IndexExpr, *ast.IndexListExpr:
		if staticCallee(u, call) != nil {
			return "", ""
		}
	}
	return kindFunc, types.ExprString(fun)
}

func isLoggerMethod(name string) bool {
	switch name {
	case "Debug", "Info", "Warn", "Error", "Fatal", "With":
		return true
	}
	return false
}

// isUserLogger reports whether t has the logger method set and is not the
// concrete fallback logger.
func isUserLogger(t types.Type) bool {
	ms := types.NewMethodSet(t)
	if _, isPtr := t.(*types.Pointer); !isPtr && !types.IsInterface(t) {
		ms = types.NewMethodSet(types.NewPointer(t))
	}
	names := map[string]bool{}
	for i := 0; i < ms.Len(); i++ {
		names[ms.At(i).Obj().Name()] = true
	}
	for _, n := range []string{"Debug", "Info", "Warn", "Error", "Fatal", "With"} {
		if !names[n] {
			return false
		}
	}
	return !strings.HasSuffix(t.String(), "internal/fallbacklog.Logger")
}

// formatsInterface reports whether an fmt call has an interface-typed
// argument other than error.
func formatsInterface(u *unit, call *ast.CallExpr) bool {
	errType := types.Universe.Lookup("error").Type()
	for _, arg := range call.Args {
		tv, ok := u.info.Types[arg]
		if !ok || tv.Type == nil {
			continue
		}
		if types.IsInterface(tv.Type) && !types.Identical(tv.Type, errType) {
			return true
		}
	}
	return false
}

// staticCallee returns the declared function or concrete method call
// calls, or nil.
func staticCallee(u *unit, call *ast.CallExpr) *types.Func {
	fn := func(id *ast.Ident) *types.Func {
		f, _ := u.info.Uses[id].(*types.Func)
		return f
	}
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return fn(f)
	case *ast.SelectorExpr:
		if s, ok := u.info.Selections[f]; ok {
			if s.Kind() == types.MethodVal && !types.IsInterface(s.Recv()) {
				m, _ := s.Obj().(*types.Func)
				return m
			}
			return nil
		}
		return fn(f.Sel)
	case *ast.IndexExpr:
		return genericCallee(u, f.X)
	case *ast.IndexListExpr:
		return genericCallee(u, f.X)
	}
	return nil
}

func genericCallee(u *unit, x ast.Expr) *types.Func {
	switch x := x.(type) {
	case *ast.Ident:
		f, _ := u.info.Uses[x].(*types.Func)
		return f
	case *ast.SelectorExpr:
		f, _ := u.info.Uses[x.Sel].(*types.Func)
		return f
	}
	return nil
}

// funcKey names a function the same way whether its package was checked
// from source or imported from export data.
func funcKey(fn *types.Func) string {
	fn = fn.Origin()
	k := ""
	if fn.Pkg() != nil {
		k = fn.Pkg().Path() + "."
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		t := sig.Recv().Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := t.(*types.Named); ok {
			k += n.Obj().Name() + "."
		}
	}
	return k + fn.Name()
}

func shortName(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

// held is the set of locks and Onces held, keyed by what holds them.
type held map[string]bool

func (h held) with(k string) held {
	c := held{}
	for x := range h {
		c[x] = true
	}
	c[k] = true
	return c
}

func (h held) copy() held {
	c := held{}
	for x := range h {
		c[x] = true
	}
	return c
}

func (h held) String() string {
	ks := make([]string, 0, len(h))
	for k := range h {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ", ")
}

// syncOp reports whether call is a lock, unlock or Once.Do on a sync value,
// and the key naming that value.
func syncOp(u *unit, call *ast.CallExpr) (op, key string) {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	s, ok := u.info.Selections[sel]
	if !ok || s.Kind() != types.MethodVal {
		return "", ""
	}
	fn, _ := s.Obj().(*types.Func)
	if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != "sync" {
		return "", ""
	}
	x := types.ExprString(sel.X)
	switch fn.Name() {
	case "Lock":
		return "lock", x + ".Lock"
	case "RLock":
		return "lock", x + ".RLock"
	case "Unlock":
		return "unlock", x + ".Lock"
	case "RUnlock":
		return "unlock", x + ".RLock"
	case "Do":
		return "do", x + ".Do"
	}
	return "", ""
}

type walker struct {
	a        *analysis
	u        *unit
	deferred held // locks whose unlock is deferred in the current function
	pending  []pendingBody
}

type pendingBody struct {
	body *ast.BlockStmt
	held held
}

// walkFunc walks one function body with h held on entry, then the func
// literals it found that run separately.
func (w *walker) walkFunc(body *ast.BlockStmt, h held) {
	saved := w.deferred
	w.deferred = held{}
	w.block(body.List, h.copy())
	w.deferred = saved
	for len(w.pending) > 0 {
		p := w.pending[0]
		w.pending = w.pending[1:]
		w.walkFunc(p.body, p.held)
	}
}

func (w *walker) block(stmts []ast.Stmt, h held) {
	for _, s := range stmts {
		w.stmt(s, h)
	}
}

func (w *walker) stmt(s ast.Stmt, h held) {
	switch s := s.(type) {
	case *ast.ExprStmt:
		if call, ok := s.X.(*ast.CallExpr); ok {
			switch op, key := syncOp(w.u, call); op {
			case "lock":
				h[key] = true
				return
			case "unlock":
				delete(h, key)
				return
			}
		}
		w.expr(s.X, h)
	case *ast.DeferStmt:
		op, key := syncOp(w.u, s.Call)
		if op == "unlock" {
			w.deferred[key] = true
			return
		}
		for _, arg := range s.Call.Args {
			w.expr(arg, h)
		}
		// A deferred call runs at return, while every lock whose unlock
		// was deferred before it is still held.
		if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
			if !unlocks(w.u, lit.Body) {
				w.pending = append(w.pending, pendingBody{lit.Body, w.deferred.copy()})
			}
			return
		}
		w.call(s.Call, w.deferred.copy())
	case *ast.GoStmt:
		if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
			w.pending = append(w.pending, pendingBody{lit.Body, held{}})
		}
		for _, arg := range s.Call.Args {
			w.expr(arg, h)
		}
	case *ast.BlockStmt:
		w.block(s.List, h.copy())
	case *ast.IfStmt:
		if s.Init != nil {
			w.stmt(s.Init, h)
		}
		w.expr(s.Cond, h)
		w.block(s.Body.List, h.copy())
		if s.Else != nil {
			w.stmt(s.Else, h.copy())
		}
	case *ast.ForStmt:
		if s.Init != nil {
			w.stmt(s.Init, h)
		}
		w.expr(s.Cond, h)
		if s.Post != nil {
			w.stmt(s.Post, h.copy())
		}
		w.block(s.Body.List, h.copy())
	case *ast.RangeStmt:
		w.expr(s.X, h)
		w.block(s.Body.List, h.copy())
	case *ast.SwitchStmt:
		if s.Init != nil {
			w.stmt(s.Init, h)
		}
		w.expr(s.Tag, h)
		for _, c := range s.Body.List {
			cc := c.(*ast.CaseClause)
			for _, e := range cc.List {
				w.expr(e, h)
			}
			w.block(cc.Body, h.copy())
		}
	case *ast.TypeSwitchStmt:
		if s.Init != nil {
			w.stmt(s.Init, h)
		}
		w.stmt(s.Assign, h)
		for _, c := range s.Body.List {
			w.block(c.(*ast.CaseClause).Body, h.copy())
		}
	case *ast.SelectStmt:
		for _, c := range s.Body.List {
			cc := c.(*ast.CommClause)
			if cc.Comm != nil {
				w.stmt(cc.Comm, h)
			}
			w.block(cc.Body, h.copy())
		}
	case *ast.LabeledStmt:
		w.stmt(s.Stmt, h)
	case *ast.AssignStmt:
		for _, e := range s.Rhs {
			w.expr(e, h)
		}
		for _, e := range s.Lhs {
			w.expr(e, h)
		}
	case *ast.ReturnStmt:
		for _, e := range s.Results {
			w.expr(e, h)
		}
	case *ast.DeclStmt:
		ast.Inspect(s, func(n ast.Node) bool {
			if e, ok := n.(ast.Expr); ok {
				w.expr(e, h)
				return false
			}
			return true
		})
	case *ast.SendStmt:
		w.expr(s.Chan, h)
		w.expr(s.Value, h)
	case *ast.IncDecStmt:
		w.expr(s.X, h)
	}
}

// unlocks reports whether body unlocks something (a deferred closure that
// releases the lock itself).
func unlocks(u *unit, body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if op, _ := syncOp(u, c); op == "unlock" {
				found = true
			}
		}
		return !found
	})
	return found
}

// expr walks an expression evaluated with h held.
func (w *walker) expr(e ast.Expr, h held) {
	if e == nil {
		return
	}
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			w.pending = append(w.pending, pendingBody{x.Body, held{}})
			return false
		case *ast.CallExpr:
			for _, arg := range x.Args {
				w.expr(arg, h)
			}
			if lit, ok := x.Fun.(*ast.FuncLit); ok { // invoked in place
				w.block(lit.Body.List, h.copy())
				return false
			}
			if op, key := syncOp(w.u, x); op == "do" {
				w.onceBody(x.Args, h.with(key))
				w.expr(x.Fun, h)
				return false
			}
			if fn := staticCallee(w.u, x); fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == "sync" &&
				strings.HasPrefix(fn.Name(), "Once") {
				w.onceBody(x.Args, h.with("sync."+fn.Name()))
				return false
			}
			w.call(x, h)
			w.expr(x.Fun, h)
			return false
		}
		return true
	})
}

// onceBody walks the functions a Once runs, with h (including the Once)
// held.
func (w *walker) onceBody(args []ast.Expr, h held) {
	for _, arg := range args {
		switch x := ast.Unparen(arg).(type) {
		case *ast.FuncLit:
			w.walkFunc(x.Body, h)
		default:
			var fn *types.Func
			switch x := x.(type) {
			case *ast.Ident:
				fn, _ = w.u.info.Uses[x].(*types.Func)
			case *ast.SelectorExpr:
				if s, ok := w.u.info.Selections[x]; ok {
					fn, _ = s.Obj().(*types.Func)
				} else {
					fn, _ = w.u.info.Uses[x.Sel].(*types.Func)
				}
			}
			if fn == nil {
				w.report(arg.Pos(), h, kindFunc, types.ExprString(arg))
			} else if s, ok := w.a.funcs[funcKey(fn)]; ok && s.reach != "" {
				w.report(arg.Pos(), h, kindReach, fn.Name()+": "+s.reach)
			}
		}
	}
}

// call checks one call made with h held.
func (w *walker) call(call *ast.CallExpr, h held) {
	if len(h) == 0 && !w.a.all {
		return
	}
	if op, _ := syncOp(w.u, call); op != "" {
		return
	}
	if k, d := classify(w.u, call); k != "" {
		if k != kindIface || (w.a.all && len(h) == 0) {
			w.report(call.Pos(), h, k, d)
		}
		return
	}
	if len(h) == 0 {
		return
	}
	if fn := staticCallee(w.u, call); fn != nil {
		if s, ok := w.a.funcs[funcKey(fn)]; ok && s.reach != "" {
			w.report(call.Pos(), h, kindReach, fn.Name()+": "+s.reach)
		}
	}
}

var markerRE = regexp.MustCompile(`//lock-held-ok:\s*\S.{4,}`)

func (w *walker) report(pos token.Pos, h held, kind, desc string) {
	p := w.a.fset.Position(pos)
	if suppressed(p.Filename, p.Line) {
		return
	}
	rel, err := filepath.Rel(w.a.root, p.Filename)
	if err != nil {
		rel = p.Filename
	}
	line := fmt.Sprintf("%s:%d: %s: %s", filepath.ToSlash(rel), p.Line, kind, desc)
	if len(h) > 0 {
		line += " while holding " + h.String()
	}
	w.a.hits[line] = true
}

var fileLines = map[string][]string{}

// suppressed reports whether line of file carries the marker with a
// rationale.
func suppressed(file string, line int) bool {
	lines, ok := fileLines[file]
	if !ok {
		b, err := os.ReadFile(file)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false
		}
		lines = strings.Split(string(b), "\n")
		fileLines[file] = lines
	}
	return line-1 < len(lines) && markerRE.MatchString(lines[line-1])
}
