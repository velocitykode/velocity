package main

import (
	"fmt"
	"go/types"
	"reflect"
)

// fields applies the field rule to u's events and records them.
func (c *checker) fields(u *unit) {
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
		if !ok || !c.isEvent(named, st) {
			continue
		}
		owner := u.pkg.Name() + "." + tn.Name()
		c.events = append(c.events, c.pos(tn.Pos())+": "+owner)
		errCodec := ownsCodec(named, "MarshalJSON", "UnmarshalJSON")
		c.walkFields(owner, st, errCodec, map[types.Type]bool{named: true})
	}
}

// isEvent reports whether named is a framework event: it has a Name()
// string method on its value or pointer, or embeds contract.EventMeta.
func (c *checker) isEvent(named *types.Named, st *types.Struct) bool {
	ms := types.NewMethodSet(types.NewPointer(named))
	if sel := ms.Lookup(nil, "Name"); sel != nil {
		if sig, ok := sel.Obj().Type().(*types.Signature); ok && sig.Params().Len() == 0 && sig.Results().Len() == 1 {
			if b, ok := sig.Results().At(0).Type().(*types.Basic); ok && b.Kind() == types.String {
				return true
			}
		}
	}
	return c.embedsMeta(st, map[*types.Struct]bool{})
}

func (c *checker) embedsMeta(st *types.Struct, seen map[*types.Struct]bool) bool {
	if seen[st] {
		return false
	}
	seen[st] = true
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if !f.Embedded() {
			continue
		}
		t := f.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := t.(*types.Named); ok {
			if n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == c.module+"/contract" && n.Obj().Name() == "EventMeta" {
				return true
			}
			if inner, ok := n.Underlying().(*types.Struct); ok && c.embedsMeta(inner, seen) {
				return true
			}
		}
	}
	return false
}

// walkFields walks st's JSON-visible fields; errCodec says the event owns
// a JSON codec, which carries its error fields as text.
func (c *checker) walkFields(owner string, st *types.Struct, errCodec bool, seen map[types.Type]bool) {
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if reflect.StructTag(st.Tag(i)).Get("json") == "-" {
			continue
		}
		if f.Embedded() {
			t := f.Type()
			if p, ok := t.(*types.Pointer); ok {
				t = p.Elem()
			}
			if inner, ok := t.Underlying().(*types.Struct); ok && !seen[t] && !leaf(t) {
				seen[t] = true
				c.walkFields(owner, inner, errCodec, seen)
				continue
			}
		}
		if !f.Exported() {
			continue
		}
		if bad := untyped(f.Type(), errCodec, map[types.Type]bool{}); bad != "" {
			c.report(f.Pos(), ruleField, fmt.Sprintf("%s.%s: field type %s holds %s", owner, f.Name(), types.TypeString(f.Type(), qual), bad))
		}
	}
}

// untyped returns a description of the part of t that does not survive
// the codec, or "".
func untyped(t types.Type, errCodec bool, seen map[types.Type]bool) string {
	if seen[t] {
		return ""
	}
	seen[t] = true
	if leaf(t) {
		return ""
	}
	switch u := t.(type) {
	case *types.TypeParam:
		return "type parameter " + u.Obj().Name()
	case *types.Alias:
		return untyped(types.Unalias(u), errCodec, seen)
	}
	if isError(t) {
		if errCodec {
			return ""
		}
		return "error (the event has no MarshalJSON/UnmarshalJSON pair)"
	}
	switch u := t.Underlying().(type) {
	case *types.Interface:
		return "interface " + types.TypeString(t, qual)
	case *types.Pointer:
		return untyped(u.Elem(), errCodec, seen)
	case *types.Slice:
		return untyped(u.Elem(), errCodec, seen)
	case *types.Array:
		return untyped(u.Elem(), errCodec, seen)
	case *types.Map:
		if s := untyped(u.Key(), errCodec, seen); s != "" {
			return s
		}
		return untyped(u.Elem(), errCodec, seen)
	case *types.Signature:
		return "func " + types.TypeString(t, qual)
	case *types.Chan:
		return "chan " + types.TypeString(t, qual)
	case *types.Basic:
		if u.Kind() == types.UnsafePointer {
			return "unsafe.Pointer"
		}
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			if reflect.StructTag(u.Tag(i)).Get("json") == "-" || (!f.Exported() && !f.Embedded()) {
				continue
			}
			if s := untyped(f.Type(), errCodec, seen); s != "" {
				return s
			}
		}
	}
	return ""
}

// leaf reports whether t owns its codec (a MarshalJSON/UnmarshalJSON or
// MarshalText/UnmarshalText pair), so its fields are its own business.
func leaf(t types.Type) bool {
	if _, ok := types.Unalias(t).(*types.Named); !ok {
		return false
	}
	return ownsCodec(t, "MarshalJSON", "UnmarshalJSON") || ownsCodec(t, "MarshalText", "UnmarshalText")
}

func ownsCodec(t types.Type, marshal, unmarshal string) bool {
	ms := types.NewMethodSet(types.NewPointer(t))
	return ms.Lookup(nil, marshal) != nil && ms.Lookup(nil, unmarshal) != nil
}

func isError(t types.Type) bool {
	return types.Identical(t, types.Universe.Lookup("error").Type())
}

func qual(p *types.Package) string { return p.Name() }
