package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

// admissionFuncs are the queue package's admission functions: the one
// place a job its caller handed in is refused when nil, before any of its
// methods runs.
var admissionFuncs = map[string]bool{"admitJob": true, "AdmitJob": true, "admitBatch": true}

// admissions applies the admission rule to u: an entry point that takes a
// job from its caller admits it before anything else touches it.
func (c *checker) admissions(u *unit) {
	for _, f := range u.files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || !fd.Name.IsExported() {
				continue
			}
			if !strings.HasPrefix(fd.Name.Name, "Push") && !strings.HasPrefix(fd.Name.Name, "Dispatch") {
				continue
			}
			all := &admissionScan{c: c, u: u, params: map[types.Object]bool{}, fields: map[types.Object]bool{}}
			all.track(fd)
			// Each job is admitted on its own: admitting one parameter
			// does not admit another.
			var jobs []*admissionScan
			for obj := range all.params {
				jobs = append(jobs, &admissionScan{c: c, u: u, params: map[types.Object]bool{obj: true}})
			}
			for obj := range all.fields {
				jobs = append(jobs, &admissionScan{c: c, u: u, fields: map[types.Object]bool{obj: true}})
			}
			for _, a := range jobs {
				if bad := a.stmts(fd.Body.List); bad != token.NoPos {
					c.report(bad, ruleAdmission, fmt.Sprintf("%s touches the job before admitting it: the first statement that uses it must call the queue's admitJob or AdmitJob (admitBatch for a batch's jobs) with it, or return another push given it unchanged", fd.Name.Name))
				}
			}
		}
	}
}

// admissionScan is the state of one entry point's scan: the parameters
// of job type, and the receiver's fields holding a slice of jobs (track
// collects all of them; each scan then follows one).
type admissionScan struct {
	c      *checker
	u      *unit
	params map[types.Object]bool
	fields map[types.Object]bool
}

func (a *admissionScan) track(fd *ast.FuncDecl) {
	for _, field := range fd.Type.Params.List {
		for _, name := range field.Names {
			if obj := a.u.info.Defs[name]; obj != nil && a.isJob(obj.Type()) {
				a.params[obj] = true
			}
		}
	}
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return
	}
	t := a.u.info.Types[fd.Recv.List[0].Type].Type
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return
	}
	for i := 0; i < st.NumFields(); i++ {
		if s, ok := st.Field(i).Type().Underlying().(*types.Slice); ok && a.isJob(s.Elem()) {
			a.fields[st.Field(i)] = true
		}
	}
}

// isJob reports whether t is the contract package's QueueJob (or an alias
// of it, as queue.Job is).
func (a *admissionScan) isJob(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return false
	}
	return n.Obj().Name() == "QueueJob" && n.Obj().Pkg().Path() == a.c.module+"/contract"
}

// stmts scans a statement list in order and returns where a job is
// touched before its admission, or token.NoPos when none is. A statement
// that does not touch the job is passed over. An if without an else that
// touches the job only in its body (the dedupe-key fallback to the plain
// push) is fine when the body admits or delegates, and the scan goes on
// after it, since the branch need not run.
func (a *admissionScan) stmts(list []ast.Stmt) token.Pos {
	for _, s := range list {
		first := a.firstTouch(s)
		if first == token.NoPos {
			continue
		}
		if a.admits(s, first) || a.delegates(s, first) {
			return token.NoPos
		}
		if ifs, ok := s.(*ast.IfStmt); ok && ifs.Else == nil && a.firstTouch(ifs.Init) == token.NoPos && a.firstTouch(ifs.Cond) == token.NoPos {
			if bad := a.stmts(ifs.Body.List); bad != token.NoPos {
				return bad
			}
			continue
		}
		return first
	}
	return token.NoPos
}

// firstTouch returns the position of the first use of a tracked job in n,
// in source order. len of a job slice reads no job and is not a use.
func (a *admissionScan) firstTouch(n ast.Node) token.Pos {
	if n == nil {
		return token.NoPos
	}
	first := token.NoPos
	ast.Inspect(n, func(n ast.Node) bool {
		if first != token.NoPos {
			return false
		}
		switch n := n.(type) {
		case *ast.CallExpr:
			if id, ok := ast.Unparen(n.Fun).(*ast.Ident); ok {
				if b, ok := a.u.info.Uses[id].(*types.Builtin); ok && b.Name() == "len" {
					return false
				}
			}
		case *ast.Ident:
			if a.params[a.u.info.Uses[n]] {
				first = n.Pos()
			}
		case *ast.SelectorExpr:
			if sel := a.u.info.Selections[n]; sel != nil && a.fields[sel.Obj()] {
				first = n.Pos()
				return false
			}
		}
		return true
	})
	return first
}

// isTracked reports whether e is a tracked job itself: the parameter, or
// the receiver's job-slice field.
func (a *admissionScan) isTracked(e ast.Expr) bool {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		return a.params[a.u.info.Uses[e]]
	case *ast.SelectorExpr:
		sel := a.u.info.Selections[e]
		return sel != nil && a.fields[sel.Obj()]
	}
	return false
}

// callee returns the function or method a call names, or nil for a call
// through a func value.
func (a *admissionScan) callee(call *ast.CallExpr) *types.Func {
	var id *ast.Ident
	switch fn := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		id = fn
	case *ast.SelectorExpr:
		id = fn.Sel
	}
	if id == nil {
		return nil
	}
	f, _ := a.u.info.Uses[id].(*types.Func)
	return f
}

// admits reports whether the first touch of the job in s is the job
// handed to an admission function as its first argument (outside a func
// literal, which need not run).
func (a *admissionScan) admits(s ast.Stmt, first token.Pos) bool {
	found := false
	ast.Inspect(s, func(n ast.Node) bool {
		if found {
			return false
		}
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			f := a.callee(n)
			if f != nil && f.Pkg() != nil && f.Pkg().Path() == a.c.module+"/queue" && admissionFuncs[f.Name()] &&
				len(n.Args) > 0 && a.isTracked(n.Args[0]) && n.Args[0].Pos() == first {
				found = true
			}
		}
		return true
	})
	return found
}

// delegates reports whether s returns a call to another push or dispatch
// given the job unchanged, as the job argument, as its first touch: that
// entry point admits it.
func (a *admissionScan) delegates(s ast.Stmt, first token.Pos) bool {
	ret, ok := s.(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	call, ok := ast.Unparen(ret.Results[0]).(*ast.CallExpr)
	if !ok {
		return false
	}
	f := a.callee(call)
	if f == nil || (!strings.HasPrefix(f.Name(), "Push") && !strings.HasPrefix(f.Name(), "Dispatch")) {
		return false
	}
	sig := f.Type().(*types.Signature)
	for i, arg := range call.Args {
		if i >= sig.Params().Len() {
			break
		}
		if a.isTracked(arg) && arg.Pos() == first && a.isJob(sig.Params().At(i).Type()) {
			return true
		}
	}
	return false
}
