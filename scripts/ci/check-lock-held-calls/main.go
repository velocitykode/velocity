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
//     matching x.Unlock() / x.RUnlock(); nested blocks inherit what is
//     held. After an if or a switch, a lock is held when it is held at the
//     end of any branch that falls through (a branch that returns, panics,
//     continues or jumps does not count), so a lock released on every
//     branch is no longer held; a loop body's unlocks end the region inside
//     the body only;
//   - defer x.Unlock(): held to the end of the function, including the
//     deferred calls registered after it (they run first, lock still held);
//   - once.Do(f), sync.OnceFunc(f), sync.OnceValue(f), sync.OnceValues(f):
//     the body of f (a literal, or a named function of the module).
//
// A func literal that is not invoked in place, and a go statement's body,
// start with nothing held.
//
// Calls flagged inside a held region, told apart by type only (never by
// the name of a variable, field or package-local type):
//
//   - logger: a method of contract.Logger called on a value whose type
//     implements contract.Logger (the module's contract package), except
//     the concrete fallback logger internal/fallbacklog.Logger (framework
//     code writing to stderr);
//   - func: a call through a func-typed variable, field, parameter, map or
//     slice element, or call result (not a declared function or method,
//     not a literal);
//   - format: an fmt call with an interface-typed argument other than
//     error, and the error or fmt.Stringer method called on an interface
//     value;
//   - reach: a call to a function of the module whose body makes one of the
//     calls above, directly or through other module functions. Only code
//     that runs during the call counts: the body itself, func literals it
//     invokes in place or defers, and Once.Do bodies. A goroutine it starts
//     and a func literal it only passes along or returns (a middleware
//     wrapper, a hook it installs) do not.
//
// Known limits: an error argument to fmt.Errorf is not flagged (wrapping is
// everywhere, and a framework error formats framework text); a plain
// interface method call is not flagged (io.Writer, hash.Hash, drivers); a
// lock taken inside a helper method is not tracked; files excluded by the
// current GOOS build constraints are not read.
//
// Suppression: a call that is fine under the lock carries a same-line
// `//lock-held-ok: <rationale>` comment, the rationale at least 5
// characters. A bare `//lock-held-ok:` does not suppress, and the hit on
// its line says so. A marker that suppresses nothing is stale; stale
// markers are listed by -all only and never fail the check.
//
// Scope: the non-test files of the packages the patterns name, except test
// infrastructure, excluded by directory: any directory whose name ends in
// "test" (cachetest, queuetest, fallbacklogtest, ...) or is "testing"
// (httpclient/testing, orm/testing, storage/testing: fakes and helpers for
// tests), internal/hostile, and scripts/.
//
// The other packages of the module that those import are read too, for
// reach, but are not reported on.
//
// Type information comes from `go list -export` and the standard library
// importer, so the tool needs no dependency outside the standard library.
//
// Usage: go run ./scripts/ci/check-lock-held-calls [packages]
// Prints "file:line: kind: call while holding lock" per offender, then on
// stderr how to fix each kind reported and the marker syntax, and exits 1
// when there is any; prints nothing and exits 0 otherwise. -all prints
// every call to user code, held or not, and the stale markers (for
// inventories).
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
		if !*all {
			fmt.Fprint(os.Stderr, hints(hits))
		}
		os.Exit(1)
	}
}

// fixes says, per kind, how to take the call out of the held region.
var fixes = []struct{ kind, fix string }{
	{kindLogger, "logger: read the logger under the lock, unlock, then write through fallbacklog.Write (or a fallbacklog.Contain logger)"},
	{kindFunc, "func: copy the func under the lock and call it after unlocking; claim any state it guards atomically first"},
	{kindFormat, "format: format the value (its Error or String text) before taking the lock"},
	{kindReach, "reach: call the function after unlocking, or take the user-code call out of it (the chain after the name shows where it is)"},
}

// hints returns the fix lines for the kinds in hits, and the marker
// syntax.
func hints(hits []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d call(s) to user code while a lock or sync.Once is held. User code can panic, block, or call back into this component.\n", len(hits))
	for _, f := range fixes {
		for _, h := range hits {
			if strings.Contains(h, ": "+f.kind+": ") {
				b.WriteString("  " + f.fix + "\n")
				break
			}
		}
	}
	b.WriteString("  a call that is safe under the lock: same-line //lock-held-ok: <rationale of at least 5 characters>\n")
	return b.String()
}

type listedPackage struct {
	ImportPath      string
	Dir             string
	Export          string
	CompiledGoFiles []string
	DepOnly         bool
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
		u := &unit{report: !p.DepOnly, info: &types.Info{
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
		pkg, err := conf.Check(p.ImportPath, fset, u.files, u.info)
		if err != nil && typeErr == nil {
			typeErr = err
		}
		if typeErr != nil {
			return nil, fmt.Errorf("type-check %s: %w", p.ImportPath, typeErr)
		}
		u.resolve(pkg, imp, mod.Path)
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
	files  []*ast.File
	info   *types.Info
	report bool // named by the patterns, not only a dependency of one

	// The module's logger interface and fallback logger as this unit sees
	// them (its own objects when it declares them, the imported ones
	// otherwise, so identity holds within the unit); nil when absent.
	logger   *types.Interface
	fallback types.Type
}

var (
	errorIface = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
	stringer   = types.NewInterfaceType([]*types.Func{types.NewFunc(token.NoPos, nil, "String",
		types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.String])), false))}, nil).Complete()
)

// resolve looks up the logger interface and the fallback logger for u.
func (u *unit) resolve(pkg *types.Package, imp types.Importer, module string) {
	lookup := func(path, name string) types.Type {
		p := pkg
		if pkg.Path() != path {
			var err error
			if p, err = imp.Import(path); err != nil {
				return nil
			}
		}
		if tn, ok := p.Scope().Lookup(name).(*types.TypeName); ok {
			return tn.Type()
		}
		return nil
	}
	if t := lookup(module+"/contract", "Logger"); t != nil {
		u.logger, _ = t.Underlying().(*types.Interface)
	}
	u.fallback = lookup(module+"/internal/fallbacklog", "Logger")
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

	lines map[string][]string     // file contents by line, for markers
	used  map[string]map[int]bool // marker lines that suppressed a held call
}

func (a *analysis) run() []string {
	a.hits = map[string]bool{}
	a.lines = map[string][]string{}
	a.used = map[string]map[int]bool{}
	a.summarize()
	for _, u := range a.units {
		if !u.report {
			continue
		}
		for _, f := range u.files {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
					w := &walker{a: a, u: u}
					w.walkFunc(fd.Body, held{})
				}
			}
		}
	}
	if a.all {
		a.staleMarkers()
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
				// Only the literals that run during the call count; a
				// call is visited before its operands, so it marks them
				// first.
				runs := map[*ast.FuncLit]bool{}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.GoStmt:
						return false
					case *ast.FuncLit:
						return runs[n]
					}
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if lit, ok := ast.Unparen(call.Fun).(*ast.FuncLit); ok {
						runs[lit] = true
					}
					if op, _ := syncOp(u, call); op == "do" {
						for _, arg := range call.Args {
							if lit, ok := ast.Unparen(arg).(*ast.FuncLit); ok {
								runs[lit] = true
							}
						}
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
	// Walk the functions in a fixed order so the chain each reports is the
	// same on every run.
	keys := make([]string, 0, len(a.funcs))
	for k := range a.funcs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for changed := true; changed; {
		changed = false
		for _, k := range keys {
			s := a.funcs[k]
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
				if u.loggerCall(recv, name) {
					return kindLogger, types.ExprString(f)
				}
				if types.IsInterface(recv) {
					if formatCall(recv, name) {
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

// loggerCall reports whether calling method name on recv is a call to
// the logger interface: recv (or a pointer to it) implements it, name is
// one of its methods, and recv is not the fallback logger.
func (u *unit) loggerCall(recv types.Type, name string) bool {
	if u.logger == nil || !hasMethod(u.logger, name) {
		return false
	}
	if u.fallback != nil {
		base := recv
		if p, ok := base.(*types.Pointer); ok {
			base = p.Elem()
		}
		if types.Identical(base, u.fallback) {
			return false
		}
	}
	if types.Implements(recv, u.logger) {
		return true
	}
	_, isPtr := recv.(*types.Pointer)
	return !isPtr && !types.IsInterface(recv) && types.Implements(types.NewPointer(recv), u.logger)
}

// formatCall reports whether calling method name on the interface type
// recv is the error or fmt.Stringer method, which formats a value.
func formatCall(recv types.Type, name string) bool {
	for _, iface := range []*types.Interface{errorIface, stringer} {
		if hasMethod(iface, name) && types.Implements(recv, iface) {
			return true
		}
	}
	return false
}

func hasMethod(iface *types.Interface, name string) bool {
	for i := 0; i < iface.NumMethods(); i++ {
		if iface.Method(i).Name() == name {
			return true
		}
	}
	return false
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

// onceFuncs are the sync functions whose argument runs inside a Once.
var onceFuncs = map[string]bool{"OnceFunc": true, "OnceValue": true, "OnceValues": true}

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

// block walks stmts in order with h held, updating h to what is held at
// the end, and reports whether control never falls out of the end.
func (w *walker) block(stmts []ast.Stmt, h held) (terminates bool) {
	for _, s := range stmts {
		if w.stmt(s, h) {
			return true
		}
	}
	return false
}

// join sets h to the locks held at the end of any path that falls
// through (a nil path terminates) and reports whether none does.
func join(h held, paths ...held) (terminates bool) {
	for k := range h {
		delete(h, k)
	}
	terminates = true
	for _, p := range paths {
		if p == nil {
			continue
		}
		terminates = false
		for k := range p {
			h[k] = true
		}
	}
	return terminates
}

// branch walks one branch on a copy of h and returns what it holds at
// its end, nil when it does not fall through.
func (w *walker) branch(walk func(held) bool, h held) held {
	c := h.copy()
	if walk(c) {
		return nil
	}
	return c
}

// stmt walks s with h held, updating h, and reports whether control never
// falls out of s.
func (w *walker) stmt(s ast.Stmt, h held) (terminates bool) {
	switch s := s.(type) {
	case *ast.ExprStmt:
		if call, ok := s.X.(*ast.CallExpr); ok {
			switch op, key := syncOp(w.u, call); op {
			case "lock":
				h[key] = true
				return false
			case "unlock":
				delete(h, key)
				return false
			}
			w.expr(s.X, h)
			return w.isPanic(call)
		}
		w.expr(s.X, h)
	case *ast.DeferStmt:
		op, key := syncOp(w.u, s.Call)
		if op == "unlock" {
			w.deferred[key] = true
			return false
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
			return false
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
		return w.block(s.List, h)
	case *ast.IfStmt:
		if s.Init != nil {
			w.stmt(s.Init, h)
		}
		w.expr(s.Cond, h)
		body := w.branch(func(c held) bool { return w.block(s.Body.List, c) }, h)
		els := h.copy()
		if s.Else != nil {
			els = w.branch(func(c held) bool { return w.stmt(s.Else, c) }, h)
		}
		return join(h, body, els)
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
		var paths []held
		hasDefault := false
		for _, c := range s.Body.List {
			cc := c.(*ast.CaseClause)
			for _, e := range cc.List {
				w.expr(e, h)
			}
			hasDefault = hasDefault || cc.List == nil
			paths = append(paths, w.branch(func(c held) bool { return w.block(cc.Body, c) }, h))
		}
		if !hasDefault {
			paths = append(paths, h.copy())
		}
		return join(h, paths...)
	case *ast.TypeSwitchStmt:
		if s.Init != nil {
			w.stmt(s.Init, h)
		}
		w.stmt(s.Assign, h)
		var paths []held
		hasDefault := false
		for _, c := range s.Body.List {
			cc := c.(*ast.CaseClause)
			hasDefault = hasDefault || cc.List == nil
			paths = append(paths, w.branch(func(c held) bool { return w.block(cc.Body, c) }, h))
		}
		if !hasDefault {
			paths = append(paths, h.copy())
		}
		return join(h, paths...)
	case *ast.SelectStmt:
		var paths []held
		for _, c := range s.Body.List {
			cc := c.(*ast.CommClause)
			if cc.Comm != nil {
				w.stmt(cc.Comm, h)
			}
			paths = append(paths, w.branch(func(c held) bool { return w.block(cc.Body, c) }, h))
		}
		return join(h, paths...)
	case *ast.LabeledStmt:
		return w.stmt(s.Stmt, h)
	case *ast.BranchStmt:
		// break leaves the enclosing statement with what is held; the
		// others go elsewhere.
		return s.Tok != token.BREAK
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
		return true
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
	return false
}

// isPanic reports whether call is the panic builtin.
func (w *walker) isPanic(call *ast.CallExpr) bool {
	id, ok := ast.Unparen(call.Fun).(*ast.Ident)
	return ok && w.u.info.Uses[id] == types.Universe.Lookup("panic")
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
			if lit, ok := ast.Unparen(x.Fun).(*ast.FuncLit); ok { // invoked in place
				w.block(lit.Body.List, h.copy())
				return false
			}
			if op, key := syncOp(w.u, x); op == "do" {
				w.onceBody(x.Args, h.with(key))
				w.expr(x.Fun, h)
				return false
			}
			if fn := staticCallee(w.u, x); fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == "sync" &&
				onceFuncs[fn.Name()] {
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

const markerPrefix = "//lock-held-ok:"

var markerRE = regexp.MustCompile(`//lock-held-ok:\s*\S.{4,}`)

func (w *walker) report(pos token.Pos, h held, kind, desc string) {
	p := w.a.fset.Position(pos)
	text := w.a.line(p.Filename, p.Line)
	if len(h) > 0 && markerRE.MatchString(text) {
		if w.a.used[p.Filename] == nil {
			w.a.used[p.Filename] = map[int]bool{}
		}
		w.a.used[p.Filename][p.Line] = true
		return
	}
	line := fmt.Sprintf("%s:%d: %s: %s", w.a.rel(p.Filename), p.Line, kind, desc)
	if len(h) > 0 {
		line += " while holding " + h.String()
		if strings.Contains(text, markerPrefix) {
			line += " (the //lock-held-ok: marker here has no rationale of at least 5 characters, so it does not suppress)"
		}
	}
	w.a.hits[line] = true
}

func (a *analysis) rel(file string) string {
	rel, err := filepath.Rel(a.root, file)
	if err != nil {
		rel = file
	}
	return filepath.ToSlash(rel)
}

// line returns line n of file, "" when it cannot be read.
func (a *analysis) line(file string, n int) string {
	lines, ok := a.lines[file]
	if !ok {
		b, err := os.ReadFile(file)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return ""
		}
		lines = strings.Split(string(b), "\n")
		a.lines[file] = lines
	}
	if n-1 < len(lines) {
		return lines[n-1]
	}
	return ""
}

// staleMarkers adds, for -all, every marker in a reported package that
// suppressed no call under a lock.
func (a *analysis) staleMarkers() {
	for _, u := range a.units {
		if !u.report {
			continue
		}
		for _, f := range u.files {
			for _, cg := range f.Comments {
				for _, c := range cg.List {
					if !strings.HasPrefix(c.Text, markerPrefix) {
						continue
					}
					p := a.fset.Position(c.Pos())
					if !a.used[p.Filename][p.Line] {
						a.hits[fmt.Sprintf("%s:%d: stale: the //lock-held-ok: marker suppresses no call under a lock", a.rel(p.Filename), p.Line)] = true
					}
				}
			}
		}
	}
}
