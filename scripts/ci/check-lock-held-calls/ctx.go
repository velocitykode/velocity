package main

import (
	"go/ast"
	"go/token"
	"go/types"
)

// Context calls: a context's Done, Err, Value and Deadline are methods of
// whatever value the caller passed, so on a context the caller supplied
// they are user code. The framework calls them itself (ctx.Err()), and so
// does any code outside the module it hands the context to: database/sql
// (BeginTx, ExecContext, QueryContext, Tx.Commit), net, the context
// package's own With* functions (which call the parent's Done and
// Deadline) and the rest.
//
// A context is framework-owned, and its methods fixed code, when it is
// built from context.Background or context.TODO, or by the module's
// internal/ownctx (Bridge, Detached: read before the lock, framework code
// after), through the context package's With* functions: a local variable
// counts when every value assigned to it is such an expression. context.WithoutCancel and the
// other With* functions of a caller's context still reach the caller's
// Value, so they are not owned.

// A stdlib value that keeps the context it was built with calls it later
// from its own methods: a *sql.Tx begun with a caller's context calls its
// Done in Commit and its Value and Done in Rollback (through the cancel
// of the context it derived). A local variable assigned such a value is a
// carrier, and a call that uses it (as the receiver or an argument) under
// a lock is reported like a call given the context itself.
//
// ctxKeepers are the calls whose first result keeps the context argument,
// each with whether the call itself calls the context too (BeginTx waits
// for a connection on its Done; building a request only stores it).
var ctxKeepers = map[string]bool{
	"database/sql.DB.BeginTx":        true,
	"database/sql.Conn.BeginTx":      true,
	"net/http.NewRequestWithContext": false,
	"net/http.Request.WithContext":   false,
	"net/http.Request.Clone":         false,
}

// ctxMethods are the methods of context.Context.
var ctxMethods = []string{"Deadline", "Done", "Err", "Value"}

// ctxRoots build a context from nothing; ctxDerive build one from their
// first argument.
var (
	ctxRoots = map[string]bool{"Background": true, "TODO": true}
	// ownctxRoots are the module's internal/ownctx builders.
	ownctxRoots = map[string]bool{"Bridge": true, "Detached": true}
	// ctxWrap only store their parent: building one calls none of its
	// methods (the result still reaches them, so it is not owned).
	ctxWrap   = map[string]bool{"WithoutCancel": true, "WithValue": true}
	ctxDerive = map[string]bool{
		"WithCancel": true, "WithCancelCause": true, "WithDeadline": true, "WithDeadlineCause": true,
		"WithTimeout": true, "WithTimeoutCause": true, "WithValue": true, "WithoutCancel": true,
	}
)

// isCtx reports whether t is an interface type with the methods of
// context.Context: its value can be any code.
func (u *unit) isCtx(t types.Type) bool {
	return u.ctxIface != nil && types.IsInterface(t) && types.Implements(t, u.ctxIface)
}

// ctxMethodCall reports whether calling method name on recv is a call of
// a context.Context method on an interface value.
func (u *unit) ctxMethodCall(recv types.Type, name string) bool {
	if !u.isCtx(recv) {
		return false
	}
	for _, m := range ctxMethods {
		if m == name {
			return true
		}
	}
	return false
}

// takesCtx reports whether call passes a context of interface type. An
// interface method taking one is a pluggable operation (a store, a
// driver, a lock backend): its implementation is any code whatever the
// context, so such a call under a lock is reported even with an owned
// context.
func (u *unit) takesCtx(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		if tv, ok := u.info.Types[arg]; ok && tv.Type != nil && u.isCtx(tv.Type) {
			return true
		}
	}
	return false
}

// ctxResult is what a call outside the module does with the contexts it
// is given: open names the argument that can be any code ("" when none
// can), methods the module methods of a concrete context it calls.
type ctxResult struct {
	open    string
	methods []*types.Func
}

// ctxCall classifies a call to a function outside the module that is
// given a context: a context of interface type that is not owned is
// user code, a concrete module context's methods are followed as reach.
func (u *unit) ctxCall(call *ast.CallExpr) ctxResult {
	var r ctxResult
	if u.ctxIface == nil {
		return r
	}
	fn := staticCallee(u, call)
	if fn == nil || fn.Pkg() == nil || inModule(u.module, fn.Pkg().Path()) {
		return r
	}
	if fn.Pkg().Path() == "context" && (ctxRoots[fn.Name()] || ctxWrap[fn.Name()]) {
		return r
	}
	if calls, keeps := ctxKeepers[funcKey(fn)]; keeps && !calls {
		// Building the value calls nothing; using it does (carriers).
		return r
	}
	var operands []ast.Expr
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok {
		if s, ok := u.info.Selections[sel]; ok && s.Kind() == types.MethodVal {
			operands = append(operands, sel.X)
		}
	}
	for _, e := range append(operands, call.Args...) {
		if id, ok := ast.Unparen(e).(*ast.Ident); ok {
			if v, ok := u.info.Uses[id].(*types.Var); ok && u.ctxCarriers[v] != "" {
				r.open = "(" + id.Name + " keeps the caller's context from " + u.ctxCarriers[v] + ", and calls its Done or Value)"
				return r
			}
		}
	}
	for _, arg := range call.Args {
		tv, ok := u.info.Types[arg]
		if !ok || tv.Type == nil {
			continue
		}
		if u.isCtx(tv.Type) {
			if r.open == "" && !u.ownedCtx(arg, map[*types.Var]bool{}) {
				r.open = "(the context " + types.ExprString(arg) + " is the caller's: its Done, Err and Value can be any code)"
			}
			continue
		}
		if types.IsInterface(tv.Type) || !types.Implements(tv.Type, u.ctxIface) && !types.Implements(types.NewPointer(tv.Type), u.ctxIface) {
			continue
		}
		for _, name := range ctxMethods {
			obj, _, _ := types.LookupFieldOrMethod(tv.Type, true, nil, name)
			if m, ok := obj.(*types.Func); ok && m.Pkg() != nil && inModule(u.module, m.Pkg().Path()) {
				r.methods = append(r.methods, m)
			}
		}
	}
	return r
}

// ownedCtx reports whether e is a framework-owned context (see the top of
// this file). seen guards against assignment cycles.
func (u *unit) ownedCtx(e ast.Expr, seen map[*types.Var]bool) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		fn := staticCallee(u, x)
		if fn == nil || fn.Pkg() == nil {
			return false
		}
		if fn.Pkg().Path() == u.module+"/internal/ownctx" {
			return ownctxRoots[fn.Name()]
		}
		if fn.Pkg().Path() != "context" {
			return false
		}
		if ctxRoots[fn.Name()] {
			return true
		}
		return ctxDerive[fn.Name()] && len(x.Args) > 0 && u.ownedCtx(x.Args[0], seen)
	case *ast.Ident:
		v, ok := u.info.Uses[x].(*types.Var)
		if !ok || seen[v] || u.ctxParams[v] || u.ctxAddr[v] {
			return false
		}
		values := u.ctxAssigns[v]
		if len(values) == 0 {
			return false
		}
		seen[v] = true
		for _, val := range values {
			if !u.ownedCtx(val, seen) {
				return false
			}
		}
		return true
	}
	return false
}

// indexCtxVars records, for u's local variables of context type, every
// value assigned to them (for a tuple assignment the call, whose first
// result is the context for every With* function), the ones that are
// parameters, and the ones whose address is taken.
func (u *unit) indexCtxVars() {
	u.ctxAssigns = map[*types.Var][]ast.Expr{}
	u.ctxParams = map[*types.Var]bool{}
	u.ctxAddr = map[*types.Var]bool{}
	u.ctxCarriers = map[*types.Var]string{}
	if u.ctxIface == nil {
		return
	}
	defer u.indexCarriers()
	local := func(id *ast.Ident) *types.Var {
		obj := u.info.Defs[id]
		if obj == nil {
			obj = u.info.Uses[id]
		}
		v, ok := obj.(*types.Var)
		if !ok || v.IsField() || v.Pkg() == nil || v.Parent() == v.Pkg().Scope() || !u.isCtx(v.Type()) {
			return nil
		}
		return v
	}
	assign := func(lhs []ast.Expr, rhs []ast.Expr) {
		for i, l := range lhs {
			id, ok := ast.Unparen(l).(*ast.Ident)
			if !ok {
				continue
			}
			v := local(id)
			if v == nil {
				continue
			}
			switch {
			case len(rhs) == len(lhs):
				u.ctxAssigns[v] = append(u.ctxAssigns[v], rhs[i])
			case len(rhs) == 1 && i == 0:
				u.ctxAssigns[v] = append(u.ctxAssigns[v], rhs[0])
			default:
				// A later result of a tuple call: not a context this
				// file can name; an empty expression owns nothing.
				u.ctxAssigns[v] = append(u.ctxAssigns[v], &ast.BadExpr{})
			}
		}
	}
	params := func(ft *ast.FuncType) {
		for _, list := range []*ast.FieldList{ft.Params, ft.Results} {
			if list == nil {
				continue
			}
			for _, f := range list.List {
				for _, n := range f.Names {
					if v := local(n); v != nil {
						u.ctxParams[v] = true
					}
				}
			}
		}
	}
	for _, f := range u.files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				params(n.Type)
				if n.Recv != nil {
					for _, r := range n.Recv.List {
						for _, name := range r.Names {
							if v := local(name); v != nil {
								u.ctxParams[v] = true
							}
						}
					}
				}
			case *ast.FuncLit:
				params(n.Type)
			case *ast.AssignStmt:
				assign(n.Lhs, n.Rhs)
			case *ast.ValueSpec:
				lhs := make([]ast.Expr, len(n.Names))
				for i, name := range n.Names {
					lhs[i] = name
				}
				assign(lhs, n.Values)
			case *ast.RangeStmt:
				for _, e := range []ast.Expr{n.Key, n.Value} {
					if id, ok := e.(*ast.Ident); ok {
						if v := local(id); v != nil {
							u.ctxAssigns[v] = append(u.ctxAssigns[v], &ast.BadExpr{})
						}
					}
				}
			case *ast.UnaryExpr:
				if n.Op == token.AND {
					if id, ok := ast.Unparen(n.X).(*ast.Ident); ok {
						if v := local(id); v != nil {
							u.ctxAddr[v] = true
						}
					}
				}
			}
			return true
		})
	}
}

// indexCarriers records the local variables assigned a value built by a
// ctxKeepers call from a context that is not owned.
func (u *unit) indexCarriers() {
	mark := func(lhs ast.Expr, rhs ast.Expr) {
		id, ok := ast.Unparen(lhs).(*ast.Ident)
		if !ok || id.Name == "_" {
			return
		}
		call, ok := ast.Unparen(rhs).(*ast.CallExpr)
		if !ok {
			return
		}
		fn := staticCallee(u, call)
		if fn == nil {
			return
		}
		if _, keeps := ctxKeepers[funcKey(fn)]; !keeps {
			return
		}
		carries := false
		for _, arg := range call.Args {
			if tv, ok := u.info.Types[arg]; ok && tv.Type != nil && u.isCtx(tv.Type) && !u.ownedCtx(arg, map[*types.Var]bool{}) {
				carries = true
			}
		}
		if !carries {
			return
		}
		obj := u.info.Defs[id]
		if obj == nil {
			obj = u.info.Uses[id]
		}
		if v, ok := obj.(*types.Var); ok {
			u.ctxCarriers[v] = types.ExprString(call.Fun)
		}
	}
	for _, f := range u.files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				if len(n.Rhs) == 1 && len(n.Lhs) >= 1 {
					mark(n.Lhs[0], n.Rhs[0])
				}
			case *ast.ValueSpec:
				if len(n.Values) == 1 && len(n.Names) >= 1 {
					mark(n.Names[0], n.Values[0])
				}
			}
			return true
		})
	}
}
