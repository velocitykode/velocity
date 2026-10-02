// check-events reports framework event code that breaks one of four rules.
//
// Rule "field": an event field whose type does not survive the queue
// codec. A queued listener receives its event after a JSON round trip: the
// producer marshals the event, the worker unmarshals it into a fresh value
// of the registered type. A field typed as an interface (any, []any,
// map[string]any, an interface of the module, a type parameter) comes back
// as whatever JSON decodes into it (a map, a string, a float64), so an
// async listener sees a different value than a sync listener saw for the
// same event. Event fields are metadata with concrete types: a string ID,
// a formatted message, a type label.
//
// An event is an exported named struct type of the module whose value or
// pointer has a Name() string method (contract.Event) or that embeds
// contract.EventMeta, directly or through an embedded struct. Its fields
// are walked the way encoding/json walks them: exported fields, json:"-"
// skipped, embedded structs, pointers, slices, arrays, map keys and
// elements. A named type that declares its own MarshalJSON and
// UnmarshalJSON (or MarshalText and UnmarshalText) is a leaf: it owns its
// codec. A field is reported when its type holds an interface type other
// than error, a type parameter, a func, a channel or an unsafe pointer, or
// error when the event does not declare its own MarshalJSON and
// UnmarshalJSON (the framework's error codec: the text crosses as a string
// and decodes back to an error with that text).
//
// Rule "envelope": an event the framework builds that does not embed
// contract.EventMeta. Every framework event carries the envelope (the
// context it was dispatched under, its trace, span and parent span ids, and
// when it happened), and code that handles framework events reads it one
// way (Meta()); the queued listener path carries it across the codec. An
// event is reported when it has no envelope and some non-test package of
// the module builds a value of it: a composite literal of its type (or its
// address), or new of it. A literal that fills an embedded field of an
// enclosing literal builds part of that value, not an event of its own, so
// a type the framework only offers for applications to embed (a base event
// with a Name method) is not reported.
//
// Rule "emitter": a failure an eventemit.Emitter records on a path of its
// own (Fail or FailLater) while the emitter never shares the app's
// Failures. In an app every failed event dispatch counts in one counter,
// the app's (App.FailedEventCount): a failure the app's dispatch function
// returns is recorded there already, but one a component originates itself
// (a drop, a panic in a dispatcher of its own) is recorded in the Failures
// its emitter holds, which is its own unless the framework hands it the
// app's through Share or SetShared. A Fail or FailLater call is reported
// when no Share or SetShared call on the same emitter (the same struct
// field, or the same variable) with a Failures other than the literal nil
// appears in the package, unless the error it
// records is the result of eventemit.DispatchContained running the same
// emitter's own Dispatcher, read into a variable or called in place: such
// a failure comes from the app's dispatch function in an app, which
// recorded it. The rule proves the component can take the app's Failures;
// the tests of the framework's wiring prove it hands them over.
//
// Rule "contain": a call into a registered callback of the events or orm
// packages (a model observer, a listener, a statement observer) that no
// recover contains; contain.go documents the proof.
//
// Scope: the non-test files of the packages the patterns name, except test
// infrastructure (a directory whose name ends in "test" or is "testing",
// internal/hostile, scripts/). There is no suppression marker: an event
// field that cannot cross the codec, or a failure that misses the app's
// counter, is changed, never excused.
//
// Type information comes from `go list -export` and the standard library
// importer, so the tool needs no dependency outside the standard library.
//
// Usage: go run ./scripts/ci/check-events [-events|-callbacks] [packages]
// Prints "file:line: rule: message" per offender, then on stderr how to fix
// each rule reported, and exits 1 when there is any; prints nothing and
// exits 0 otherwise. -events prints every event type found instead (for
// inventories); -callbacks prints every registered-callback call of the
// events and orm packages with whether it is contained.
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
	"sort"
	"strings"
)

const (
	ruleField    = "field"
	ruleEmitter  = "emitter"
	ruleEnvelope = "envelope"
)

var fixes = []struct{ rule, fix string }{
	{ruleField, "field: carry metadata with a concrete type (a string ID, a formatted message, a type label); an error field needs the event's MarshalJSON/UnmarshalJSON pair (eventmeta.ErrorText / eventmeta.TextError)"},
	{ruleEnvelope, "envelope: embed contract.EventMeta in the event and fill it from the context the event is built under (eventmeta.Current, or eventmeta.Child for an operation that runs as a span of its own)"},
	{ruleContain, "contain: call the registered callback inside the package's containment helper, the one function that defers a recover and returns panicerr.FromRecovered as that callback's error"},
	{ruleEmitter, "emitter: have the framework hand the emitter the app's Failures (Share, or SetShared for a process-wide emitter) where it wires the component's dispatcher"},
}

func main() {
	list := flag.Bool("events", false, "print every event type found")
	cbs := flag.Bool("callbacks", false, "print every registered-callback call in events and orm, contained or not")
	flag.Parse()
	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	r, err := check(".", patterns)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-events:", err)
		os.Exit(2)
	}
	if *cbs {
		for _, e := range r.callbacks {
			fmt.Println(e)
		}
		return
	}
	if *list {
		for _, e := range r.events {
			fmt.Println(e)
		}
		return
	}
	for _, h := range r.hits {
		fmt.Println(h)
	}
	if len(r.hits) > 0 {
		fmt.Fprintf(os.Stderr, "%d event rule violation(s).\n", len(r.hits))
		for _, f := range fixes {
			for _, h := range r.hits {
				if strings.Contains(h, ": "+f.rule+": ") {
					fmt.Fprintln(os.Stderr, f.fix)
					break
				}
			}
		}
		os.Exit(1)
	}
}

type listedPackage struct {
	Dir             string
	ImportPath      string
	Export          string
	CompiledGoFiles []string
	DepOnly         bool
	Module          *struct{ Path, Dir string }
	Error           *struct{ Err string }
}

// result is what check found: the offenders, the event types and the
// registered-callback calls, sorted.
type result struct {
	hits      []string
	events    []string
	callbacks []string
}

// checker holds what both rules share.
type checker struct {
	fset   *token.FileSet
	root   string
	module string
	result
	// bare holds the events without the envelope, built the event types
	// some package builds, both by package path and type name.
	bare  map[string]bareEvent
	built map[string]bool
}

// unit is one type-checked package.
type unit struct {
	pkg   *types.Package
	files []*ast.File
	info  *types.Info
}

func check(dir string, patterns []string) (result, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return result{}, err
	}
	modOut, err := goCmd(absDir, "list", "-m", "-json")
	if err != nil {
		return result{}, err
	}
	var mod struct{ Path, Dir string }
	if err := json.Unmarshal(modOut, &mod); err != nil {
		return result{}, fmt.Errorf("go list -m: %w", err)
	}
	listOut, err := goCmd(absDir, append([]string{"list", "-e", "-export", "-compiled", "-deps", "-json"}, patterns...)...)
	if err != nil {
		return result{}, err
	}
	exports := map[string]string{}
	var targets []listedPackage
	dec := json.NewDecoder(bytes.NewReader(listOut))
	for {
		var p listedPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return result{}, fmt.Errorf("go list: %w", err)
		}
		if p.Error != nil {
			return result{}, fmt.Errorf("go list %s: %s", p.ImportPath, p.Error.Err)
		}
		exports[p.ImportPath] = p.Export
		if !p.DepOnly && p.Module != nil && p.Module.Path == mod.Path && !excluded(mod.Path, p.ImportPath) {
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
	c := &checker{fset: fset, root: mod.Dir, module: mod.Path}
	for _, p := range targets {
		u := &unit{info: &types.Info{
			Types:      map[ast.Expr]types.TypeAndValue{},
			Uses:       map[*ast.Ident]types.Object{},
			Defs:       map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
		}}
		for _, f := range p.CompiledGoFiles {
			if !filepath.IsAbs(f) {
				f = filepath.Join(p.Dir, f)
			}
			if strings.HasSuffix(f, "_test.go") || !strings.HasSuffix(f, ".go") {
				continue
			}
			af, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				return result{}, err
			}
			u.files = append(u.files, af)
		}
		var typeErr error
		conf := types.Config{Importer: imp, Error: func(err error) {
			if typeErr == nil {
				typeErr = err
			}
		}}
		pkg, err := conf.Check(p.ImportPath, fset, u.files, u.info)
		if err != nil && typeErr == nil {
			typeErr = err
		}
		if typeErr != nil {
			return result{}, fmt.Errorf("type-check %s: %w", p.ImportPath, typeErr)
		}
		u.pkg = pkg
		c.fields(u)
		c.emitters(u)
		c.envelopes(u)
		c.contains(u)
	}
	c.reportEnvelopes()
	sort.Strings(c.hits)
	sort.Strings(c.events)
	return c.result, nil
}

func (c *checker) report(pos token.Pos, rule, msg string) {
	c.hits = append(c.hits, fmt.Sprintf("%s: %s: %s", c.pos(pos), rule, msg))
}

func (c *checker) pos(p token.Pos) string {
	pos := c.fset.Position(p)
	if rel, err := filepath.Rel(c.root, pos.Filename); err == nil {
		return fmt.Sprintf("%s:%d", filepath.ToSlash(rel), pos.Line)
	}
	return fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
}

func excluded(module, path string) bool {
	rel := strings.TrimPrefix(strings.TrimPrefix(path, module), "/")
	if rel == "internal/hostile" || strings.HasPrefix(rel, "internal/hostile/") ||
		rel == "scripts" || strings.HasPrefix(rel, "scripts/") {
		return true
	}
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasSuffix(seg, "test") || seg == "testing" {
			return true
		}
	}
	return false
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
