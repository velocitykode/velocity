package main

import (
	"go/ast"
	"go/token"
	"go/types"
)

// envelopes records u's events without the envelope and the event types
// u builds; check reports the pairs once every package is seen, since an
// event may be built outside the package that declares it.
func (c *checker) envelopes(u *unit) {
	scope := u.pkg.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || tn.IsAlias() || !tn.Exported() {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok {
			continue
		}
		st, ok := named.Underlying().(*types.Struct)
		if !ok || !c.isEvent(named, st) || c.embedsMeta(st, map[*types.Struct]bool{}) {
			continue
		}
		if c.bare == nil {
			c.bare = map[string]bareEvent{}
		}
		c.bare[typeKey(named)] = bareEvent{tn.Pos(), u.pkg.Name() + "." + tn.Name()}
	}

	if c.built == nil {
		c.built = map[string]bool{}
	}
	for _, f := range u.files {
		// parts are the literals that fill an embedded field of an
		// enclosing literal: they build part of another value, not an
		// event of their own.
		parts := map[*ast.CompositeLit]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CompositeLit:
				for _, el := range n.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					if v, ok := u.info.Uses[key].(*types.Var); ok && v.Embedded() {
						if lit := compositeOf(kv.Value); lit != nil {
							parts[lit] = true
						}
					}
				}
				if !parts[n] {
					c.markBuilt(u.info.Types[n].Type)
				}
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok && len(n.Args) == 1 {
					if b, ok := u.info.Uses[id].(*types.Builtin); ok && b.Name() == "new" {
						c.markBuilt(u.info.Types[n.Args[0]].Type)
					}
				}
			}
			return true
		})
	}
}

// compositeOf returns the literal e is, or takes the address of.
func compositeOf(e ast.Expr) *ast.CompositeLit {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	lit, _ := e.(*ast.CompositeLit)
	return lit
}

func (c *checker) markBuilt(t types.Type) {
	if t == nil {
		return
	}
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := types.Unalias(t).(*types.Named); ok && n.Obj().Pkg() != nil {
		c.built[typeKey(n)] = true
	}
}

// reportEnvelopes reports every event without the envelope that some
// package of the module builds.
func (c *checker) reportEnvelopes() {
	for key, e := range c.bare {
		if c.built[key] {
			c.report(e.pos, ruleEnvelope, e.owner+": the framework builds this event, and it does not embed contract.EventMeta")
		}
	}
}

// bareEvent is an event type without the envelope.
type bareEvent struct {
	pos   token.Pos
	owner string
}

func typeKey(n *types.Named) string {
	return n.Obj().Pkg().Path() + "." + n.Obj().Name()
}
