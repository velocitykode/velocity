// check-unconditional-skip walks every *_test.go file in the repo and
// reports t.Skip(...) calls that are not guarded by a preceding
// conditional in the enclosing function. Replaces the earlier 8-line
// sed-window shell script, which misfired under refactor — a guard `if`
// nine lines above a skip read as unguarded, and an unrelated `if` seven
// lines above an unrelated skip read as guarded.
//
// Guard rule (enforced via go/ast):
//
//  1. The call is lexically inside an *ast.IfStmt, *ast.SwitchStmt,
//     *ast.TypeSwitchStmt, or *ast.SelectStmt body within the function
//     (including nested blocks / closures). OR
//  2. At least one of those statements appears earlier in the enclosing
//     function body (between the function's opening brace and the skip
//     call). This matches "check env/short/table-field, then skip".
//
// Anything that doesn't satisfy either is a "naked" skip — the class of
// regression Phase 1.1 eliminated.
//
// Usage: go run ./scripts/ci/check-unconditional-skip [root]
// Exits 0 and prints nothing on clean. Exits 0 with non-empty stdout on
// findings (the CI step treats any output as failure).
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

	var findings []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip vendor, .git, and the tool's own directory.
			name := d.Name()
			if name == "vendor" || name == ".git" || name == "node_modules" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fs, err := scan(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		findings = append(findings, fs...)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan failed:", err)
		os.Exit(2)
	}

	sort.Strings(findings)
	for _, f := range findings {
		fmt.Println(f)
	}
}

// scan parses a single _test.go file and returns one string per unguarded
// t.Skip call: "path:line:col: <source>".
func scan(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var findings []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		walkScope(fset, fn.Body, &findings)
	}
	return findings, nil
}

// walkScope evaluates `scope` as the enclosing function body for any
// t.Skip calls found directly within. When the walk encounters a nested
// *ast.FuncLit (subtest closure, t.Cleanup callback, goroutine), it
// recurses with the closure's own body as the new scope — otherwise a
// guard in the outer scope would spuriously satisfy a bare skip inside
// the inner closure.
func walkScope(fset *token.FileSet, scope *ast.BlockStmt, out *[]string) {
	ast.Inspect(scope, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok && lit.Body != nil {
			walkScope(fset, lit.Body, out)
			return false // skip inner ast.Inspect — walkScope handled it
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isSkipCall(call) {
			return true
		}
		if guarded(scope, call) {
			return true
		}
		pos := fset.Position(call.Pos())
		snippet := renderSnippet(fset, call)
		*out = append(*out, fmt.Sprintf("%s:%d:%d: %s", pos.Filename, pos.Line, pos.Column, snippet))
		return true
	})
}

// isSkipCall reports whether `call` is a testing.TB.Skip / Skipf / SkipNow
// call — i.e. a real test-skip, not `c.Skip(n)` on some domain type that
// happens to share the method name. We require the receiver identifier to
// be a conventional testing.TB name (`t`, `b`, `tb`). Non-conventional
// receiver names are vanishingly rare and would have to be added here.
func isSkipCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Skip", "Skipf", "SkipNow":
	default:
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	switch recv.Name {
	case "t", "b", "tb":
		return true
	}
	return false
}

// guarded reports whether the skip call is "guarded" per the rule in the
// package comment: either inside a conditional body, or preceded by one in
// the enclosing function.
func guarded(fnBody *ast.BlockStmt, call *ast.CallExpr) bool {
	// Rule 1: lexically inside a conditional. Walk the function body and
	// see if any IfStmt / SwitchStmt / SelectStmt / TypeSwitchStmt contains
	// this call position.
	skipPos := call.Pos()
	var insideCond bool
	ast.Inspect(fnBody, func(n ast.Node) bool {
		if insideCond {
			return false
		}
		switch n.(type) {
		case *ast.IfStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			if n.Pos() < skipPos && skipPos < n.End() {
				insideCond = true
				return false
			}
		}
		return true
	})
	if insideCond {
		return true
	}

	// Rule 2: a conditional statement appears earlier in the same function
	// body (not necessarily containing the skip). Matches:
	//   if os.Getenv("X") == "" { ... }
	//   t.Skip("needs X")
	for _, stmt := range fnBody.List {
		if stmt.End() <= skipPos {
			switch stmt.(type) {
			case *ast.IfStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
				return true
			}
			// assignments like `if err := ...; err != nil { ... }` are
			// IfStmt and covered above. A naked `if` at any earlier
			// position in the same block counts as a guard.
		}
	}
	return false
}

// renderSnippet returns a best-effort one-line rendering of the skip call
// for the diagnostic. We read the source file directly rather than using
// printer because we only need the call expression, not its args.
func renderSnippet(fset *token.FileSet, call *ast.CallExpr) string {
	pos := fset.Position(call.Pos())
	data, err := os.ReadFile(pos.Filename)
	if err != nil {
		return "t.Skip(...)"
	}
	lines := strings.Split(string(data), "\n")
	if pos.Line-1 < 0 || pos.Line-1 >= len(lines) {
		return "t.Skip(...)"
	}
	return strings.TrimSpace(lines[pos.Line-1])
}
