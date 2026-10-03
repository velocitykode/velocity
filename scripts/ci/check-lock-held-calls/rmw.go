package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"regexp"
	"strings"
)

// The rmw rule: a store read followed by a write of the same key, on the
// request path of the packages that keep per-session state (rmwScope). Two
// requests of one session that run the pair at once both read "absent" (or
// the same old value) and both write: the last write wins and what the
// other request handed its client is lost (a CSRF token minted twice, a
// rotated remember token written over).
//
// Flagged: in one function (its nested literals included), a call of a
// writer method (Set, Put, Store) on a value of interface type, after a
// call of a reader method (Get, Load, Exists) on the same receiver
// expression with the same key, the first string argument. Not flagged: a
// write inside a func literal handed to an internal/buildonce Group's Do
// (the per-key flight: one read-then-write per key at a time) or Serial's
// Do (the transitions of one value, one at a time), and a
// compare-and-set method (any other name, LoadOrStore, UpdateShared, UpdateData).
//
// A JWT blacklist is such a store under its own names: on a receiver whose
// interface type is named BlacklistStore, IsBlacklisted is a reader and Add
// a writer. Add reports whether it consumed the JTI, so the fix for a
// flagged pair is to drop the read and branch on Add's result. Two refresh
// calls racing the pair both read "not blacklisted" and both issue.
// The delete shape of the same rule: a call of a delete method (Delete,
// Forget, ForgetCtx) on a value of interface type in those packages. A
// record removed there is removed whatever it holds when the delete lands:
// a delete decided on an earlier read of the record (it looked expired, or
// revoked) removes one a concurrent request renewed or wrote in between.
// The read need not be in the same function, or anywhere (the caller may
// have made it), so every such delete is flagged, a delete inside the
// per-key flight excepted. A conditional removal is a compare-and-delete
// method of the store (any other name: CompareAndDeleteCtx, UpdateData),
// which is not flagged.
//
// Suppression: a same-line `//store-rmw-ok: <rationale>` comment on the
// write or the delete, the rationale at least 5 characters, saying why the
// pair is idempotent, why no two requests share the key, or why the delete
// is right whatever the record holds (a destroy, a retired id). A marker
// that suppresses nothing is stale and is reported.

const kindRMW = "rmw"

// rmwScope lists the packages, relative to the module, whose stores hold
// per-session state written on the request path.
var rmwScope = map[string]bool{
	"csrf":                 true,
	"csrf/stores":          true,
	"auth":                 true,
	"auth/drivers/session": true,
	"auth/drivers/schemes": true,
}

var (
	rmwReaders = map[string]bool{"Get": true, "Load": true, "Exists": true}
	rmwWriters = map[string]bool{"Set": true, "Put": true, "Store": true}
	// rmwDeleters are the unconditional removals: the session and token
	// stores' Delete and the cache backend's Forget.
	rmwDeleters = map[string]bool{"Delete": true, "Forget": true, "ForgetCtx": true}
)

// The reader and the writer of an interface type named blacklistStoreType.
const (
	blacklistStoreType = "BlacklistStore"
	blacklistReader    = "IsBlacklisted"
	blacklistWriter    = "Add"
)

const rmwMarkerPrefix = "//store-rmw-ok:"

var rmwMarkerRE = regexp.MustCompile(`//store-rmw-ok:\s*\S.{4,}`)

// rmwCall is a reader or writer call on an interface-typed store.
type rmwCall struct {
	pos    token.Pos
	recv   string // the receiver expression
	key    string // the first string argument, "" when none
	method string
}

// storeRMW reports the read-then-write pairs and the unconditional deletes
// of the packages in rmwScope, and the stale //store-rmw-ok: markers there.
func (a *analysis) storeRMW() {
	used := map[string]map[int]bool{}
	for _, u := range a.units {
		if !u.report || !rmwScope[strings.TrimPrefix(strings.TrimPrefix(u.path, u.module), "/")] {
			continue
		}
		for _, f := range u.files {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				var reads, writes, deletes []rmwCall
				collectRMW(u, fd.Body, false, &reads, &writes, &deletes)
				for _, w := range writes {
					for _, r := range reads {
						if r.pos < w.pos && r.recv == w.recv && r.key == w.key {
							a.reportRMW(w, fmt.Sprintf("%s.%s after %s.%s (line %d) on the same key", w.recv, w.method, r.recv, r.method, a.fset.Position(r.pos).Line), used)
							break
						}
					}
				}
				for _, d := range deletes {
					a.reportRMW(d, fmt.Sprintf("%s.%s removes the record whatever it holds now, outside a compare-and-delete", d.recv, d.method), used)
				}
			}
			for _, cg := range f.Comments {
				for _, c := range cg.List {
					if !strings.HasPrefix(c.Text, rmwMarkerPrefix) {
						continue
					}
					p := a.fset.Position(c.Pos())
					if !used[p.Filename][p.Line] {
						a.hits[fmt.Sprintf("%s:%d: stale: the //store-rmw-ok: marker suppresses no store read-then-write and no store delete", a.rel(p.Filename), p.Line)] = true
					}
				}
			}
		}
	}
}

// reportRMW reports the write or delete c with what (the text after the
// kind), unless its line carries a marker with a rationale.
func (a *analysis) reportRMW(c rmwCall, what string, used map[string]map[int]bool) {
	p := a.fset.Position(c.pos)
	text := a.line(p.Filename, p.Line)
	if used[p.Filename] == nil {
		used[p.Filename] = map[int]bool{}
	}
	if rmwMarkerRE.MatchString(text) {
		used[p.Filename][p.Line] = true
		return
	}
	line := fmt.Sprintf("%s:%d: %s: %s", a.rel(p.Filename), p.Line, kindRMW, what)
	if strings.Contains(text, rmwMarkerPrefix) {
		used[p.Filename][p.Line] = true
		line += " (the //store-rmw-ok: marker here has no rationale of at least 5 characters, so it does not suppress)"
	}
	a.hits[line] = true
}

// collectRMW appends the store reader, writer and delete calls in n to
// reads, writes and deletes. A writer or delete inside a literal handed to
// a buildonce Group's Do runs under the per-key flight and is left out
// (flight).
func collectRMW(u *unit, n ast.Node, flight bool, reads, writes, deletes *[]rmwCall) {
	ast.Inspect(n, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isFlightDo(u, call) {
			collectRMW(u, call.Fun, flight, reads, writes, deletes)
			for _, arg := range call.Args {
				if lit, ok := ast.Unparen(arg).(*ast.FuncLit); ok {
					collectRMW(u, lit.Body, true, reads, writes, deletes)
				} else {
					collectRMW(u, arg, flight, reads, writes, deletes)
				}
			}
			return false
		}
		sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
		if !ok {
			return true
		}
		s := u.info.Selections[sel]
		if s == nil || s.Kind() != types.MethodVal || !types.IsInterface(s.Recv()) {
			return true
		}
		name := sel.Sel.Name
		blacklist := isBlacklistStore(s.Recv())
		c := rmwCall{pos: call.Pos(), recv: types.ExprString(sel.X), key: firstStringArg(u, call), method: name}
		switch {
		case rmwReaders[name], blacklist && name == blacklistReader:
			*reads = append(*reads, c)
		case (rmwWriters[name] || blacklist && name == blacklistWriter) && !flight:
			*writes = append(*writes, c)
		case rmwDeleters[name] && !flight:
			*deletes = append(*deletes, c)
		}
		return true
	})
}

// isBlacklistStore reports whether t is an interface type named
// BlacklistStore, in any package.
func isBlacklistStore(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Name() == blacklistStoreType
}

// isFlightDo reports whether call is the Do method of an internal/buildonce
// Group or Serial.
func isFlightDo(u *unit, call *ast.CallExpr) bool {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Do" {
		return false
	}
	s := u.info.Selections[sel]
	if s == nil || s.Kind() != types.MethodVal {
		return false
	}
	t := s.Recv()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	// Group is the per-key flight; Serial runs the transitions of one
	// value one at a time. Both hold no lock while the function runs.
	if name := named.Obj().Name(); name != "Group" && name != "Serial" {
		return false
	}
	return named.Obj().Pkg().Path() == u.module+"/internal/buildonce"
}

// firstStringArg returns the first argument of call whose type is a
// string, as written, or "".
func firstStringArg(u *unit, call *ast.CallExpr) string {
	for _, arg := range call.Args {
		tv, ok := u.info.Types[arg]
		if !ok || tv.Type == nil {
			continue
		}
		if b, ok := tv.Type.Underlying().(*types.Basic); ok && b.Info()&types.IsString != 0 {
			return types.ExprString(arg)
		}
	}
	return ""
}
