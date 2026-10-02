package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
)

// Rule "contain": a call into user code held in a callback registry of the
// events or orm packages (a model observer, a listener, a statement
// observer) that no recover contains. A registered callback is user code:
// a panic in it must fail that callback only, as a typed panic error
// (internal/panicerr), and never escape to the framework goroutine or the
// caller that fired it, skipping the rest of the fan-out.
//
// A callback interface is a named interface type of the module, named for
// a callback kind (its name ends in Observer, Listener or Subscriber),
// held by a registry: a struct field of the events or orm package trees whose type
// is a slice, array or map of it (or of a map or slice of it), an
// atomic.Pointer to it, or any of those of a struct with a field of it. A
// callback call is a call of a method of such an interface on a value of
// it, or on a value asserted from it (handler, ok := listener.(X)). A
// method of a type that itself implements the interface (a decorator
// observer wrapping another) is exempt: it is the callback, contained
// where it is called. So is a call, directly in an exported function's
// body, on a parameter of that function (Subscribe(subscriber) calling
// subscriber.Subscribe): the caller handed the value in and the call runs
// on its goroutine before returning, so a panic reaches the code that
// passed it, as if it had made the call itself.
//
// A callback call is contained when the function it is written in is:
//   - one whose body defers a function that calls recover;
//   - an unexported function or method of the package, every call of
//     which (at least one, none in a go statement, never taken as a value)
//     is contained;
//   - a function literal called in place, deferred, or bound to a local
//     variable or passed to a parameter every use of which is a contained
//     call or a pass to another such parameter; a literal run by a go
//     statement is never contained by the function that starts it.
//
// The proof is per package and by name; a call reached through an
// interface or from another package does not contain. There is no
// suppression marker: route the call through the package's containment
// helper (the one function that defers the recover).

const ruleContain = "contain"

// callbackSite is one callback call and whether it is contained.
type callbackSite struct {
	pos       token.Pos
	call      string
	contained bool
}

// inScope reports whether the import path is in the events or orm trees.
func (c *checker) containScope(path string) bool {
	rel := strings.TrimPrefix(strings.TrimPrefix(path, c.module), "/")
	for _, root := range []string{"events", "orm"} {
		if rel == root || strings.HasPrefix(rel, root+"/") {
			return true
		}
	}
	return false
}

// contains applies the contain rule to u and records every callback call
// for the -callbacks inventory.
func (c *checker) contains(u *unit) {
	if !c.containScope(u.pkg.Path()) {
		return
	}
	s := &containScan{
		c:        c,
		u:        u,
		ifaces:   map[*types.Named]bool{},
		parent:   map[ast.Node]ast.Node{},
		encl:     map[ast.Node]ast.Node{},
		decls:    map[*types.Func]*ast.FuncDecl{},
		sites:    map[*types.Func][]ast.Node{},
		escaped:  map[*types.Func]bool{},
		argSites: map[*types.Func][]*ast.CallExpr{},
		asserted: map[types.Object]bool{},
		memo:     map[ast.Node]int{},
		pmemo:    map[types.Object]int{},
	}
	s.collectIfaces()
	if len(s.ifaces) == 0 {
		return
	}
	s.index()
	for _, site := range s.callbacks() {
		c.callbacks = append(c.callbacks, fmt.Sprintf("%s: %s contained=%t", c.pos(site.pos), site.call, site.contained))
		if !site.contained {
			c.report(site.pos, ruleContain, fmt.Sprintf("%s calls into a registered callback with no recover around it: run it through the package's containment helper (panicerr.FromRecovered in a deferred recover)", site.call))
		}
	}
}

type containScan struct {
	c *checker
	u *unit
	// ifaces holds the callback interfaces.
	ifaces map[*types.Named]bool
	// parent maps a node to its parent; encl maps a node to its enclosing
	// function (a *ast.FuncDecl or *ast.FuncLit).
	parent map[ast.Node]ast.Node
	encl   map[ast.Node]ast.Node
	// decls maps a package function or method to its declaration; sites
	// holds the calls of it; escaped marks one used as a value.
	decls   map[*types.Func]*ast.FuncDecl
	sites   map[*types.Func][]ast.Node
	escaped map[*types.Func]bool
	// argSites holds the calls a package function is passed to as a value.
	argSites map[*types.Func][]*ast.CallExpr
	// asserted holds the variables bound to a type assertion of a callback
	// interface value.
	asserted map[types.Object]bool
	// memo and pmemo cache containment of a function and of a parameter
	// (or local variable) holding a function: 1 in progress, 2 no, 3 yes.
	memo  map[ast.Node]int
	pmemo map[types.Object]int
}

// collectIfaces finds the callback interfaces the package's registries hold.
func (s *containScan) collectIfaces() {
	for _, f := range s.u.files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			obj := s.u.info.Defs[ts.Name]
			if obj == nil {
				return true
			}
			st, ok := obj.Type().Underlying().(*types.Struct)
			if !ok {
				return true
			}
			for i := 0; i < st.NumFields(); i++ {
				s.registryElem(st.Field(i).Type(), false, true)
			}
			return true
		})
	}
}

// registryElem records the callback interface a registry field type t
// holds. inColl is true below a slice, array, map or atomic.Pointer; deep
// allows one struct element level.
func (s *containScan) registryElem(t types.Type, inColl, deep bool) {
	switch tt := types.Unalias(t).(type) {
	case *types.Slice:
		s.registryElem(tt.Elem(), true, deep)
		return
	case *types.Array:
		s.registryElem(tt.Elem(), true, deep)
		return
	case *types.Map:
		s.registryElem(tt.Elem(), true, deep)
		return
	case *types.Pointer:
		s.registryElem(tt.Elem(), inColl, deep)
		return
	case *types.Named:
		if obj := tt.Obj(); obj.Pkg() != nil && obj.Pkg().Path() == "sync/atomic" && obj.Name() == "Pointer" {
			if args := tt.TypeArgs(); args != nil && args.Len() == 1 {
				s.registryElem(args.At(0), true, deep)
			}
			return
		}
		if !inColl {
			return
		}
		if _, ok := tt.Underlying().(*types.Interface); ok {
			if s.moduleType(tt) && callbackName(tt.Obj().Name()) {
				s.ifaces[tt] = true
			}
			return
		}
		if st, ok := tt.Underlying().(*types.Struct); ok && deep {
			for i := 0; i < st.NumFields(); i++ {
				s.registryElem(st.Field(i).Type(), true, false)
			}
		}
	}
}

func (s *containScan) moduleType(n *types.Named) bool {
	p := n.Obj().Pkg()
	return p != nil && (p.Path() == s.c.module || strings.HasPrefix(p.Path(), s.c.module+"/"))
}

// index records parents, enclosing functions, declarations, the calls of
// each package function and the variables asserted from a callback value.
func (s *containScan) index() {
	for _, f := range s.u.files {
		var stack []ast.Node
		var fns []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if len(fns) > 0 && fns[len(fns)-1] == top {
					fns = fns[:len(fns)-1]
				}
				return true
			}
			if len(stack) > 0 {
				s.parent[n] = stack[len(stack)-1]
			}
			if len(fns) > 0 {
				s.encl[n] = fns[len(fns)-1]
			}
			stack = append(stack, n)
			switch n := n.(type) {
			case *ast.FuncDecl:
				if fn, ok := s.u.info.Defs[n.Name].(*types.Func); ok {
					s.decls[fn] = n
				}
				fns = append(fns, n)
			case *ast.FuncLit:
				fns = append(fns, n)
			case *ast.Ident:
				fn, ok := s.u.info.Uses[n].(*types.Func)
				if !ok || fn.Pkg() != s.u.pkg {
					return true
				}
				// A use is a call when the ident (or the selector it
				// names) is the Fun of its parent call.
				var expr ast.Node = n
				if sel, ok := s.parent[n].(*ast.SelectorExpr); ok && sel.Sel == n {
					expr = sel
				}
				call, ok := s.parent[expr].(*ast.CallExpr)
				switch {
				case ok && call.Fun == expr:
					s.sites[fn] = append(s.sites[fn], call)
				case ok && argIndex(call, expr) >= 0:
					// Passed as a value to a parameter: contained when
					// the parameter is (see containedUncached).
					s.argSites[fn] = append(s.argSites[fn], call)
				default:
					s.escaped[fn] = true
				}
			case *ast.AssignStmt:
				s.recordAssert(n.Lhs, n.Rhs)
			case *ast.ValueSpec:
				lhs := make([]ast.Expr, len(n.Names))
				for i, id := range n.Names {
					lhs[i] = id
				}
				s.recordAssert(lhs, n.Values)
			}
			return true
		})
	}
}

// argIndex returns the index of e among call's arguments, or -1.
func argIndex(call *ast.CallExpr, e ast.Node) int {
	for i, a := range call.Args {
		if a == e {
			return i
		}
	}
	return -1
}

func (s *containScan) recordAssert(lhs, rhs []ast.Expr) {
	if len(rhs) != 1 || len(lhs) == 0 {
		return
	}
	ta, ok := ast.Unparen(rhs[0]).(*ast.TypeAssertExpr)
	if !ok || !s.callbackValue(ta.X) {
		return
	}
	if id, ok := lhs[0].(*ast.Ident); ok {
		if obj := s.u.info.Defs[id]; obj != nil {
			s.asserted[obj] = true
		} else if obj := s.u.info.Uses[id]; obj != nil {
			s.asserted[obj] = true
		}
	}
}

// callbackValue reports whether e is a value of a callback interface, or
// a variable asserted from one.
func (s *containScan) callbackValue(e ast.Expr) bool {
	e = ast.Unparen(e)
	if id, ok := e.(*ast.Ident); ok {
		if obj := s.u.info.Uses[id]; obj != nil && s.asserted[obj] {
			return true
		}
	}
	if ta, ok := e.(*ast.TypeAssertExpr); ok && ta.Type != nil && s.callbackValue(ta.X) {
		return true
	}
	tv, ok := s.u.info.Types[e]
	if !ok {
		return false
	}
	n, ok := types.Unalias(tv.Type).(*types.Named)
	return ok && s.ifaces[n]
}

// callbackName reports whether an interface name names a user callback
// kind: an observer, a listener or a subscriber. A registry of other
// interfaces (the database drivers a manager holds) holds infrastructure,
// not callbacks fired at events.
func callbackName(name string) bool {
	for _, kind := range []string{"Observer", "Listener", "Subscriber"} {
		if strings.HasSuffix(name, kind) {
			return true
		}
	}
	return false
}

// callbacks lists the package's callback calls, sorted by position.
func (s *containScan) callbacks() []callbackSite {
	var out []callbackSite
	for _, f := range s.u.files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			selection := s.u.info.Selections[sel]
			if selection == nil || selection.Kind() != types.MethodVal || !types.IsInterface(selection.Recv()) {
				return true
			}
			if !s.callbackValue(sel.X) || s.decorator(call) || s.callersOwn(sel.X) {
				return true
			}
			out = append(out, callbackSite{
				pos:       call.Pos(),
				call:      types.ExprString(sel.X) + "." + sel.Sel.Name,
				contained: s.containedAt(call),
			})
			return true
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pos < out[j].pos })
	return out
}

// callersOwn reports whether x is a parameter of the exported function
// declaration the call is written in, called directly in its body (an
// unexported helper's parameter is the framework's own pass of a
// registered value, not the caller's): the caller handed
// the value in and the call runs it on the caller's goroutine before
// returning (registering a subscriber), so a panic reaches the code that
// passed it, as if it had called it itself. Nothing is fanned out.
func (s *containScan) callersOwn(x ast.Expr) bool {
	id, ok := ast.Unparen(x).(*ast.Ident)
	if !ok {
		return false
	}
	decl, ok := s.encl[id].(*ast.FuncDecl)
	if !ok || !decl.Name.IsExported() {
		return false
	}
	obj := s.u.info.Uses[id]
	for _, field := range decl.Type.Params.List {
		for _, name := range field.Names {
			if obj != nil && s.u.info.Defs[name] == obj {
				return true
			}
		}
	}
	return false
}

// decorator reports whether call is written in a method of a type that
// implements a callback interface: that type is itself the callback.
func (s *containScan) decorator(call ast.Node) bool {
	fn := s.outerDecl(call)
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return false
	}
	recv := s.u.info.TypeOf(fn.Recv.List[0].Type)
	if recv == nil {
		return false
	}
	for n := range s.ifaces {
		iface := n.Underlying().(*types.Interface)
		if types.Implements(recv, iface) {
			return true
		}
		if _, isPtr := recv.(*types.Pointer); !isPtr && types.Implements(types.NewPointer(recv), iface) {
			return true
		}
	}
	return false
}

func (s *containScan) outerDecl(n ast.Node) *ast.FuncDecl {
	for n != nil {
		if d, ok := n.(*ast.FuncDecl); ok {
			return d
		}
		n = s.parent[n]
	}
	return nil
}

// containedAt reports whether node n runs under a recover: its enclosing
// function is contained.
func (s *containScan) containedAt(n ast.Node) bool {
	fn := s.encl[n]
	if fn == nil {
		return false
	}
	// A go statement starts a new goroutine: nothing the starting
	// function defers covers it. inGo checks n's own function only.
	return s.contained(fn)
}

// contained reports whether function node fn (a declaration or a literal)
// runs under a recover.
func (s *containScan) contained(fn ast.Node) bool {
	switch s.memo[fn] {
	case 1, 2:
		return false
	case 3:
		return true
	}
	s.memo[fn] = 1
	ok := s.containedUncached(fn)
	if ok {
		s.memo[fn] = 3
	} else {
		s.memo[fn] = 2
	}
	return ok
}

func (s *containScan) containedUncached(fn ast.Node) bool {
	if s.defersRecover(fn) {
		return true
	}
	switch fn := fn.(type) {
	case *ast.FuncDecl:
		obj, _ := s.u.info.Defs[fn.Name].(*types.Func)
		if obj == nil || obj.Exported() || s.escaped[obj] || len(s.sites[obj])+len(s.argSites[obj]) == 0 {
			return false
		}
		for _, call := range s.sites[obj] {
			if s.inGo(call) || !s.containedAt(call) {
				return false
			}
		}
		for _, call := range s.argSites[obj] {
			if !s.argContained(call, argIndex(call, s.argOf(call, obj))) {
				return false
			}
		}
		return true
	case *ast.FuncLit:
		return s.litContained(fn)
	}
	return false
}

// litContained reports whether a function literal runs under a recover:
// called in place or deferred in a contained function, or bound to a
// variable or parameter every use of which is contained.
func (s *containScan) litContained(lit *ast.FuncLit) bool {
	p := s.parent[lit]
	switch p := p.(type) {
	case *ast.CallExpr:
		if p.Fun == lit {
			if _, isGo := s.parent[p].(*ast.GoStmt); isGo {
				return false
			}
			return s.containedAt(p)
		}
		// An argument: contained when the callee's parameter is.
		for i, a := range p.Args {
			if a == lit {
				return s.argContained(p, i)
			}
		}
	case *ast.AssignStmt:
		for i, r := range p.Rhs {
			if r == lit && i < len(p.Lhs) {
				if id, ok := p.Lhs[i].(*ast.Ident); ok {
					obj := s.u.info.Defs[id]
					if obj == nil {
						obj = s.u.info.Uses[id]
					}
					return obj != nil && s.varContained(obj)
				}
			}
		}
	case *ast.ValueSpec:
		for i, r := range p.Values {
			if r == lit && i < len(p.Names) {
				if obj := s.u.info.Defs[p.Names[i]]; obj != nil {
					return s.varContained(obj)
				}
			}
		}
	}
	return false
}

// argContained reports whether argument i of call reaches a parameter of
// a package function that is contained.
func (s *containScan) argContained(call *ast.CallExpr, i int) bool {
	if s.inGo(call) {
		return false
	}
	fn := s.callee(call)
	if fn == nil {
		return false
	}
	decl := s.decls[fn]
	if decl == nil {
		return false
	}
	param := s.paramObj(decl, i)
	return param != nil && s.varContained(param)
}

// argOf returns the argument of call that names fn.
func (s *containScan) argOf(call *ast.CallExpr, fn *types.Func) ast.Node {
	for _, a := range call.Args {
		id, ok := a.(*ast.Ident)
		if sel, isSel := a.(*ast.SelectorExpr); isSel {
			id, ok = sel.Sel, true
		}
		if ok && s.u.info.Uses[id] == fn {
			return a
		}
	}
	return nil
}

func (s *containScan) callee(call *ast.CallExpr) *types.Func {
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		fn, _ := s.u.info.Uses[f].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		fn, _ := s.u.info.Uses[f.Sel].(*types.Func)
		return fn
	}
	return nil
}

func (s *containScan) paramObj(decl *ast.FuncDecl, i int) types.Object {
	n := 0
	for _, field := range decl.Type.Params.List {
		names := field.Names
		if len(names) == 0 {
			if n == i {
				return nil
			}
			n++
			continue
		}
		for _, id := range names {
			if n == i {
				return s.u.info.Defs[id]
			}
			n++
		}
	}
	return nil
}

// varContained reports whether every use of v, a variable or parameter
// holding a function, is a contained call or a pass to a contained
// parameter.
func (s *containScan) varContained(v types.Object) bool {
	switch s.pmemo[v] {
	case 1, 2:
		return false
	case 3:
		return true
	}
	s.pmemo[v] = 1
	ok := true
	for _, f := range s.u.files {
		if f.Pos() > v.Pos() || v.Pos() > f.End() {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if !ok {
				return false
			}
			id, isID := n.(*ast.Ident)
			if !isID || s.u.info.Uses[id] != v {
				return true
			}
			switch p := s.parent[id].(type) {
			case *ast.CallExpr:
				if p.Fun == id {
					if s.inGo(p) || !s.containedAt(p) {
						ok = false
					}
					return true
				}
				for i, a := range p.Args {
					if a == id {
						if !s.argContained(p, i) {
							ok = false
						}
						return true
					}
				}
				ok = false
			default:
				ok = false
			}
			return true
		})
	}
	if ok {
		s.pmemo[v] = 3
	} else {
		s.pmemo[v] = 2
	}
	return ok
}

// inGo reports whether call is the call of a go statement.
func (s *containScan) inGo(call ast.Node) bool {
	_, ok := s.parent[call].(*ast.GoStmt)
	return ok
}

// defersRecover reports whether fn's own body (not a nested literal's)
// defers a function that calls recover directly.
func (s *containScan) defersRecover(fn ast.Node) bool {
	var body *ast.BlockStmt
	switch fn := fn.(type) {
	case *ast.FuncDecl:
		body = fn.Body
	case *ast.FuncLit:
		body = fn.Body
	}
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.DeferStmt:
			switch f := ast.Unparen(n.Call.Fun).(type) {
			case *ast.FuncLit:
				found = s.callsRecover(f.Body)
			default:
				if fnObj := s.callee(n.Call); fnObj != nil {
					if d := s.decls[fnObj]; d != nil {
						found = s.callsRecover(d.Body)
					}
				}
			}
			return false
		}
		return true
	})
	return found
}

// callsRecover reports whether body calls the recover builtin directly.
func (s *containScan) callsRecover(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok {
				if b, ok := s.u.info.Uses[id].(*types.Builtin); ok && b.Name() == "recover" {
					found = true
				}
			}
		}
		return true
	})
	return found
}
