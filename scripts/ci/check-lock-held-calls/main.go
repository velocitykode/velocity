// check-lock-held-calls reports calls to user code made while framework
// code holds a lock or runs inside a sync.Once. User code (a logger, a func
// value the caller supplied, a value formatted through its own String or
// Error, a value an encoder or io.Copy calls into) can panic, block, or call
// back into the component that called it; under the component's lock a call
// back deadlocks for good, and inside a Once it deadlocks every later caller.
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
//   - a call to a lock-returning helper: a function of the module that
//     returns a func value and still holds a lock when it returns, on any
//     path (lockPath() { mu.Lock(); return mu.Unlock }). The lock is held
//     from the call until the variable the result was assigned to is
//     called, or the lock is unlocked; a lock naming the helper's receiver
//     or a parameter is renamed to the caller's expression for it;
//   - deferred statements run in execution order: at every return, and at
//     an explicit panic, they run last registered first, each with what is
//     held at that point, and each one's unlocks and locks change what the
//     ones registered before it run with. A deferred literal is walked
//     whole, so `defer func() { l.Warn("x"); mu.Unlock() }()` is reported
//     and `defer func() { mu.Unlock(); l.Warn("x") }()` is not;
//   - once.Do(f), sync.OnceFunc(f), sync.OnceValue(f), sync.OnceValues(f):
//     the body of f (a literal, or a named function of the module).
//
// A func literal invoked in place is walked as a function of its own, with
// what is held at the call, and leaves held what it holds when it returns.
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
//     not a literal, not the release func of a lock-returning helper);
//   - format: an fmt call, or a call to internal/errchain's Errorf,
//     Sprintf or Sprint (the contained forms, whose bodies are not
//     followed), with an interface-typed argument other than error, and
//     the error or fmt.Stringer method called on an interface value;
//   - callback: a call to encoding/json, encoding/xml, encoding/gob or io
//     that calls methods of a value it is given (Marshal, Unmarshal,
//     Encoder.Encode, Decoder.Decode, io.Copy, io.ReadAll, io.ReadFull and
//     their siblings; callbacks.go lists them all), when that value can be
//     any code: its type, walked the way the encoder walks it (exported
//     fields, json:"-" and xml:"-" skipped, pointers, slices, arrays, map
//     keys and elements), holds an interface or a type parameter, or it is
//     a stdlib encoder, decoder or reader wrapping one. A decode into a
//     local variable declared without a value and not used before the call
//     finds only nil interfaces and is not flagged. A concrete type's own
//     MarshalJSON (or MarshalText, UnmarshalJSON, GobEncode, Read, Write
//     and the rest) declared in the module is followed as reach; one
//     declared in the standard library or a dependency is fixed code;
//   - ctx: a context the caller supplied, used under the lock (ctx.go): a
//     Done, Err, Value or Deadline call on a context of interface type; a
//     call outside the module given such a context that is not owned
//     (built from context.Background or TODO through the context
//     package's With functions; WithoutCancel and WithValue of a caller's
//     context still reach its Value), or given a value that kept one
//     (a *sql.Tx begun with it); and an interface method given any
//     context, which is a pluggable store, driver or backend;
//   - statement: a database/sql call that runs a statement or closes a
//     result set (Exec*, Query*, QueryRow*, Prepare* on a DB, Conn, Tx or
//     Stmt; Row.Scan; Rows.Next, NextResultSet, Close), whose context is
//     not one internal/ownctx Hold or HoldDetached built: a pool the ORM's
//     drivers package opened runs its statement observer and query logger
//     inside it, unless the context holds them until the lock is released
//     (statement.go); and a method called on an interface declared in
//     database/sql/driver or on the module's orm/drivers.StatementObserver;
//   - hold (not a call under a lock): an internal/ownctx Hold or
//     HoldDetached whose Held no `defer h.Release()` in the same function
//     releases (statement.go);
//   - rmw (not a call under a lock, rmw.go): in csrf, csrf/stores, auth,
//     auth/drivers/session and auth/drivers/schemes, a writer method (Set,
//     Put, Store) called on an interface-typed store after a reader method
//     (Get, Load, Exists) on the same receiver with the same key, outside
//     an internal/buildonce Group's or Serial's Do: two requests of one
//     session racing the pair each write their own value and the last
//     write wins. Use the
//     store's compare-and-set (LoadOrStore, UpdateShared, UpdateData) or
//     the per-key flight; a pair that is safe carries
//     `//store-rmw-ok: <rationale>`. On a BlacklistStore-typed receiver
//     the pair is IsBlacklisted then Add: branch on Add's result instead
//     of reading first;
//     `//store-rmw-ok: <rationale>`. The same rule reports, in those
//     packages, every unconditional delete (Delete, Forget, ForgetCtx) on
//     an interface-typed store outside the per-key flight: a delete
//     decided on an earlier read removes a record renewed in between. Use
//     the store's compare-and-delete inside its own atomic step; a delete
//     that is right whatever the record holds (a destroy, a retired id)
//     carries the marker;
//
// The flight shape. internal/buildonce is how a component runs user code (a
// store, a driver, a factory) in a step that must not overlap another: a
// Group's Do (one build per key at a time) and a Serial's Do (the
// transitions of one value, one at a time) run their function with no
// sync lock held, so nothing above reports a user-code call inside one.
// A sync.Mutex held across the same call is reported (ctx, func, logger,
// ...): serializing a store call with a plain mutex does not pass.
//
//   - reach: a call to a function of the module whose body makes one of the
//     calls above, directly or through other module functions. Only code
//     that runs during the call counts: the body itself, func literals it
//     invokes in place or defers, and Once.Do bodies. A goroutine it starts
//     and a func literal it only passes along or returns (a middleware
//     wrapper, a hook it installs) do not.
//
// Closed parameters: a func parameter of an unexported function or method
// is closed when every call site of it in the package's non-test files
// passes a func literal or a declared function (test files are outside the
// scope, so a test passing a hostile func does not open it). Calling a
// closed parameter counts as calling those bodies: a literal is walked with
// what is held at the call, so a user-code call in it is reported on its own
// line, and a declared function is reported as reach at the call when it
// reaches user code. The parameter is open (a call
// through it is a func call as above) when any call site passes another
// value (a variable, a field, a call result, a literal stored first), when
// the function is used other than by calling it (a method value, a method
// expression), when its name is a method of an interface in the package,
// when a call site spreads or forwards a tuple, or when the body assigns
// to the parameter or takes its address.
//
// Known limits: an error argument to fmt.Errorf is not flagged (wrapping is
// everywhere, and a framework error formats framework text); a plain
// interface method call is not flagged (io.Writer, hash.Hash; a driver's
// and a statement observer's are, see statement), so
// r.Read on an io.Reader is not either, while io.Copy of it is; a lock
// that is not a sync.Mutex or sync.RWMutex (a file lock) is not tracked;
// a stdlib value keeping a caller's context is known only from the keeper
// table in ctx.go (sql BeginTx, the http request builders); a context kept
// in a field is not owned, so passing one under a lock is reported;
// a panic that is not an explicit panic call is not a return point for
// deferred statements; a stdlib reader built by the framework (an HKDF
// reader, crypto/rand.Reader) looks like any io.Reader and needs a marker;
// files excluded by the current GOOS build constraints are not read.
//
// Suppression: a call that is fine under the lock carries a same-line
// `//lock-held-ok: <rationale>` comment, the rationale at least 5
// characters, saying why the held call is safe, or that it runs user code
// and its fix is filed. A bare `//lock-held-ok:` does not suppress, and the
// hit on its line says so. A marker that suppresses nothing is stale and
// is reported, so a marker cannot outlive the call it was written for.
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
	{kindStale, "stale: remove the //lock-held-ok: marker; nothing on its line runs user code under a lock any more"},
	{kindCallback, "callback: encode, decode or copy before taking the lock or after releasing it (json, gob, xml and io calls run methods of the values they are given)"},
	{kindStmt, "statement: run the statement on an ownctx.Hold context and release the Held after unlocking (the pool's statement observer and query logger then run off the lock); call a driver or observer method after unlocking"},
	{kindHold, "hold: release the Held on a defer registered right after the Hold (before the lock's deferred unlock), so a panic or early return still delivers its statement reports after the lock is released"},
	{kindRMW, "rmw: run the read and the write of one key as one step: inside the per-key flight (an internal/buildonce Group's Do), or as a compare-and-set the store offers (LoadOrStore, UpdateShared, UpdateData; on a BlacklistStore, branch on what Add returns and drop the IsBlacklisted read); a delete decided on a read of the record goes through the store's compare-and-delete (CompareAndDeleteCtx, or the store's own step under its lock)"},
	{kindCtx, "ctx: read the caller's context before taking the lock, and hand code under the lock a context the framework owns (built from context.Background)"},
}

// hints returns the fix lines for the kinds in hits, and the marker
// syntax.
func hints(hits []string) string {
	var b strings.Builder
	rmw := 0
	for _, h := range hits {
		if strings.Contains(h, ": "+kindRMW+": ") {
			rmw++
		}
	}
	if n := len(hits) - rmw; n > 0 {
		fmt.Fprintf(&b, "%d call(s) to user code while a lock or sync.Once is held. User code can panic, block, or call back into this component.\n", n)
	}
	if rmw > 0 {
		fmt.Fprintf(&b, "%d store read(s) followed by a write of the same key, or unconditional store delete(s). Two requests of one session that both read before either writes each write their own value, and the last write wins; a delete decided on an earlier read removes a record renewed since.\n", rmw)
	}
	for _, f := range fixes {
		for _, h := range hits {
			if strings.Contains(h, ": "+f.kind+": ") {
				b.WriteString("  " + f.fix + "\n")
				break
			}
		}
	}
	if rmw < len(hits) {
		b.WriteString("  a call that is safe under the lock: same-line //lock-held-ok: <rationale of at least 5 characters>\n")
	}
	if rmw > 0 {
		b.WriteString("  a read-then-write or a delete that is safe: same-line //store-rmw-ok: <rationale of at least 5 characters> on the write or the delete\n")
	}
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
	a := &analysis{fset: fset, root: mod.Dir, funcs: map[string]*funcSummary{}, lits: map[string]*ast.FuncLit{}, inClosed: map[*ast.FuncLit]bool{}, all: all}
	for _, p := range targets {
		u := &unit{report: !p.DepOnly, module: mod.Path, path: p.ImportPath, info: &types.Info{
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
		u.indexZeroVars()
		u.indexCtxVars()
		u.indexSQLVars()
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
	report bool   // named by the patterns, not only a dependency of one
	module string // the module's path: methods declared under it are module code
	path   string // the package's import path

	// zeroVars are the local variables declared without a value (var v T);
	// uses holds every position each of them is used at. Together they tell
	// a decode into a variable still at its zero value (see fresh).
	zeroVars map[*types.Var]bool
	uses     map[*types.Var][]token.Pos

	// The module's logger interface and fallback logger as this unit sees
	// them (its own objects when it declares them, the imported ones
	// otherwise, so identity holds within the unit); nil when absent.
	logger   *types.Interface
	fallback types.Type

	// closed maps each closed func parameter (see the package comment) to
	// the keys of the bodies its call sites pass, in a.funcs.
	closed map[*types.Var][]string

	// ctxIface is context.Context as this unit sees it, nil when absent.
	// ctxAssigns, ctxParams and ctxAddr describe the unit's local context
	// variables, for ownedCtx (ctx.go).
	ctxIface   *types.Interface
	ctxAssigns map[*types.Var][]ast.Expr
	ctxParams  map[*types.Var]bool
	ctxAddr    map[*types.Var]bool
	// ctxCarriers maps each local variable holding a value that keeps a
	// caller's context (ctx.go) to the call that built it.
	ctxCarriers map[*types.Var]string
	// sqlAssigns maps each local variable to every value assigned to it,
	// for heldResult (statement.go).
	sqlAssigns map[*types.Var][]ast.Expr
	// ctxParamArgs maps each closed context parameter (statement.go) to the
	// arguments its call sites pass.
	ctxParamArgs map[*types.Var][]ast.Expr
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
	if t := lookup("context", "Context"); t != nil {
		u.ctxIface, _ = t.Underlying().(*types.Interface)
	}
}

// funcSummary is what one declared function does that counts as user code.
type funcSummary struct {
	direct  string   // the first user-code call it makes itself, "" if none
	callees []string // the module functions it calls
	reach   string   // how it reaches user code, "" if it does not

	// holds are the locks a function returning a func value still holds
	// when it returns: a lock-returning helper, whose result releases them.
	// A key naming the receiver or a parameter starts with a placeholder
	// (see hole) that a call site replaces with its own expression.
	holds []string
}

type analysis struct {
	fset  *token.FileSet
	root  string
	units []*unit
	funcs map[string]*funcSummary
	lits  map[string]*ast.FuncLit // the literal each "func literal at" key names
	all   bool
	hits  map[string]bool

	inClosed map[*ast.FuncLit]bool // closed-parameter literals being walked

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
	a.staleMarkers()
	a.unreleasedHolds()
	a.storeRMW()
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
		a.closeParams(u)
	}
	for _, u := range a.units {
		u.indexCarriers()
	}
	a.lockEffects()
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
				s := summarizeBody(a, u, fd.Body)
				if old := a.funcs[funcKey(obj)]; old != nil {
					s.holds = old.holds
				}
				a.funcs[funcKey(obj)] = s
			}
		}
	}
	// A format entry's body formats its caller's operands, which the call
	// site is flagged for instead (formatEntry).
	for _, u := range a.units {
		for _, obj := range u.info.Defs {
			if fn, ok := obj.(*types.Func); ok && formatEntry(u.module, fn) {
				if s := a.funcs[funcKey(fn)]; s != nil {
					s.reach, s.callees = "", nil
				}
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

// summarizeBody records the user code body calls and the module functions
// it calls, counting only the code that runs during the call.
func summarizeBody(a *analysis, u *unit, body *ast.BlockStmt) *funcSummary {
	s := &funcSummary{}
	// A call through a variable holding a lock-returning helper's result
	// releases a lock; it runs no user code.
	releases := map[types.Object]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
			if call, ok := ast.Unparen(as.Rhs[0]).(*ast.CallExpr); ok && len(a.holdsOf(u, call)) > 0 {
				for _, obj := range assignedObjects(u, as) {
					releases[obj] = true
				}
			}
		}
		return true
	})
	// Only the literals that run during the call count; a call is visited
	// before its operands, so it marks them first.
	runs := map[*ast.FuncLit]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
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
		if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok && releases[u.info.Uses[id]] {
			return true
		}
		if k, d := classify(u, call, body); k != "" && k != kindIface && s.direct == "" {
			s.direct = k + " " + d
		}
		if c := staticCallee(u, call); c != nil {
			s.callees = append(s.callees, funcKey(c))
		}
		for _, m := range u.callback(call, body).methods {
			s.callees = append(s.callees, funcKey(m))
		}
		for _, m := range u.ctxCall(call).methods {
			s.callees = append(s.callees, funcKey(m))
		}
		s.callees = append(s.callees, u.closedCall(call)...)
		return true
	})
	s.reach = s.direct
	return s
}

// closeParams finds u's closed func parameters (see the package comment)
// and summarizes the func literals their call sites pass, keyed in a.funcs
// by position.
func (a *analysis) closeParams(u *unit) {
	u.closed = map[*types.Var][]string{}
	u.ctxParamArgs = map[*types.Var][]ast.Expr{}
	decls := map[*types.Func]*ast.FuncDecl{}
	ifaceMethods := map[string]bool{}
	for _, f := range u.files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil && !fd.Name.IsExported() {
				if obj, _ := u.info.Defs[fd.Name].(*types.Func); obj != nil {
					decls[obj] = fd
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if it, ok := n.(*ast.InterfaceType); ok {
				for _, m := range it.Methods.List {
					for _, name := range m.Names {
						ifaceMethods[name.Name] = true
					}
				}
			}
			return true
		})
	}
	if len(decls) == 0 {
		return
	}
	// calls holds each function's call sites; open marks the functions
	// used other than by a plain call.
	calls := map[*types.Func][]*ast.CallExpr{}
	called := map[*ast.Ident]bool{}
	open := map[*types.Func]bool{}
	for _, f := range u.files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn := staticCallee(u, call)
			if fn == nil {
				return true
			}
			fn = fn.Origin()
			if _, ok := decls[fn]; !ok {
				return true
			}
			if id := calleeIdent(call.Fun); id != nil {
				called[id] = true
			}
			if call.Ellipsis.IsValid() {
				open[fn] = true
			}
			calls[fn] = append(calls[fn], call)
			return true
		})
	}
	for id, obj := range u.info.Uses {
		if fn, ok := obj.(*types.Func); ok && !called[id] {
			open[fn.Origin()] = true
		}
	}
	// Walk the declarations in source order so literal keys and summaries
	// are the same on every run.
	fns := make([]*types.Func, 0, len(decls))
	for fn := range decls {
		fns = append(fns, fn)
	}
	sort.Slice(fns, func(i, j int) bool { return fns[i].Pos() < fns[j].Pos() })
	for _, fn := range fns {
		fd := decls[fn]
		if open[fn] || ifaceMethods[fn.Name()] {
			continue
		}
		sig := fn.Type().(*types.Signature)
		u.indexCtxParamArgs(fd, sig, calls[fn])
		if sig.Variadic() {
			// Keep it simple: a variadic function's arguments may not map
			// one to one onto its parameters.
			continue
		}
		reassigned := assignedParams(u, fd.Body)
		for i := 0; i < sig.Params().Len(); i++ {
			param := sig.Params().At(i)
			if _, ok := param.Type().Underlying().(*types.Signature); !ok || param.Name() == "" || param.Name() == "_" || reassigned[param] {
				continue
			}
			var keys []string
			closed := true
			for _, call := range calls[fn] {
				if len(call.Args) != sig.Params().Len() {
					closed = false
					break
				}
				key := a.bodyKey(u, call.Args[i])
				if key == "" {
					closed = false
					break
				}
				keys = append(keys, key)
			}
			if closed {
				sort.Strings(keys)
				u.closed[param] = slicesCompact(keys)
			}
		}
	}
}

// calleeIdent returns the identifier naming the function a call's Fun
// expression calls, nil when there is none.
func calleeIdent(fun ast.Expr) *ast.Ident {
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		return f
	case *ast.SelectorExpr:
		return f.Sel
	case *ast.IndexExpr:
		return calleeIdent(f.X)
	case *ast.IndexListExpr:
		return calleeIdent(f.X)
	}
	return nil
}

// bodyKey returns the a.funcs key of the body arg names when arg is a func
// literal (summarized here) or a declared function or concrete method, ""
// otherwise.
func (a *analysis) bodyKey(u *unit, arg ast.Expr) string {
	switch x := ast.Unparen(arg).(type) {
	case *ast.FuncLit:
		p := a.fset.Position(x.Pos())
		key := fmt.Sprintf("func literal at %s:%d:%d", a.rel(p.Filename), p.Line, p.Column)
		if _, ok := a.funcs[key]; !ok {
			a.funcs[key] = summarizeBody(a, u, x.Body)
			a.lits[key] = x
		}
		return key
	case *ast.Ident:
		if fn, ok := u.info.Uses[x].(*types.Func); ok {
			return funcKey(fn)
		}
	case *ast.SelectorExpr:
		if s, ok := u.info.Selections[x]; ok {
			if s.Kind() == types.MethodVal && !types.IsInterface(s.Recv()) {
				if fn, ok := s.Obj().(*types.Func); ok {
					return funcKey(fn)
				}
			}
			return ""
		}
		if fn, ok := u.info.Uses[x.Sel].(*types.Func); ok {
			return funcKey(fn)
		}
	}
	return ""
}

// assignedParams returns the variables body assigns to or takes the
// address of.
func assignedParams(u *unit, body *ast.BlockStmt) map[*types.Var]bool {
	out := map[*types.Var]bool{}
	mark := func(e ast.Expr) {
		if id, ok := ast.Unparen(e).(*ast.Ident); ok {
			if v, ok := u.info.Uses[id].(*types.Var); ok {
				out[v] = true
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for _, e := range n.Lhs {
				mark(e)
			}
		case *ast.UnaryExpr:
			if n.Op == token.AND {
				mark(n.X)
			}
		}
		return true
	})
	return out
}

// slicesCompact drops adjacent duplicates from a sorted slice.
func slicesCompact(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// closedCall returns the body keys of the closed parameter call calls
// through, nil when it calls something else.
func (u *unit) closedCall(call *ast.CallExpr) []string {
	id, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok {
		return nil
	}
	v, ok := u.info.Uses[id].(*types.Var)
	if !ok {
		return nil
	}
	return u.closed[v]
}

const (
	kindLogger   = "logger"
	kindFunc     = "func"
	kindFormat   = "format"
	kindReach    = "reach"
	kindCallback = "callback"
	kindStale    = "stale"
	kindCtx      = "ctx"
	kindStmt     = "statement"
	kindHold     = "hold"
	kindIface    = "iface" // interface method call: listed by -all only
)

// classify reports the kind of user code call is, "" when it is none. body
// is the function body the call is in.
func classify(u *unit, call *ast.CallExpr, body *ast.BlockStmt) (kind, desc string) {
	fun := ast.Unparen(call.Fun)
	if tv, ok := u.info.Types[fun]; ok && (tv.IsType() || tv.IsBuiltin()) {
		return "", ""
	}
	if cb := u.callback(call, body); cb.open != "" {
		return kindCallback, types.ExprString(fun) + " " + cb.open
	}
	if cc := u.ctxCall(call); cc.open != "" {
		return kindCtx, types.ExprString(fun) + " " + cc.open
	}
	if why := u.statementCall(call); why != "" {
		return kindStmt, types.ExprString(fun) + " " + why
	}
	switch f := fun.(type) {
	case *ast.FuncLit:
		return "", ""
	case *ast.Ident:
		if v, ok := u.info.Uses[f].(*types.Var); ok {
			if _, closed := u.closed[v]; closed {
				return "", ""
			}
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
				if u.ctxMethodCall(recv, name) {
					return kindCtx, types.ExprString(f)
				}
				if types.IsInterface(recv) {
					if u.observerIface(recv) {
						return kindStmt, types.ExprString(f) + " (a database driver or statement observer method: any code)"
					}
					if formatCall(recv, name) {
						return kindFormat, types.ExprString(f)
					}
					if u.takesCtx(call) {
						return kindCtx, types.ExprString(f) + " (an interface method given a context: a store, driver or backend, any code)"
					}
					return kindIface, types.ExprString(f)
				}
			}
			return "", ""
		}
		switch obj := u.info.Uses[f.Sel].(type) {
		case *types.Func:
			if (obj.Pkg() != nil && obj.Pkg().Path() == "fmt" || formatEntry(u.module, obj)) && formatsInterface(u, call) {
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
// formatEntries are internal/errchain's contained forms of fmt's
// formatting. A call to one counts as the fmt call it stands for: flagged
// by its operands (formatsInterface), and never followed as reach into
// its body, whose interface calls format those same operands.
var formatEntries = map[string]bool{"Errorf": true, "Sprintf": true, "Sprint": true}

// formatEntry reports whether fn is one of the module's formatEntries.
func formatEntry(module string, fn *types.Func) bool {
	return fn.Pkg() != nil && fn.Pkg().Path() == module+"/internal/errchain" && formatEntries[fn.Name()] &&
		fn.Type().(*types.Signature).Recv() == nil
}

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
	a       *analysis
	u       *unit
	silent  bool // follow what is held only: report nothing, queue nothing
	scope   *scope
	pending []pendingBody
}

type pendingBody struct {
	body *ast.BlockStmt
	held held
}

// scope is one function body being walked: its deferred statements, what
// it holds when it returns, and its variables holding a lock-returning
// helper's result.
type scope struct {
	parent   *scope
	body     *ast.BlockStmt
	defers   []*deferred
	exit     held
	releases map[types.Object][]string
}

// deferred is one deferred statement: an unlock (or a call releasing
// locks), a literal to walk, or another call to check, with the locks held
// whenever it runs.
type deferred struct {
	unlock []string
	lit    *ast.FuncLit
	call   *ast.CallExpr
	entry  held
	ran    bool
}

// walkFunc walks one function body with h held on entry, then the func
// literals it found that run separately.
func (w *walker) walkFunc(body *ast.BlockStmt, h held) {
	w.runScope(body, h.copy())
	for len(w.pending) > 0 {
		p := w.pending[0]
		w.pending = w.pending[1:]
		w.runScope(p.body, p.held)
	}
}

// runScope walks body as a function of its own with h held on entry and
// returns what is held when it returns, once its deferred statements ran.
// Each deferred literal and call is then checked with every lock held at
// any of the times it runs.
func (w *walker) runScope(body *ast.BlockStmt, h held) held {
	s := &scope{parent: w.scope, body: body, exit: held{}, releases: map[types.Object][]string{}}
	w.scope = s
	if !w.block(body.List, h) {
		w.exitWith(h)
	}
	for _, d := range s.defers {
		if !d.ran || w.silent {
			continue
		}
		switch {
		case d.lit != nil:
			w.runScope(d.lit.Body, d.entry.copy())
		case d.call != nil:
			w.call(d.call, d.entry)
		}
	}
	w.scope = s.parent
	return s.exit
}

// exitWith records a return from the current function with h held: the
// deferred statements run last registered first, each with what the ones
// after it left held.
func (w *walker) exitWith(h held) {
	s := w.scope
	cur := h.copy()
	for i := len(s.defers) - 1; i >= 0; i-- {
		d := s.defers[i]
		d.ran = true
		for k := range cur {
			d.entry[k] = true
		}
		switch {
		case d.unlock != nil:
			for _, k := range d.unlock {
				delete(cur, k)
			}
		case d.lit != nil:
			sw := &walker{a: w.a, u: w.u, silent: true, scope: s}
			cur = sw.runScope(d.lit.Body, cur)
		}
	}
	for k := range cur {
		s.exit[k] = true
	}
}

// releaseKeys returns the locks a call through a variable holding a
// lock-returning helper's result releases, nil for any other call.
func (w *walker) releaseKeys(call *ast.CallExpr) []string {
	id, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok {
		return nil
	}
	obj := w.u.info.Uses[id]
	for s := w.scope; s != nil; s = s.parent {
		if keys, ok := s.releases[obj]; ok {
			return keys
		}
	}
	return nil
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
			if w.isPanic(call) {
				w.exitWith(h)
				return true
			}
			return false
		}
		w.expr(s.X, h)
	case *ast.DeferStmt:
		for _, arg := range s.Call.Args {
			w.expr(arg, h)
		}
		d := &deferred{entry: held{}}
		if op, key := syncOp(w.u, s.Call); op == "unlock" {
			d.unlock = []string{key}
		} else if keys := w.releaseKeys(s.Call); keys != nil {
			d.unlock = keys
		} else if lit, ok := ast.Unparen(s.Call.Fun).(*ast.FuncLit); ok {
			d.lit = lit
		} else if op == "" {
			d.call = s.Call
		}
		w.scope.defers = append(w.scope.defers, d)
	case *ast.GoStmt:
		if lit, ok := s.Call.Fun.(*ast.FuncLit); ok && !w.silent {
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
		if len(s.Rhs) == 1 {
			if call, ok := ast.Unparen(s.Rhs[0]).(*ast.CallExpr); ok {
				if keys := w.a.holdsOf(w.u, call); len(keys) > 0 {
					for _, obj := range assignedObjects(w.u, s) {
						w.scope.releases[obj] = keys
					}
				}
			}
		}
	case *ast.ReturnStmt:
		for _, e := range s.Results {
			w.expr(e, h)
		}
		w.exitWith(h)
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

// expr walks an expression evaluated with h held, updating h with the
// locks a call in it takes or releases.
func (w *walker) expr(e ast.Expr, h held) {
	if e == nil {
		return
	}
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			if !w.silent {
				w.pending = append(w.pending, pendingBody{x.Body, held{}})
			}
			return false
		case *ast.CallExpr:
			for _, arg := range x.Args {
				w.expr(arg, h)
			}
			if lit, ok := ast.Unparen(x.Fun).(*ast.FuncLit); ok { // invoked in place
				exit := w.runScope(lit.Body, h.copy())
				join(h, exit)
				return false
			}
			if keys := w.releaseKeys(x); keys != nil {
				for _, k := range keys {
					delete(h, k)
				}
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
			for _, k := range w.a.holdsOf(w.u, x) {
				h[k] = true
			}
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
			if w.silent {
				continue
			}
			w.runScope(x.Body, h.copy())
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
	if w.silent || (len(h) == 0 && !w.a.all) {
		return
	}
	if op, _ := syncOp(w.u, call); op != "" {
		return
	}
	if k, d := classify(w.u, call, w.scope.body); k != "" {
		if k != kindIface || (w.a.all && len(h) == 0) {
			w.report(call.Pos(), h, k, d)
		}
		return
	}
	if len(h) == 0 {
		return
	}
	methods := append(w.u.callback(call, w.scope.body).methods, w.u.ctxCall(call).methods...)
	for _, m := range methods {
		if s, ok := w.a.funcs[funcKey(m)]; ok && s.reach != "" {
			w.report(call.Pos(), h, kindReach, types.ExprString(call.Fun)+": "+shortName(funcKey(m))+" -> "+s.reach)
			return
		}
	}
	if fn := staticCallee(w.u, call); fn != nil {
		if s, ok := w.a.funcs[funcKey(fn)]; ok && s.reach != "" {
			w.report(call.Pos(), h, kindReach, fn.Name()+": "+s.reach)
		}
		return
	}
	// A closed parameter's literal bodies are walked with what is held
	// here, so each user-code call in them is reported on its own line
	// and a marker covers that call only; a declared function passed in
	// is reported here as reach.
	for _, k := range w.u.closedCall(call) {
		if lit, ok := w.a.lits[k]; ok {
			if !w.a.inClosed[lit] {
				w.a.inClosed[lit] = true
				w.runScope(lit.Body, h.copy())
				delete(w.a.inClosed, lit)
			}
			continue
		}
		if s, ok := w.a.funcs[k]; ok && s.reach != "" {
			w.report(call.Pos(), h, kindReach, types.ExprString(call.Fun)+": "+shortName(k)+" -> "+s.reach)
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

// staleMarkers adds every marker in a reported package that suppressed no
// call under a lock: the lock or the call it names is gone, and a marker
// left behind would silence the next call someone writes on that line.
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
