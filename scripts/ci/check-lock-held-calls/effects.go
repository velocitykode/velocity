package main

import (
	"go/ast"
	"go/types"
	"sort"
	"strconv"
	"strings"
)

// Lock effects: a function that returns a func value and still holds a
// lock when it returns is a lock-returning helper,
//
//	func (d *D) lockPath() (unlock func()) { d.mu.Lock(); return d.mu.Unlock }
//
// A call to it takes the lock; calling or deferring the variable its
// result is assigned to releases it, as does unlocking the lock itself.

// hole is the placeholder a summary key starts with in place of the
// receiver (hole(-1)) or parameter i (hole(i)), so a call site can put its
// own expression there.
func hole(i int) string { return "\x00" + strconv.Itoa(i) }

// lockEffects records holds for every declared function returning a func
// value, repeating until no summary changes so a helper built on another
// helper is seen too.
func (a *analysis) lockEffects() {
	type decl struct {
		u  *unit
		fd *ast.FuncDecl
		fn *types.Func
	}
	var decls []decl
	for _, u := range a.units {
		for _, f := range u.files {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				fn, _ := u.info.Defs[fd.Name].(*types.Func)
				if fn == nil || !returnsFunc(fn) {
					continue
				}
				decls = append(decls, decl{u, fd, fn})
			}
		}
	}
	for round := 0; round < 4; round++ {
		changed := false
		for _, d := range decls {
			w := &walker{a: a, u: d.u, silent: true}
			exit := w.runScope(d.fd.Body, held{})
			keys := holeKeys(d.u, d.fd, exit)
			key := funcKey(d.fn)
			s := a.funcs[key]
			if s == nil {
				s = &funcSummary{}
				a.funcs[key] = s
			}
			if strings.Join(s.holds, "\n") != strings.Join(keys, "\n") {
				s.holds = keys
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

// returnsFunc reports whether fn has a func-typed result.
func returnsFunc(fn *types.Func) bool {
	res := fn.Type().(*types.Signature).Results()
	for i := 0; i < res.Len(); i++ {
		if _, ok := res.At(i).Type().Underlying().(*types.Signature); ok {
			return true
		}
	}
	return false
}

// holeKeys turns the keys held at fd's exits into summary keys: a key
// naming fd's receiver or a parameter gets that name's hole.
func holeKeys(u *unit, fd *ast.FuncDecl, exit held) []string {
	names := map[string]string{}
	if fd.Recv != nil {
		for _, f := range fd.Recv.List {
			for _, n := range f.Names {
				names[n.Name] = hole(-1)
			}
		}
	}
	i := 0
	for _, f := range fd.Type.Params.List {
		if len(f.Names) == 0 {
			i++
			continue
		}
		for _, n := range f.Names {
			names[n.Name] = hole(i)
			i++
		}
	}
	var keys []string
	for k := range exit {
		head, rest, _ := strings.Cut(k, ".")
		if h, ok := names[head]; ok {
			k = h + "." + rest
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// holdsOf returns the locks call takes when it calls a lock-returning
// helper, named as the caller names them; nil for any other call.
func (a *analysis) holdsOf(u *unit, call *ast.CallExpr) []string {
	fn := staticCallee(u, call)
	if fn == nil {
		return nil
	}
	s := a.funcs[funcKey(fn)]
	if s == nil || len(s.holds) == 0 {
		return nil
	}
	fill := map[string]string{}
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok {
		if x, ok := u.info.Selections[sel]; ok && x.Kind() == types.MethodVal {
			fill[hole(-1)] = types.ExprString(sel.X)
		}
	}
	for i, arg := range call.Args {
		fill[hole(i)] = types.ExprString(arg)
	}
	keys := make([]string, 0, len(s.holds))
	for _, k := range s.holds {
		if head, rest, ok := strings.Cut(k, "."); ok && strings.HasPrefix(head, "\x00") {
			e, ok := fill[head]
			if !ok {
				// A receiver or parameter this call does not name: keep
				// the lock under a name no unlock here can match.
				e = shortName(funcKey(fn)) + "()"
			}
			k = e + "." + rest
		}
		keys = append(keys, k)
	}
	return keys
}

// assignedObjects returns the variables as assigns to that have a func
// type: where a lock-returning helper's release func lands.
func assignedObjects(u *unit, as *ast.AssignStmt) []types.Object {
	var out []types.Object
	for _, e := range as.Lhs {
		id, ok := ast.Unparen(e).(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		obj := u.info.Defs[id]
		if obj == nil {
			obj = u.info.Uses[id]
		}
		if v, ok := obj.(*types.Var); ok {
			if _, ok := v.Type().Underlying().(*types.Signature); ok {
				out = append(out, v)
			}
		}
	}
	return out
}
