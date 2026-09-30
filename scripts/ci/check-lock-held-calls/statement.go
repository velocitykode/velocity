package main

import (
	"fmt"
	"go/ast"
	"go/types"
)

// Statement calls: a pool opened by the ORM's drivers package is
// instrumented, and every statement run through it calls the pool's
// statement observer and query logger (user code) from inside the
// database/sql call, before the call returns. So a database/sql call that
// runs a statement, or closes a result set (which reports its statement),
// is user code under a lock, whatever context it is given, unless the
// context holds the observation for later: one built by the module's
// internal/ownctx Hold or HoldDetached, whose Held runs the observer and
// logger once the lock is released. A context derived from such a
// context does not hold: the pool recognises only the context Hold built
// (ownctx.HeldBy runs no method of a context). A Row or Rows returned by a query given a
// holding context holds too, so its Scan, Next and Close are not
// reported. A context parameter of an unexported function holds when the
// function is only called, never assigned the parameter, and every call
// site in the package passes a holding context (as closed func
// parameters, see the package comment).
//
// The observer and the driver behind the pool are reached through
// interfaces the checker can name by type: an interface method called on
// a value whose type is an interface declared in database/sql/driver, or
// is the module's orm/drivers.StatementObserver, is reported under a lock
// too, context or not.

// sqlStatements are the database/sql methods that run a statement or close
// a result set.
var sqlStatements = map[string]bool{}

// sqlCarrierResults are the database/sql methods whose result set, given a
// holding context, holds its later Scan, Next and Close.
var sqlCarrierResults = map[string]bool{}

func init() {
	for _, recv := range []string{"DB", "Conn", "Tx", "Stmt"} {
		for _, m := range []string{"Exec", "ExecContext", "Query", "QueryContext", "QueryRow", "QueryRowContext", "Prepare", "PrepareContext"} {
			sqlStatements["database/sql."+recv+"."+m] = true
		}
		sqlCarrierResults["database/sql."+recv+".QueryContext"] = true
		sqlCarrierResults["database/sql."+recv+".QueryRowContext"] = true
	}
	for _, k := range []string{"Row.Scan", "Rows.Next", "Rows.NextResultSet", "Rows.Close"} {
		sqlStatements["database/sql."+k] = true
	}
}

// ownctxHolders are the internal/ownctx builders whose context holds
// statement observations until the lock is released.
var ownctxHolders = map[string]bool{"Hold": true, "HoldDetached": true}

// statementCall reports why call runs the statement observer inline under
// a lock, "" when it is not a statement call or its observation is held.
func (u *unit) statementCall(call *ast.CallExpr) string {
	fn := staticCallee(u, call)
	if fn == nil || !sqlStatements[funcKey(fn)] {
		return ""
	}
	const why = "(runs a statement: an instrumented pool calls its statement observer and query logger inside it; give it an ownctx.Hold context and release the Held after unlocking)"
	for _, arg := range call.Args {
		if tv, ok := u.info.Types[arg]; ok && tv.Type != nil && u.isCtx(tv.Type) {
			if u.heldCtx(arg, map[*types.Var]bool{}) {
				return ""
			}
			return why
		}
	}
	// No context argument: a Row or Rows reports its statement with the
	// context of the query that returned it.
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok && u.heldResult(sel.X) {
		return ""
	}
	return why
}

// heldResult reports whether e is a result set returned by a query given a
// holding context: the call itself, or a local variable every value
// assigned to which is one.
func (u *unit) heldResult(e ast.Expr) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		return u.heldQuery(x)
	case *ast.Ident:
		v, ok := u.info.Uses[x].(*types.Var)
		if !ok {
			return false
		}
		values := u.sqlAssigns[v]
		if len(values) == 0 {
			return false
		}
		for _, val := range values {
			call, ok := ast.Unparen(val).(*ast.CallExpr)
			if !ok || !u.heldQuery(call) {
				return false
			}
		}
		return true
	}
	return false
}

// heldQuery reports whether call is a query, returning a Row or Rows,
// given a holding context.
func (u *unit) heldQuery(call *ast.CallExpr) bool {
	fn := staticCallee(u, call)
	if fn == nil || !sqlCarrierResults[funcKey(fn)] {
		return false
	}
	for _, arg := range call.Args {
		if tv, ok := u.info.Types[arg]; ok && tv.Type != nil && u.isCtx(tv.Type) {
			return u.heldCtx(arg, map[*types.Var]bool{})
		}
	}
	return false
}

// heldCtx reports whether e is a context that holds statement
// observations (see the top of this file). seen guards against assignment
// cycles.
func (u *unit) heldCtx(e ast.Expr, seen map[*types.Var]bool) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		fn := staticCallee(u, x)
		if fn == nil || fn.Pkg() == nil {
			return false
		}
		if fn.Pkg().Path() == u.module+"/internal/ownctx" {
			return ownctxHolders[fn.Name()]
		}
		return false
	case *ast.Ident:
		v, ok := u.info.Uses[x].(*types.Var)
		if !ok || seen[v] || u.ctxAddr[v] {
			return false
		}
		values := u.ctxAssigns[v]
		if u.ctxParams[v] {
			var closed bool
			if values, closed = u.ctxParamArgs[v]; !closed || len(u.ctxAssigns[v]) > 0 {
				return false
			}
		}
		if len(values) == 0 {
			return false
		}
		// seen holds the variables being resolved, not those resolved: a
		// variable reached twice (two call sites passing the same
		// parameter) is resolved twice, and only a cycle stops.
		seen[v] = true
		defer delete(seen, v)
		for _, val := range values {
			if !u.heldCtx(val, seen) {
				return false
			}
		}
		return true
	}
	return false
}

// observerIface reports whether recv is an interface a database driver or
// a statement observer implements: declared in database/sql/driver, or the
// module's orm/drivers.StatementObserver.
func (u *unit) observerIface(recv types.Type) bool {
	n, ok := types.Unalias(recv).(*types.Named)
	if !ok || !types.IsInterface(n) || n.Obj().Pkg() == nil {
		return false
	}
	switch n.Obj().Pkg().Path() {
	case "database/sql/driver":
		return true
	case u.module + "/orm/drivers":
		return n.Obj().Name() == "StatementObserver"
	}
	return false
}

// indexSQLVars records, for u's local variables, every value assigned to
// them from a single call, for heldResult: a variable holding a Row or
// Rows is assigned the query's call.
func (u *unit) indexSQLVars() {
	u.sqlAssigns = map[*types.Var][]ast.Expr{}
	for _, f := range u.files {
		ast.Inspect(f, func(n ast.Node) bool {
			var lhs, rhs []ast.Expr
			switch n := n.(type) {
			case *ast.AssignStmt:
				lhs, rhs = n.Lhs, n.Rhs
			case *ast.ValueSpec:
				for _, name := range n.Names {
					lhs = append(lhs, name)
				}
				rhs = n.Values
			default:
				return true
			}
			for i, l := range lhs {
				id, ok := ast.Unparen(l).(*ast.Ident)
				if !ok || id.Name == "_" {
					continue
				}
				obj := u.info.Defs[id]
				if obj == nil {
					obj = u.info.Uses[id]
				}
				v, ok := obj.(*types.Var)
				if !ok {
					continue
				}
				switch {
				case len(rhs) == len(lhs):
					u.sqlAssigns[v] = append(u.sqlAssigns[v], rhs[i])
				case len(rhs) == 1 && i == 0:
					u.sqlAssigns[v] = append(u.sqlAssigns[v], rhs[0])
				default:
					u.sqlAssigns[v] = append(u.sqlAssigns[v], &ast.BadExpr{})
				}
			}
			return true
		})
	}
}

// indexCtxParamArgs records fd's context parameters that every one of
// calls, its call sites, gives an argument for, with those arguments;
// heldCtx and ownedCtx decide whether they all hold or are all owned. fd
// is unexported and used only by calls.
func (u *unit) indexCtxParamArgs(fd *ast.FuncDecl, sig *types.Signature, calls []*ast.CallExpr) {
	n := sig.Params().Len()
	if sig.Variadic() {
		n-- // the variadic parameter takes the rest of the arguments
	}
	for i := 0; i < n; i++ {
		param := sig.Params().At(i)
		if !u.isCtx(param.Type()) || param.Name() == "" || param.Name() == "_" {
			continue
		}
		args := make([]ast.Expr, 0, len(calls))
		for _, call := range calls {
			if len(call.Args) <= i {
				args = nil
				break
			}
			args = append(args, call.Args[i])
		}
		if len(args) > 0 {
			u.ctxParamArgs[param] = args
		}
	}
}

// unreleasedHolds adds a hit for every ownctx Hold or HoldDetached call in
// a reported package whose Held is not released by a `defer h.Release()`
// in the same function: a Release reached only on the straight path is
// skipped by a panic or an early return, and the statement reports the
// Held keeps are never delivered. A deferred Release registered before
// the lock's deferred unlock (or deferred at all, when the lock is taken
// and released inside a func literal or a helper) runs after the lock is
// released.
func (a *analysis) unreleasedHolds() {
	for _, u := range a.units {
		if !u.report {
			continue
		}
		for _, f := range u.files {
			ast.Inspect(f, func(n ast.Node) bool {
				var body *ast.BlockStmt
				switch fn := n.(type) {
				case *ast.FuncDecl:
					body = fn.Body
				case *ast.FuncLit:
					body = fn.Body
				}
				if body != nil {
					a.checkHolds(u, body)
				}
				return true
			})
		}
	}
}

// checkHolds reports the Hold calls of one function body (not of the func
// literals in it, which are functions of their own) whose Held no
// deferred Release in the body releases.
func (a *analysis) checkHolds(u *unit, body *ast.BlockStmt) {
	type hold struct {
		call *ast.CallExpr
		held types.Object // nil when the Held is discarded or not assigned
	}
	var holds []hold
	released := map[types.Object]bool{}
	isHold := func(e ast.Expr) (*ast.CallExpr, int) {
		call, ok := ast.Unparen(e).(*ast.CallExpr)
		if !ok {
			return nil, 0
		}
		fn := staticCallee(u, call)
		if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != u.module+"/internal/ownctx" || !ownctxHolders[fn.Name()] {
			return nil, 0
		}
		return call, fn.Type().(*types.Signature).Results().Len() - 1
	}
	object := func(e ast.Expr) types.Object {
		id, ok := ast.Unparen(e).(*ast.Ident)
		if !ok || id.Name == "_" {
			return nil
		}
		if obj := u.info.Defs[id]; obj != nil {
			return obj
		}
		return u.info.Uses[id]
	}
	seen := map[*ast.CallExpr]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			if len(n.Rhs) == 1 {
				if call, idx := isHold(n.Rhs[0]); call != nil {
					seen[call] = true
					var obj types.Object
					if idx < len(n.Lhs) {
						obj = object(n.Lhs[idx])
					}
					holds = append(holds, hold{call, obj})
				}
			}
		case *ast.ValueSpec:
			if len(n.Values) == 1 {
				if call, idx := isHold(n.Values[0]); call != nil {
					seen[call] = true
					var obj types.Object
					if idx < len(n.Names) {
						obj = object(n.Names[idx])
					}
					holds = append(holds, hold{call, obj})
				}
			}
		case *ast.DeferStmt:
			if sel, ok := ast.Unparen(n.Call.Fun).(*ast.SelectorExpr); ok && sel.Sel.Name == "Release" && len(n.Call.Args) == 0 {
				if obj := object(sel.X); obj != nil {
					released[obj] = true
				}
			}
		case *ast.CallExpr:
			if call, _ := isHold(n); call != nil && !seen[call] {
				holds = append(holds, hold{call: call})
			}
		}
		return true
	})
	for _, h := range holds {
		if h.held != nil && released[h.held] {
			continue
		}
		p := a.fset.Position(h.call.Pos())
		a.hits[fmt.Sprintf("%s:%d: %s: %s's Held is not released by a defer in this function; a panic or early return would keep its statement reports", a.rel(p.Filename), p.Line, kindHold, types.ExprString(h.call.Fun))] = true
	}
}
