// check-error-inspection reports framework code that inspects an error it
// did not make itself outside internal/errchain. An error's Error, Unwrap,
// Is and As methods are user code when a handler, job, driver, store,
// listener or callback returned the error: they may panic, and a chain may
// loop back on itself. errors.Is and errors.As follow such a chain without
// bound and let a panic through, so the goroutine inspecting the error
// crashes or hangs, often cleanup that must finish. internal/errchain
// holds the bounded, contained forms: Is, As, Unwrap, Text, and Walk with
// Matches and MatchesAs for a classification of its own, and Errorf,
// Sprintf and Sprint for formatting a value whose type is not known.
//
// Calls flagged, told apart by type only:
//
//   - is, as, unwrap: a call to errors.Is, errors.As or errors.Unwrap;
//
//   - text, unwrap, is, as: a method call on a value whose static type is
//     an interface, when the method is Error() string, Unwrap() error,
//     Unwrap() []error, Is(error) bool or As(any) bool (matched by
//     signature, so a logger's Error(msg, kvs...) is not a hit, and a hand
//     walk's x.Unwrap() after err.(interface{ Unwrap() error }) is);
//
//   - format: a call to a fmt print function (Errorf, Sprintf, Sprint,
//     Fprintf, Appendf and the rest) with an operand whose static type is
//     an interface (error, any, fmt.Stringer, a type parameter), or a
//     spread slice of them. fmt recovers a panic in the operand's Error,
//     String or Format method once, but it formats the panic value too,
//     and re-panics when that panics: a writer or buffer argument and
//     the format string are not operands.
//
//   - nil: a comparison with nil (== or !=) of a parameter of an exported
//     function or method, in the function's body or a function literal
//     inside it, when the parameter's type is an interface this module
//     declares (through an alias too, and an alias of an interface with
//     no name of its own). The caller is user code, and a
//     typed nil (a nil *T held in the interface) is not equal to nil: the
//     check lets it through and a later call runs a method on a nil
//     receiver. internal/nilval.Is answers for both. An exported method
//     counts whatever its receiver's type is named: an unexported type
//     reaches the caller through a constructor or an interface. Not
//     flagged: the
//     error type (a non-nil error value is an error, whatever it holds),
//     context.Context and other interfaces declared outside the module,
//     the empty interface, a type parameter, and a comparison of anything
//     but the parameter itself (a field or local it was stored in: the
//     entry point is where a typed nil is made a plain nil).
//
// Every error-typed (and, for format, interface-typed) value is treated
// as possibly user-made: types cannot tell a framework sentinel from a
// user error wrapping one, and a stdlib error (a json decode, an io.Copy)
// can wrap one too.
//
// Known limits: a call on a concrete type is not flagged (its method is
// the module's own or a dependency's). Other methods of a user error (a
// status code, headers, a client message, GRPCStatus) are not flagged: an
// interface method call cannot be told from any other by type; the
// classification sites read them inside the same contained walk. A fmt
// operand of concrete type is not flagged, though fmt may reach a user
// value through its fields. Logger key-value pairs are not flagged: the
// framework's log drivers format every value with errchain.Sprint (their
// fmt calls fall under the format rule), and a user logger's own
// formatting is contained by fallbacklog.
//
// Suppression: a same-line `//error-inspection-ok: <rationale>` comment,
// the rationale at least 5 characters. A bare marker does not suppress,
// and the hit on its line says so. A marker that suppresses nothing is
// stale and is reported.
//
// Scope: the non-test files of the packages the patterns name, except
// internal/errchain (the one place allowed to make these calls) and test
// infrastructure, excluded by directory as check-lock-held-calls does: any
// directory whose name ends in "test" or is "testing", internal/hostile,
// and scripts/.
//
// Type information comes from `go list -export` and the standard library
// importer, so the tool needs no dependency outside the standard library.
//
// Usage: go run ./scripts/ci/check-error-inspection [-report-only] [packages]
// Prints "file:line: kind: call" per offender, then on stderr the fix for
// each kind reported and the marker syntax, and exits 1 when there is any;
// prints nothing and exits 0 otherwise. -report-only prints the same and
// exits 0.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func main() {
	reportOnly := flag.Bool("report-only", false, "print the offenders and exit 0")
	flag.Parse()
	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	hits, err := check(".", patterns)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-error-inspection:", err)
		os.Exit(2)
	}
	for _, h := range hits {
		fmt.Println(h)
	}
	if len(hits) > 0 {
		fmt.Fprint(os.Stderr, hints(hits))
		if !*reportOnly {
			os.Exit(1)
		}
	}
}

const (
	kindIs     = "is"
	kindAs     = "as"
	kindUnwrap = "unwrap"
	kindText   = "text"
	kindStale  = "stale"
	kindFormat = "format"
	kindNil    = "nil"
)

var fixes = []struct{ kind, fix string }{
	{kindIs, "is: errchain.Is(err, target), or errchain.Matches inside an errchain.Walk visit"},
	{kindAs, "as: errchain.As[T](err), or errchain.MatchesAs[T] inside an errchain.Walk visit"},
	{kindUnwrap, "unwrap: errchain.Unwrap(err), or errchain.Walk for a walk of the chain"},
	{kindText, "text: errchain.Text(err)"},
	{kindFormat, "format: errchain.Errorf, errchain.Sprintf or errchain.Sprint(v) in place of the fmt call"},
	{kindNil, "nil: nilval.Is(v) in place of v == nil (and !nilval.Is(v) for v != nil): a typed nil is nil too"},
	{kindStale, "stale: remove the //error-inspection-ok: marker; nothing on its line inspects an error any more"},
}

// hints returns the fix lines for the kinds in hits, and the marker
// syntax.
func hints(hits []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d uncontained inspection(s) of a value user code supplied. An error's or value's Error, String, Format, Unwrap, Is and As methods can panic, and its chain can loop; a typed nil passes a comparison with nil.\n", len(hits))
	for _, f := range fixes {
		for _, h := range hits {
			if strings.Contains(h, ": "+f.kind+": ") {
				b.WriteString("  " + f.fix + "\n")
				break
			}
		}
	}
	b.WriteString("  an inspection that is safe: same-line //error-inspection-ok: <rationale of at least 5 characters>\n")
	return b.String()
}

type listedPackage struct {
	ImportPath      string
	Dir             string
	Export          string
	CompiledGoFiles []string
	DepOnly         bool
	Module          *struct{ Path, Dir string }
	Error           *struct{ Err string }
}

func goCmd(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return out, nil
}

// excluded reports whether a package is out of scope (see the package
// comment).
func excluded(module, path string) bool {
	rel := strings.TrimPrefix(strings.TrimPrefix(path, module), "/")
	for _, dir := range []string{"internal/errchain", "internal/hostile", "scripts"} {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasSuffix(seg, "test") || seg == "testing" {
			return true
		}
	}
	return false
}

const markerPrefix = "//error-inspection-ok:"

// markerRE matches a marker with a rationale of at least 5 characters.
var markerRE = regexp.MustCompile(`//error-inspection-ok: *\S.{3,}\S`)

var (
	errorType   = types.Universe.Lookup("error").Type()
	errorsSlice = types.NewSlice(errorType)
	emptyIface  = types.NewInterfaceType(nil, nil).Complete()
)

// check type-checks the module packages patterns name, run in dir, and
// returns the offenders, sorted.
func check(dir string, patterns []string) ([]string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	modOut, err := goCmd(absDir, "list", "-m", "-json")
	if err != nil {
		return nil, err
	}
	var mod struct{ Path, Dir string }
	if err := json.Unmarshal(modOut, &mod); err != nil {
		return nil, fmt.Errorf("go list -m: %w", err)
	}
	listOut, err := goCmd(absDir, append([]string{"list", "-e", "-export", "-compiled", "-deps", "-json"}, patterns...)...)
	if err != nil {
		return nil, err
	}
	exports := map[string]string{}
	var targets []listedPackage
	dec := json.NewDecoder(bytes.NewReader(listOut))
	for {
		var p listedPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list: %w", err)
		}
		if p.Error != nil {
			return nil, fmt.Errorf("go list %s: %s", p.ImportPath, p.Error.Err)
		}
		exports[p.ImportPath] = p.Export
		if p.Module != nil && p.Module.Path == mod.Path && !p.DepOnly && !excluded(mod.Path, p.ImportPath) {
			targets = append(targets, p)
		}
	}

	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		f := exports[path]
		if f == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(f)
	})
	hits := map[string]bool{}
	for _, p := range targets {
		var files []*ast.File
		for _, f := range p.CompiledGoFiles {
			if !filepath.IsAbs(f) {
				f = filepath.Join(p.Dir, f)
			}
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			af, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
			if err != nil {
				return nil, err
			}
			files = append(files, af)
		}
		info := &types.Info{
			Types:      map[ast.Expr]types.TypeAndValue{},
			Defs:       map[*ast.Ident]types.Object{},
			Uses:       map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
		}
		var typeErr error
		conf := types.Config{Importer: imp, Error: func(err error) {
			if typeErr == nil {
				typeErr = err
			}
		}}
		if _, err := conf.Check(p.ImportPath, fset, files, info); err != nil && typeErr == nil {
			typeErr = err
		}
		if typeErr != nil {
			return nil, fmt.Errorf("type-check %s: %w", p.ImportPath, typeErr)
		}
		for _, f := range files {
			checkFile(fset, mod.Dir, mod.Path, info, f, hits)
		}
	}
	out := make([]string, 0, len(hits))
	for h := range hits {
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// checkFile adds f's offenders and stale markers to hits.
func checkFile(fset *token.FileSet, root, module string, info *types.Info, f *ast.File, hits map[string]bool) {
	markers := map[int]string{} // line -> comment text
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, markerPrefix) {
				markers[fset.Position(c.Pos()).Line] = c.Text
			}
		}
	}
	rel := func(file string) string {
		r, err := filepath.Rel(root, file)
		if err != nil {
			r = file
		}
		return filepath.ToSlash(r)
	}
	used := map[int]bool{}
	// report records one offender at at, unless a marker with a rationale
	// is on its line.
	report := func(at token.Pos, kind, what string) {
		pos := fset.Position(at)
		if m, ok := markers[pos.Line]; ok {
			used[pos.Line] = true
			if markerRE.MatchString(m) {
				return
			}
			hits[fmt.Sprintf("%s:%d: %s: %s (the marker on this line has no rationale)", rel(pos.Filename), pos.Line, kind, what)] = true
			return
		}
		hits[fmt.Sprintf("%s:%d: %s: %s", rel(pos.Filename), pos.Line, kind, what)] = true
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if kind := classify(info, call); kind != "" {
			report(call.Pos(), kind, types.ExprString(call.Fun))
		}
		return true
	})
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || !exportedFunc(fd) {
			continue
		}
		params := boundaryParams(info, module, fd)
		if len(params) == 0 {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			be, ok := n.(*ast.BinaryExpr)
			if ok && nilComparison(info, be, params) {
				report(be.Pos(), kindNil, types.ExprString(be))
			}
			return true
		})
	}
	for line := range markers {
		if !used[line] {
			hits[fmt.Sprintf("%s:%d: %s: the //error-inspection-ok: marker suppresses no inspection", rel(fset.File(f.Pos()).Name()), line, kindStale)] = true
		}
	}
}

// exportedFunc reports whether fd is callable from outside its package:
// an exported function, or an exported method whatever its receiver's
// type is named. An unexported type's exported methods are reachable too:
// an exported constructor returns the value, or it satisfies an interface
// the caller holds.
func exportedFunc(fd *ast.FuncDecl) bool {
	return fd.Name.IsExported()
}

// boundaryParams returns the parameters of fd whose type is an interface
// the module declares, other than error.
func boundaryParams(info *types.Info, module string, fd *ast.FuncDecl) map[types.Object]bool {
	var params map[types.Object]bool
	for _, field := range fd.Type.Params.List {
		for _, name := range field.Names {
			obj := info.Defs[name]
			if obj == nil || !moduleInterface(module, obj.Type()) {
				continue
			}
			if params == nil {
				params = map[types.Object]bool{}
			}
			params[obj] = true
		}
	}
	return params
}

// moduleInterface reports whether t is an interface type declared in a
// package of module: a named interface (seen through aliases), or a
// module-declared alias of a non-empty interface that has no name of its
// own (type Store = interface{ Get() int }).
func moduleInterface(module string, t types.Type) bool {
	inModule := func(pkg *types.Package) bool {
		return pkg != nil && (pkg.Path() == module || strings.HasPrefix(pkg.Path(), module+"/"))
	}
	aliased := false
	for {
		alias, ok := t.(*types.Alias)
		if !ok {
			break
		}
		aliased = aliased || inModule(alias.Obj().Pkg())
		t = alias.Rhs()
	}
	switch x := t.(type) {
	case *types.Named:
		return types.IsInterface(x) && inModule(x.Obj().Pkg())
	case *types.Interface:
		return aliased && !x.Empty()
	}
	return false
}

// nilComparison reports whether be compares one of params with nil.
func nilComparison(info *types.Info, be *ast.BinaryExpr, params map[types.Object]bool) bool {
	if be.Op != token.EQL && be.Op != token.NEQ {
		return false
	}
	operand := be.X
	if tv, ok := info.Types[be.X]; ok && tv.IsNil() {
		operand = be.Y
	} else if tv, ok := info.Types[be.Y]; !ok || !tv.IsNil() {
		return false
	}
	id, ok := ast.Unparen(operand).(*ast.Ident)
	return ok && params[info.Uses[id]]
}

// classify returns the kind of an inspection call, or "".
func classify(info *types.Info, call *ast.CallExpr) string {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if fn, ok := info.Uses[sel.Sel].(*types.Func); ok && fn.Pkg() != nil && fn.Pkg().Path() == "fmt" && fn.Type().(*types.Signature).Recv() == nil {
		if formatsInterface(info, call, fn.Name()) {
			return kindFormat
		}
		return ""
	}
	if fn, ok := info.Uses[sel.Sel].(*types.Func); ok && fn.Pkg() != nil && fn.Pkg().Path() == "errors" && fn.Type().(*types.Signature).Recv() == nil {
		switch fn.Name() {
		case "Is":
			return kindIs
		case "As":
			return kindAs
		case "Unwrap":
			return kindUnwrap
		}
		return ""
	}
	s := info.Selections[sel]
	if s == nil || s.Kind() != types.MethodVal || !types.IsInterface(s.Recv()) {
		return ""
	}
	fn, ok := s.Obj().(*types.Func)
	if !ok {
		return ""
	}
	return errorMethod(fn)
}

// errorMethod returns the kind of fn when it is one of the error methods
// by name and signature, or "".
func errorMethod(fn *types.Func) string {
	sig := fn.Type().(*types.Signature)
	p, r := sig.Params(), sig.Results()
	if r.Len() != 1 || sig.Variadic() {
		return ""
	}
	res := r.At(0).Type()
	switch fn.Name() {
	case "Error":
		if p.Len() == 0 && types.Identical(res, types.Typ[types.String]) {
			return kindText
		}
	case "Unwrap":
		if p.Len() == 0 && (types.Identical(res, errorType) || types.Identical(res, errorsSlice)) {
			return kindUnwrap
		}
	case "Is":
		if p.Len() == 1 && types.Identical(p.At(0).Type(), errorType) && types.Identical(res, types.Typ[types.Bool]) {
			return kindIs
		}
	case "As":
		if p.Len() == 1 && types.Identical(p.At(0).Type(), emptyIface) && types.Identical(res, types.Typ[types.Bool]) {
			return kindAs
		}
	}
	return ""
}

// fmtOperands maps a fmt print function to the index of its first
// operand: the arguments before it are a writer, a buffer or the format.
var fmtOperands = map[string]int{
	"Errorf": 1, "Sprintf": 1, "Printf": 1, "Fprintf": 2, "Appendf": 2,
	"Sprint": 0, "Print": 0, "Fprint": 1, "Append": 1,
	"Sprintln": 0, "Println": 0, "Fprintln": 1, "Appendln": 1,
}

// formatsInterface reports whether a call to the fmt function name hands
// fmt an operand of interface type: a value whose dynamic type, and so
// whose Error, String, Format or GoString method, the call site does not
// know.
func formatsInterface(info *types.Info, call *ast.CallExpr, name string) bool {
	first, ok := fmtOperands[name]
	if !ok {
		return false
	}
	for i := first; i < len(call.Args); i++ {
		if interfaceOperand(info, call, i) {
			return true
		}
	}
	return false
}

// interfaceOperand reports whether argument i of call is a value of
// interface type, or a spread slice of them.
func interfaceOperand(info *types.Info, call *ast.CallExpr, i int) bool {
	tv, ok := info.Types[call.Args[i]]
	if !ok || tv.IsNil() || tv.Type == nil {
		return false
	}
	t := tv.Type
	if call.Ellipsis.IsValid() && i == len(call.Args)-1 {
		s, ok := t.Underlying().(*types.Slice)
		if !ok {
			return false
		}
		t = s.Elem()
	}
	return types.IsInterface(t)
}
