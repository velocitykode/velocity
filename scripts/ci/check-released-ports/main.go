// check-released-ports reports a test that takes a port from a listener
// and releases it before the code under test binds it.
//
// Such a test listens on port 0, reads the port the kernel picked, closes
// the listener and hands the port number (or the address) to the code
// under test, which binds it again. Between the close and the bind any
// other process on the machine can take the port, and the test fails with
// "bind: address already in use" (or a dial reaches a stranger). The fix
// is to keep the listener and hand the listener itself over: a server
// that serves on a caller's listener (grpc.WithListener,
// grpc.GatewayWithListener, velocity.WithListener) never lets the port go.
//
// Rule (go/ast, per function including its closures): a variable is
// reported when every use of it is a call of its Addr or Close method,
// it reads Addr at least once, and it calls Close at least once in place
// (not deferred, not inside a function literal such as a t.Cleanup). A
// listener used only for its address and closed in place is a port
// source, never a server. A listener held open for the whole test (a
// deferred or Cleanup close) to make a bind fail is not reported, nor is
// one handed to other code.
//
// Scope: every *_test.go file under the root, except testdata, vendor
// and .git. There is no suppression marker.
//
// Usage: go run ./scripts/ci/check-released-ports [root]
// Prints "file:line: message" per offender and exits 1 when there is any;
// prints nothing and exits 0 otherwise.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	hits, err := check(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-released-ports:", err)
		os.Exit(2)
	}
	for _, h := range hits {
		fmt.Println(h)
	}
	if len(hits) > 0 {
		fmt.Fprintf(os.Stderr, "%d released port(s): keep the listener and hand it to the server (grpc.WithListener, grpc.GatewayWithListener, velocity.WithListener) instead of its port.\n", len(hits))
		os.Exit(1)
	}
}

// check returns the offenders under root, sorted.
func check(root string) ([]string, error) {
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git", "node_modules", "testdata":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		h, err := scan(root, path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		hits = append(hits, h...)
		return nil
	})
	sort.Strings(hits)
	return hits, err
}

// use tallies how a variable is used within one function.
type use struct {
	decl      token.Pos
	name      string
	fn        ast.Node // the function body that declares it
	addr      int
	closeHere int
	other     int
}

// scan reports the released ports of one file.
func scan(root, path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	var hits []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		uses := map[*ast.Object]*use{}
		tally(fn.Body, fn.Body, uses, false)
		for _, u := range uses {
			if u.addr > 0 && u.closeHere > 0 && u.other == 0 {
				hits = append(hits, fmt.Sprintf("%s:%d: %s is read for its address and closed: its port is released before the code under test binds it",
					filepath.ToSlash(rel), fset.Position(u.decl).Line, u.name))
			}
		}
	}
	return hits, nil
}

// tally walks n, which runs in the function body fn, counting each local
// variable's uses into uses. deferred says n runs at the end of fn: inside
// a defer. A Close is in place when it runs where it stands in the body of
// the function that declares the variable; a function literal's body
// (a t.Cleanup, a goroutine) runs at a time of its own.
func tally(n, fn ast.Node, uses map[*ast.Object]*use, deferred bool) {
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.DeferStmt:
			tally(n.Call, fn, uses, true)
			return false
		case *ast.FuncLit:
			tally(n.Body, n.Body, uses, false)
			return false
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || len(n.Args) != 0 {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			u := local(id, fn, uses)
			if u == nil {
				return true
			}
			switch sel.Sel.Name {
			case "Addr":
				u.addr++
			case "Close":
				if !deferred && fn == u.fn {
					u.closeHere++
				}
			default:
				u.other++
			}
			return false
		case *ast.Ident:
			if u := local(n, fn, uses); u != nil && n.Obj.Decl != nil && n.NamePos != u.decl {
				u.other++
			}
		}
		return true
	})
}

// local returns the tally of the variable id refers to, creating it at
// the variable's declaration in the function body fn, or nil when id is
// not a local variable.
func local(id *ast.Ident, fn ast.Node, uses map[*ast.Object]*use) *use {
	obj := id.Obj
	if obj == nil || obj.Kind != ast.Var || id.Name == "_" {
		return nil
	}
	u, ok := uses[obj]
	if !ok {
		pos := token.NoPos
		switch d := obj.Decl.(type) {
		case *ast.AssignStmt:
			for _, l := range d.Lhs {
				if lid, ok := l.(*ast.Ident); ok && lid.Name == id.Name {
					pos = lid.NamePos
				}
			}
		case *ast.ValueSpec:
			for _, nm := range d.Names {
				if nm.Name == id.Name {
					pos = nm.NamePos
				}
			}
		default:
			// A parameter, a range variable or a result: handed in, not
			// a listener the function opened.
			return nil
		}
		if pos == token.NoPos {
			return nil
		}
		u = &use{decl: pos, name: id.Name, fn: fn}
		uses[obj] = u
	}
	return u
}
