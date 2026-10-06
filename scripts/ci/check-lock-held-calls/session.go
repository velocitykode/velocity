package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"regexp"
	"strconv"
	"strings"
)

// Two rules about a server session, neither a call under a lock.
//
// The mark rule: a use of a session's IsModified (a call, a method value, or
// a method expression handed its receiver) in the packages that keep
// per-session state (rmwScope), outside the session driver package, whose
// stores own the question. A save in flight has cleared the modified mark
// before its store call and puts it back when the write fails, so a look at
// the mark from outside the session's own save can read "unchanged" for a
// session that is then left unsaved. Whether a session is saved is decided
// inside its Save, which the caller simply calls. A read that decides
// something else, and is right whatever a save in flight does to the mark,
// carries a same-line `//session-mark-ok: <rationale>` comment, the
// rationale at least 5 characters; a marker that suppresses nothing is
// stale and is reported.
//
// The session-id rule, in every package: a session id handed to a log call
// or built into an error text. The id is the session's bearer credential;
// logs and error texts reach more readers than the cookie does. Flagged:
//
//   - in a log call (a method of contract.Logger, the fallback logger
//     included, called on a value or named through its type and handed
//     the logger, With among them since the fields it binds are written with
//     every later line; contract.BindFields, for the same reason; or a
//     function or method named logDebug, logInfo, logWarn or logError): the
//     key "session_id", and a session-id value;
//   - in an Errorf call (fmt or internal/errchain): a session-id value.
//
// A session-id value is recognised by where it comes from or what it is
// called: the ID() of a value that is a session (it also has Regenerate and
// Invalidate; the method expression handed that value counts too), the ID
// field of a session record (a struct type named StoredSession, SessionMeta
// or cacheRecord), a string any of these is joined into with +, a string
// variable assigned one of those in the same function, and a string
// variable or parameter whose name ends in sessionID or sid (any case). An
// id that reaches the call under another name is not seen. The fix is the
// reference: auth/internal/sessionref.Of(id). There is no marker.

const (
	kindMark      = "mark"
	kindSessionID = "session-id"
)

// markOwner is the package, relative to the module, whose session stores
// decide on the modified mark inside their own save.
const markOwner = "auth/drivers/session"

const markMarkerPrefix = "//session-mark-ok:"

var markMarkerRE = regexp.MustCompile(`//session-mark-ok:\s*\S.{4,}`)

// sessionIDNameRE matches the names a variable holding a session id goes
// by: sessionID, oldSessionID, sessID, sid.
var sessionIDNameRE = regexp.MustCompile(`(?i)(sess(ion)?_?id|^sid)$`)

// logWrappers are the names the module's components give their own log
// helpers, which forward their arguments to a logger.
var logWrappers = map[string]bool{"logDebug": true, "logInfo": true, "logWarn": true, "logError": true}

// sessionRecordTypes are the struct types whose ID field is a session id.
var sessionRecordTypes = map[string]bool{"StoredSession": true, "SessionMeta": true, "cacheRecord": true}

// sessionRules reports the mark reads and the logged session ids, and the
// stale //session-mark-ok: markers.
func (a *analysis) sessionRules() {
	used := map[string]map[int]bool{}
	for _, u := range a.units {
		if !u.report {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(u.path, u.module), "/")
		marks := rmwScope[rel] && rel != markOwner
		for _, f := range u.files {
			// Every declaration: a function, and a package variable holding
			// a func literal (a seam tests replace).
			for _, d := range f.Decls {
				ids := sessionIDVars(u, d)
				ast.Inspect(d, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok && marks && isMarkRead(u, sel) {
						a.reportMark(sel.Pos(), types.ExprString(sel), used)
					}
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if what := loggedSessionID(u, call, ids); what != "" {
						p := a.fset.Position(call.Pos())
						a.hits[fmt.Sprintf("%s:%d: %s: %s", a.rel(p.Filename), p.Line, kindSessionID, what)] = true
					}
					return true
				})
			}
			for _, cg := range f.Comments {
				for _, c := range cg.List {
					if !strings.HasPrefix(c.Text, markMarkerPrefix) {
						continue
					}
					p := a.fset.Position(c.Pos())
					if !used[p.Filename][p.Line] {
						a.hits[fmt.Sprintf("%s:%d: stale: the //session-mark-ok: marker suppresses no read of a session's modified mark", a.rel(p.Filename), p.Line)] = true
					}
				}
			}
		}
	}
}

// isMarkRead reports whether sel selects an IsModified method: called on a
// value (m.IsModified()), taken as a method value (f := m.IsModified) or as
// a method expression that is handed its receiver (modified.IsModified(m)).
// Each reads the mark, or hands out the means to.
func isMarkRead(u *unit, sel *ast.SelectorExpr) bool {
	if sel.Sel.Name != "IsModified" {
		return false
	}
	s := u.info.Selections[sel]
	return s != nil && (s.Kind() == types.MethodVal || s.Kind() == types.MethodExpr)
}

// reportMark reports the mark read at pos, unless its line carries a marker
// with a rationale.
func (a *analysis) reportMark(pos token.Pos, what string, used map[string]map[int]bool) {
	p := a.fset.Position(pos)
	text := a.line(p.Filename, p.Line)
	if used[p.Filename] == nil {
		used[p.Filename] = map[int]bool{}
	}
	if markMarkerRE.MatchString(text) {
		used[p.Filename][p.Line] = true
		return
	}
	line := fmt.Sprintf("%s:%d: %s: %s read outside the session's own save", a.rel(p.Filename), p.Line, kindMark, what)
	if strings.Contains(text, markMarkerPrefix) {
		used[p.Filename][p.Line] = true
		line += " (the //session-mark-ok: marker here has no rationale of at least 5 characters, so it does not suppress)"
	}
	a.hits[line] = true
}

// loggedSessionID returns what of a session id call hands to a log or builds
// into an error text, "" when nothing.
func loggedSessionID(u *unit, call *ast.CallExpr, ids map[*types.Var]bool) string {
	log, sink := logOrErrorCall(u, call)
	if sink == "" {
		return ""
	}
	for _, arg := range call.Args {
		if lit, ok := ast.Unparen(arg).(*ast.BasicLit); ok && log && lit.Kind == token.STRING {
			if key, err := strconv.Unquote(lit.Value); err == nil && key == "session_id" {
				return fmt.Sprintf("%s is given the key \"session_id\"", sink)
			}
		}
		if isSessionID(u, arg, ids) {
			return fmt.Sprintf("%s is given the session id %s", sink, types.ExprString(arg))
		}
	}
	return ""
}

// logOrErrorCall names call when it is a log call (log true) or an Errorf
// that builds an error text, "" otherwise.
func logOrErrorCall(u *unit, call *ast.CallExpr) (log bool, name string) {
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		if logWrappers[fun.Name] || u.bindsFields(fun) {
			return true, fun.Name
		}
	case *ast.SelectorExpr:
		method := fun.Sel.Name
		// A method called on a value, or named through its type and handed
		// its receiver (contract.Logger.Warn(l, ...)).
		if s := u.info.Selections[fun]; s != nil && (s.Kind() == types.MethodVal || s.Kind() == types.MethodExpr) {
			if logWrappers[method] || u.loggerMethod(s.Recv(), method) {
				return true, types.ExprString(fun)
			}
			return false, ""
		}
		if logWrappers[method] || u.bindsFields(fun.Sel) {
			return true, types.ExprString(fun)
		}
		if method != "Errorf" {
			return false, ""
		}
		if pkg, ok := u.info.Uses[fun.Sel].(*types.Func); ok && pkg.Pkg() != nil {
			if path := pkg.Pkg().Path(); path == "fmt" || path == u.module+"/internal/errchain" {
				return false, types.ExprString(fun)
			}
		}
	}
	return false, ""
}

// bindsFields reports whether id names contract.BindFields, which binds its
// pairs to every line the logger it returns writes.
func (u *unit) bindsFields(id *ast.Ident) bool {
	fn, ok := u.info.Uses[id].(*types.Func)
	return ok && fn.Name() == "BindFields" && fn.Pkg() != nil && fn.Pkg().Path() == u.module+"/contract"
}

// loggerMethod reports whether calling method name on recv writes a log
// line or binds fields to later ones (With): the logger interface, or the
// fallback logger.
func (u *unit) loggerMethod(recv types.Type, name string) bool {
	if u.logger == nil || !hasMethod(u.logger, name) {
		return false
	}
	if u.loggerCall(recv, name) {
		return true
	}
	if u.fallback == nil {
		return false
	}
	if p, ok := recv.(*types.Pointer); ok {
		recv = p.Elem()
	}
	return types.Identical(recv, u.fallback)
}

// sessionIDVars returns the variables of the declaration body assigned a
// session id by where it comes from (isSessionIDSource).
func sessionIDVars(u *unit, body ast.Node) map[*types.Var]bool {
	ids := map[*types.Var]bool{}
	mark := func(lhs ast.Expr, rhs ast.Expr) {
		id, ok := lhs.(*ast.Ident)
		if !ok || !isSessionIDSource(u, rhs) {
			return
		}
		obj := u.info.Defs[id]
		if obj == nil {
			obj = u.info.Uses[id]
		}
		if v, ok := obj.(*types.Var); ok {
			ids[v] = true
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			if len(s.Lhs) == len(s.Rhs) {
				for i := range s.Lhs {
					mark(s.Lhs[i], s.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			if len(s.Names) == len(s.Values) {
				for i := range s.Names {
					mark(s.Names[i], s.Values[i])
				}
			}
		}
		return true
	})
	return ids
}

// isSessionID reports whether e is a session id: one by where it comes
// from, a variable assigned one, or a string variable named like one.
func isSessionID(u *unit, e ast.Expr, ids map[*types.Var]bool) bool {
	e = ast.Unparen(e)
	if isSessionIDSource(u, e) {
		return true
	}
	// A string an id is joined into carries the id.
	if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
		return isSessionID(u, b.X, ids) || isSessionID(u, b.Y, ids)
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	v, ok := u.info.Uses[id].(*types.Var)
	if !ok {
		return false
	}
	if ids[v] {
		return true
	}
	b, ok := v.Type().Underlying().(*types.Basic)
	return ok && b.Info()&types.IsString != 0 && sessionIDNameRE.MatchString(id.Name)
}

// isSessionIDSource reports whether e reads a session id where it lives:
// the ID() of a session, or the ID field of a session record, alone or
// joined into a string with +.
func isSessionIDSource(u *unit, e ast.Expr) bool {
	e = ast.Unparen(e)
	if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
		return isSessionIDSource(u, b.X) || isSessionIDSource(u, b.Y)
	}
	if call, ok := e.(*ast.CallExpr); ok {
		sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "ID" {
			return false
		}
		// The method expression handed its receiver: session.ID(s).
		if s := u.info.Selections[sel]; s != nil && s.Kind() == types.MethodExpr {
			return len(call.Args) == 1 && sessionShaped(s.Recv())
		}
		if len(call.Args) != 0 {
			return false
		}
		tv, ok := u.info.Types[sel.X]
		return ok && tv.Type != nil && sessionShaped(tv.Type)
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "ID" {
		return false
	}
	s := u.info.Selections[sel]
	if s == nil || s.Kind() != types.FieldVal {
		return false
	}
	t := s.Recv()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	return ok && sessionRecordTypes[named.Obj().Name()]
}

// sessionShaped reports whether t has the methods that make a value a
// session: ID, Regenerate and Invalidate.
func sessionShaped(t types.Type) bool {
	has := func(name string) bool {
		obj, _, _ := types.LookupFieldOrMethod(t, true, nil, name)
		_, ok := obj.(*types.Func)
		return ok
	}
	return has("ID") && has("Regenerate") && has("Invalidate")
}
