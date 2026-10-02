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
// (the per-key flight: one read-then-write per key at a time), and a
// compare-and-set method (any other name, LoadOrStore, UpdateShared, UpdateData).
//
// Suppression: a same-line `//store-rmw-ok: <rationale>` comment on the
// write, the rationale at least 5 characters, saying why the pair is
// idempotent or why no two requests share the key. A marker that
// suppresses nothing is stale and is reported.

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

// storeRMW reports the read-then-write pairs of the packages in rmwScope,
// and the stale //store-rmw-ok: markers there.
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
				var reads, writes []rmwCall
				collectRMW(u, fd.Body, false, &reads, &writes)
				for _, w := range writes {
					for _, r := range reads {
						if r.pos < w.pos && r.recv == w.recv && r.key == w.key {
							a.reportRMW(w, r, used)
							break
						}
					}
				}
			}
			for _, cg := range f.Comments {
				for _, c := range cg.List {
					if !strings.HasPrefix(c.Text, rmwMarkerPrefix) {
						continue
					}
					p := a.fset.Position(c.Pos())
					if !used[p.Filename][p.Line] {
						a.hits[fmt.Sprintf("%s:%d: stale: the //store-rmw-ok: marker suppresses no store read-then-write", a.rel(p.Filename), p.Line)] = true
					}
				}
			}
		}
	}
}

func (a *analysis) reportRMW(w, r rmwCall, used map[string]map[int]bool) {
	p := a.fset.Position(w.pos)
	text := a.line(p.Filename, p.Line)
	if rmwMarkerRE.MatchString(text) {
		if used[p.Filename] == nil {
			used[p.Filename] = map[int]bool{}
		}
		used[p.Filename][p.Line] = true
		return
	}
	line := fmt.Sprintf("%s:%d: %s: %s.%s after %s.%s (line %d) on the same key", a.rel(p.Filename), p.Line, kindRMW, w.recv, w.method, r.recv, r.method, a.fset.Position(r.pos).Line)
	if strings.Contains(text, rmwMarkerPrefix) {
		used[p.Filename] = map[int]bool{p.Line: true}
		line += " (the //store-rmw-ok: marker here has no rationale of at least 5 characters, so it does not suppress)"
	}
	a.hits[line] = true
}

// collectRMW appends the store reader and writer calls in n to reads and
// writes. A writer inside a literal handed to a buildonce Group's Do runs
// under the per-key flight and is left out (flight).
func collectRMW(u *unit, n ast.Node, flight bool, reads, writes *[]rmwCall) {
	ast.Inspect(n, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isFlightDo(u, call) {
			collectRMW(u, call.Fun, flight, reads, writes)
			for _, arg := range call.Args {
				if lit, ok := ast.Unparen(arg).(*ast.FuncLit); ok {
					collectRMW(u, lit.Body, true, reads, writes)
				} else {
					collectRMW(u, arg, flight, reads, writes)
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
		c := rmwCall{pos: call.Pos(), recv: types.ExprString(sel.X), key: firstStringArg(u, call), method: name}
		switch {
		case rmwReaders[name]:
			*reads = append(*reads, c)
		case rmwWriters[name] && !flight:
			*writes = append(*writes, c)
		}
		return true
	})
}

// isFlightDo reports whether call is the Do method of an internal/buildonce
// Group.
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
	if !ok || named.Obj().Name() != "Group" || named.Obj().Pkg() == nil {
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
