package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"strings"
)

// Callback calls: functions of encoding/json, encoding/xml, encoding/gob
// and io that call methods of the values they are given. An encoder calls
// a value's MarshalJSON (or MarshalText, MarshalXML, GobEncode,
// MarshalBinary, the encoding interfaces), a decoder the Unmarshal
// counterparts, and io.Copy and its siblings the Read and Write of their
// readers and writers. Such a call runs user code when a value it is
// given can hold any code: an interface, a type parameter, or a stdlib
// encoder, decoder or reader that wraps one. A value of a concrete type
// only runs the methods that type declares: a module method is followed
// like any module function (reach), a stdlib or third-party one is fixed
// code.

// codec describes one encoding package: the struct tag key that skips a
// field ("" when fields are skipped only by being unexported) and the
// methods its encoder and its decoder call on a value.
type codec struct {
	tag    string
	encode []string
	decode []string
}

var (
	jsonCodec = &codec{tag: "json", encode: []string{"MarshalJSON", "MarshalText"}, decode: []string{"UnmarshalJSON", "UnmarshalText"}}
	xmlCodec  = &codec{tag: "xml", encode: []string{"MarshalXML", "MarshalXMLAttr", "MarshalText"}, decode: []string{"UnmarshalXML", "UnmarshalXMLAttr", "UnmarshalText"}}
	gobCodec  = &codec{encode: []string{"GobEncode", "MarshalBinary"}, decode: []string{"GobDecode", "UnmarshalBinary"}}
)

// ioMethods are the methods io's copy and read helpers call on a reader
// or writer.
var ioMethods = []string{"Read", "Write", "ReadFrom", "WriteTo", "ReadAt", "WriteAt", "WriteString"}

type role int

const (
	roleEncode  role = iota // a value the codec encodes
	roleDecode              // a pointer the codec decodes into
	roleIO                  // a reader or writer the call reads or writes
	roleWrapped             // the receiver: a stdlib value wrapping a reader or writer
)

type callbackArg struct {
	idx   int // argument index; -1 for the receiver
	role  role
	codec *codec
}

// callbacks maps each callback function, keyed as funcKey names it, to
// the arguments through which it calls methods. Constructors that only
// store a reader or writer (json.NewDecoder, io.LimitReader) are not
// listed: they call nothing.
var callbacks = func() map[string][]callbackArg {
	m := map[string][]callbackArg{}
	wrapped := callbackArg{idx: -1, role: roleWrapped}
	for pkg, c := range map[string]*codec{"encoding/json": jsonCodec, "encoding/xml": xmlCodec} {
		m[pkg+".Marshal"] = []callbackArg{{0, roleEncode, c}}
		m[pkg+".MarshalIndent"] = []callbackArg{{0, roleEncode, c}}
		m[pkg+".Unmarshal"] = []callbackArg{{1, roleDecode, c}}
		m[pkg+".Encoder.Encode"] = []callbackArg{wrapped, {0, roleEncode, c}}
		m[pkg+".Decoder.Decode"] = []callbackArg{wrapped, {0, roleDecode, c}}
		m[pkg+".Decoder.Token"] = []callbackArg{wrapped}
	}
	m["encoding/json.Decoder.More"] = []callbackArg{wrapped}
	m["encoding/xml.Encoder.EncodeElement"] = []callbackArg{wrapped, {0, roleEncode, xmlCodec}}
	m["encoding/xml.Encoder.EncodeToken"] = []callbackArg{wrapped}
	m["encoding/xml.Encoder.Flush"] = []callbackArg{wrapped}
	m["encoding/xml.Encoder.Close"] = []callbackArg{wrapped}
	m["encoding/xml.Decoder.DecodeElement"] = []callbackArg{wrapped, {0, roleDecode, xmlCodec}}
	m["encoding/xml.Decoder.RawToken"] = []callbackArg{wrapped}
	m["encoding/gob.Encoder.Encode"] = []callbackArg{wrapped, {0, roleEncode, gobCodec}}
	m["encoding/gob.Encoder.EncodeValue"] = []callbackArg{wrapped, {0, roleEncode, gobCodec}}
	m["encoding/gob.Decoder.Decode"] = []callbackArg{wrapped, {0, roleDecode, gobCodec}}
	m["encoding/gob.Decoder.DecodeValue"] = []callbackArg{wrapped, {0, roleDecode, gobCodec}}
	for name, args := range map[string][]int{
		"Copy": {0, 1}, "CopyBuffer": {0, 1}, "CopyN": {0, 1},
		"ReadAll": {0}, "ReadFull": {0}, "ReadAtLeast": {0}, "WriteString": {0},
	} {
		for _, i := range args {
			m["io."+name] = append(m["io."+name], callbackArg{i, roleIO, nil})
		}
	}
	for _, name := range []string{"LimitedReader.Read", "SectionReader.Read", "SectionReader.ReadAt", "OffsetWriter.Write", "OffsetWriter.WriteAt"} {
		m["io."+name] = []callbackArg{wrapped}
	}
	return m
}()

// callbackResult is what a callback call reaches: open names why it can
// run any code ("" when it cannot), methods the module methods it calls.
type callbackResult struct {
	open    string
	methods []*types.Func
}

// callback classifies call against the callbacks table. body is the
// function body the call is in, for fresh.
func (u *unit) callback(call *ast.CallExpr, body *ast.BlockStmt) callbackResult {
	var r callbackResult
	fn := staticCallee(u, call)
	if fn == nil || fn.Pkg() == nil {
		return r
	}
	args, ok := callbacks[funcKey(fn)]
	if !ok {
		return r
	}
	for _, a := range args {
		var arg ast.Expr
		switch {
		case a.idx < 0:
			if a.role == roleWrapped && r.open == "" {
				r.open = "(its reader or writer can be any code)"
			}
			continue
		case a.idx < len(call.Args):
			arg = call.Args[a.idx]
		default:
			continue
		}
		tv, ok := u.info.Types[arg]
		if !ok || tv.Type == nil {
			continue
		}
		w := typeWalk{u: u, seen: map[types.Type]bool{}}
		switch a.role {
		case roleEncode:
			w.names, w.tag = a.codec.encode, a.codec.tag
			w.walk(tv.Type, false)
		case roleDecode:
			w.names, w.tag = a.codec.decode, a.codec.tag
			w.walk(tv.Type, u.fresh(arg, call.Pos(), body))
		case roleIO:
			w.names = ioMethods
			w.shallow(tv.Type)
		}
		if w.open != "" && r.open == "" {
			r.open = "(" + w.open + ")"
		}
		r.methods = append(r.methods, w.methods...)
	}
	return r
}

// typeWalk looks through a type the way an encoder does, for values that
// can run any code (open) and module methods the encoder calls.
type typeWalk struct {
	u       *unit
	names   []string // the methods the codec calls
	tag     string   // struct tag key that skips a field
	seen    map[types.Type]bool
	open    string
	methods []*types.Func
}

// method returns the first of w.names that t's method set (or its
// pointer's) declares.
func (w *typeWalk) method(t types.Type) *types.Func {
	for _, name := range w.names {
		obj, _, _ := types.LookupFieldOrMethod(t, true, nil, name)
		if fn, ok := obj.(*types.Func); ok {
			return fn
		}
	}
	return nil
}

// found records that the codec calls fn: module code is followed, other
// code is fixed.
func (w *typeWalk) found(fn *types.Func) {
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && types.IsInterface(sig.Recv().Type()) {
		w.open = "a value whose " + fn.Name() + " comes from an embedded interface"
		return
	}
	if fn.Pkg() != nil && inModule(w.u.module, fn.Pkg().Path()) {
		w.methods = append(w.methods, fn)
	}
}

// shallow classifies a reader or writer: an interface or type parameter
// can be any code; a concrete type runs its own methods.
func (w *typeWalk) shallow(t types.Type) {
	switch t.(type) {
	case *types.TypeParam:
		w.open = "a type parameter"
		return
	}
	if types.IsInterface(t) {
		w.open = "a value of interface type " + types.TypeString(t, nil)
		return
	}
	for _, name := range w.names {
		obj, _, _ := types.LookupFieldOrMethod(t, true, nil, name)
		if fn, ok := obj.(*types.Func); ok {
			w.found(fn)
		}
	}
}

// walk looks through t for values the codec calls into. fresh is true
// when the value is a variable still at its zero value, whose interfaces
// are nil: a decoder fills them with plain values and calls nothing on
// them.
func (w *typeWalk) walk(t types.Type, fresh bool) {
	if w.open != "" || w.seen[t] {
		return
	}
	w.seen[t] = true
	if _, ok := t.(*types.TypeParam); ok {
		w.open = "a type parameter"
		return
	}
	if types.IsInterface(t) {
		if !fresh {
			w.open = "a value of interface type " + types.TypeString(t, nil)
		}
		return
	}
	if _, ok := t.(*types.Pointer); !ok {
		if fn := w.method(t); fn != nil {
			w.found(fn)
			return
		}
	}
	switch x := t.Underlying().(type) {
	case *types.Pointer:
		if fn := w.method(x); fn != nil {
			w.found(fn)
			return
		}
		w.walk(x.Elem(), fresh)
	case *types.Slice:
		w.walk(x.Elem(), fresh)
	case *types.Array:
		w.walk(x.Elem(), fresh)
	case *types.Map:
		w.walk(x.Key(), fresh)
		w.walk(x.Elem(), fresh)
	case *types.Struct:
		for i := 0; i < x.NumFields(); i++ {
			f := x.Field(i)
			if !f.Exported() && !f.Embedded() {
				continue
			}
			if w.tag != "" && reflect.StructTag(x.Tag(i)).Get(w.tag) == "-" {
				continue
			}
			w.walk(f.Type(), fresh)
		}
	}
}

// inModule reports whether path is module or one of its packages.
func inModule(module, path string) bool {
	return module != "" && (path == module || strings.HasPrefix(path, module+"/"))
}

// indexZeroVars records u's local variables declared without a value and
// every position each is used at.
func (u *unit) indexZeroVars() {
	u.zeroVars = map[*types.Var]bool{}
	u.uses = map[*types.Var][]token.Pos{}
	for _, f := range u.files {
		ast.Inspect(f, func(n ast.Node) bool {
			if vs, ok := n.(*ast.ValueSpec); ok && len(vs.Values) == 0 {
				for _, name := range vs.Names {
					if v, ok := u.info.Defs[name].(*types.Var); ok && !v.IsField() && v.Parent() != v.Pkg().Scope() {
						u.zeroVars[v] = true
					}
				}
			}
			return true
		})
	}
	for id, obj := range u.info.Uses {
		if v, ok := obj.(*types.Var); ok && u.zeroVars[v] {
			u.uses[v] = append(u.uses[v], id.Pos())
		}
	}
}

// fresh reports whether arg is &v for a local v declared without a value
// in body and not used before the call at pos: a decode into it finds
// only nil interfaces and pointers.
func (u *unit) fresh(arg ast.Expr, pos token.Pos, body *ast.BlockStmt) bool {
	ue, ok := ast.Unparen(arg).(*ast.UnaryExpr)
	if !ok || ue.Op != token.AND || body == nil {
		return false
	}
	id, ok := ast.Unparen(ue.X).(*ast.Ident)
	if !ok {
		return false
	}
	v, ok := u.info.Uses[id].(*types.Var)
	if !ok || !u.zeroVars[v] || v.Pos() < body.Pos() || v.Pos() > body.End() {
		return false
	}
	for _, p := range u.uses[v] {
		if p < pos {
			return false
		}
	}
	return true
}
