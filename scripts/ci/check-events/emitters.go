package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

// emitters applies the emitter rule to u.
func (c *checker) emitters(u *unit) {
	e := &emitterScan{c: c, u: u, shared: map[types.Object]bool{}, defs: map[types.Object]ast.Expr{}, writes: map[types.Object]int{}}
	for _, f := range u.files {
		ast.Inspect(f, e.collect)
	}
	for _, call := range e.fails {
		sel := call.Fun.(*ast.SelectorExpr)
		obj := e.object(sel.X)
		if obj != nil && e.shared[obj] {
			continue
		}
		if len(call.Args) > 1 && e.ownDispatch(call.Args[1], sel.X) {
			continue
		}
		c.report(call.Pos(), ruleEmitter, fmt.Sprintf("%s.%s records a failure the emitter originates into Failures the app never shares with it: no Share or SetShared on %s in package %s", exprString(sel.X), sel.Sel.Name, exprString(sel.X), u.pkg.Name()))
	}
}

// emitterScan is the state of one package's scan.
type emitterScan struct {
	c *checker
	u *unit
	// shared holds the emitters (a struct field or a variable) some Share
	// or SetShared call in the package names.
	shared map[types.Object]bool
	// fails are the Fail and FailLater calls on an emitter.
	fails []*ast.CallExpr
	// defs holds the value a local variable was declared with (x := v,
	// var x = v); writes counts the assignments to each variable.
	defs   map[types.Object]ast.Expr
	writes map[types.Object]int
}

func (e *emitterScan) collect(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.CallExpr:
		sel, ok := n.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch e.emitterMethod(sel) {
		case "Share", "SetShared":
			// The Failures argument: Share(f), SetShared(fn, f, logger). A
			// literal nil hands the emitter back to its own Failures, so it
			// shares nothing.
			i := 0
			if sel.Sel.Name == "SetShared" {
				i = 1
			}
			if obj := e.object(sel.X); obj != nil && len(n.Args) > i && !isNil(n.Args[i]) {
				e.shared[obj] = true
			}
		case "Fail", "FailLater":
			e.fails = append(e.fails, n)
		}
	case *ast.AssignStmt:
		for i, lhs := range n.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok {
				continue
			}
			obj := e.u.info.Defs[id]
			if obj == nil {
				obj = e.u.info.Uses[id]
			}
			if obj == nil {
				continue
			}
			e.writes[obj]++
			if n.Tok == token.DEFINE && len(n.Lhs) == len(n.Rhs) {
				e.defs[obj] = n.Rhs[i]
			}
		}
	case *ast.ValueSpec:
		for i, id := range n.Names {
			obj := e.u.info.Defs[id]
			if obj == nil {
				continue
			}
			if len(n.Values) == len(n.Names) {
				e.writes[obj]++
				e.defs[obj] = n.Values[i]
			}
		}
	}
	return true
}

// emitterMethod returns the name of the eventemit.Emitter method sel
// selects, or "".
func (e *emitterScan) emitterMethod(sel *ast.SelectorExpr) string {
	s := e.u.info.Selections[sel]
	if s == nil || s.Kind() != types.MethodVal {
		return ""
	}
	recv := s.Recv()
	if p, ok := recv.(*types.Pointer); ok {
		recv = p.Elem()
	}
	n, ok := types.Unalias(recv).(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != e.c.module+"/internal/eventemit" || n.Obj().Name() != "Emitter" {
		return ""
	}
	return sel.Sel.Name
}

// object returns the emitter x names: the struct field of a selector, or
// the variable of an identifier (through parentheses, & and *), or nil.
func (e *emitterScan) object(x ast.Expr) types.Object {
	for {
		switch v := x.(type) {
		case *ast.ParenExpr:
			x = v.X
			continue
		case *ast.StarExpr:
			x = v.X
			continue
		case *ast.UnaryExpr:
			if v.Op == token.AND {
				x = v.X
				continue
			}
			return nil
		case *ast.Ident:
			return e.u.info.Uses[v]
		case *ast.SelectorExpr:
			if s := e.u.info.Selections[v]; s != nil && s.Kind() == types.FieldVal {
				return s.Obj()
			}
			return e.u.info.Uses[v.Sel]
		}
		return nil
	}
}

// ownDispatch reports whether err is the result of
// eventemit.DispatchContained running the Dispatcher of the emitter recv
// names (the same field or variable, reached through the same expression,
// so another value's emitter does not count), called in place or read
// into a variable declared with it and never assigned again.
func (e *emitterScan) ownDispatch(err ast.Expr, recv ast.Expr) bool {
	obj := e.object(recv)
	if obj == nil {
		return false
	}
	call, ok := e.value(err).(*ast.CallExpr)
	if !ok || !e.isDispatchContained(call) || len(call.Args) < 2 {
		return false
	}
	d, ok := e.value(call.Args[1]).(*ast.CallExpr)
	if !ok || len(d.Args) != 0 {
		return false
	}
	sel, ok := d.Fun.(*ast.SelectorExpr)
	return ok && e.emitterMethod(sel) == "Dispatcher" && e.object(sel.X) == obj && sameEmitter(sel.X, recv)
}

// value returns x, or the value the variable x names was declared with
// when it is assigned nowhere else.
func (e *emitterScan) value(x ast.Expr) ast.Expr {
	id, ok := x.(*ast.Ident)
	if !ok {
		return x
	}
	obj := e.u.info.Uses[id]
	if obj == nil || e.writes[obj] != 1 {
		return x
	}
	if v, ok := e.defs[obj]; ok {
		return v
	}
	return x
}

// isDispatchContained reports whether call calls
// eventemit.DispatchContained.
func (e *emitterScan) isDispatchContained(call *ast.CallExpr) bool {
	var id *ast.Ident
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		id = f.Sel
	case *ast.Ident:
		id = f
	default:
		return false
	}
	fn, ok := e.u.info.Uses[id].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == e.c.module+"/internal/eventemit" && fn.Name() == "DispatchContained"
}

// isNil reports whether x is the literal nil.
func isNil(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == "nil"
}

// exprString prints a short form of x for a message.
func exprString(x ast.Expr) string {
	switch v := x.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.ParenExpr:
		return exprString(v.X)
	case *ast.StarExpr:
		return "*" + exprString(v.X)
	case *ast.UnaryExpr:
		return v.Op.String() + exprString(v.X)
	case *ast.CallExpr:
		return exprString(v.Fun) + "()"
	}
	return "emitter"
}

// sameEmitter reports whether a and b spell the same emitter, ignoring
// parentheses, & and *.
func sameEmitter(a, b ast.Expr) bool {
	return exprString(strip(a)) == exprString(strip(b))
}

func strip(x ast.Expr) ast.Expr {
	for {
		switch v := x.(type) {
		case *ast.ParenExpr:
			x = v.X
		case *ast.StarExpr:
			x = v.X
		case *ast.UnaryExpr:
			if v.Op != token.AND {
				return x
			}
			x = v.X
		default:
			return x
		}
	}
}
