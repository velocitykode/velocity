// check-alloc-averages reports every use of testing.AllocsPerRun outside a
// benchmark function.
//
// testing.AllocsPerRun reads the process-wide malloc count, so a test that
// compares its result (to zero, to a budget, or to a second average) fails
// whenever another goroutine allocates during the run: a parallel test, a
// timer, a background worker, the race detector's own bookkeeping. Such a
// test fails a healthy tree at random. A test proves a contract by a direct
// observation instead (a probe context that counts its reads, a counted
// call only the contract's work makes); allocation counts belong in
// benchmarks, reported with b.ReportAllocs.
//
// Rule (go/ast, stdlib only): a reference to AllocsPerRun through the
// file's import of "testing" (its default name, an alias, or a dot import)
// is a finding unless it sits inside a top-level function named
// Benchmark... that has no receiver. Every .go file under the root is
// scanned, test helpers included; vendor, .git, node_modules and testdata
// directories are skipped. There is no suppression marker.
//
// Usage: go run ./scripts/ci/check-alloc-averages [root]
// Prints "path:line:col: <source line>" per finding and exits 1 when there
// is any; prints nothing and exits 0 on a clean tree; exits 2 when a file
// cannot be scanned.
package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	findings, err := scanTree(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan failed:", err)
		os.Exit(2)
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) > 0 {
		os.Exit(1)
	}
}

// scanTree scans every .go file under root and returns the findings sorted.
func scanTree(root string) ([]string, error) {
	var findings []string
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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		found, err := scan(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		findings = append(findings, found...)
		return nil
	})
	sort.Strings(findings)
	return findings, err
}

// scan parses one file and returns one finding per AllocsPerRun reference
// outside a benchmark function.
func scan(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	name, dot := testingImport(file)
	if name == "" && !dot {
		return nil, nil
	}
	var positions []token.Pos
	for _, decl := range file.Decls {
		if isBenchmark(decl) {
			continue
		}
		var visit func(n ast.Node) bool
		visit = func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.SelectorExpr:
				if x, ok := e.X.(*ast.Ident); ok && name != "" && x.Name == name && e.Sel.Name == "AllocsPerRun" {
					positions = append(positions, e.Pos())
				}
				// e.Sel names a field or method (v.AllocsPerRun), never
				// a dot-imported function: visit only e.X.
				ast.Inspect(e.X, visit)
				return false
			case *ast.Field:
				// A field's own names declare, never reference.
				if e.Type != nil {
					ast.Inspect(e.Type, visit)
				}
				return false
			case *ast.Ident:
				if dot && e.Name == "AllocsPerRun" {
					positions = append(positions, e.Pos())
				}
			}
			return true
		}
		ast.Inspect(decl, visit)
	}
	if len(positions) == 0 {
		return nil, nil
	}
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	findings := make([]string, 0, len(positions))
	for _, p := range positions {
		pos := fset.Position(p)
		src := ""
		if pos.Line-1 < len(lines) {
			src = strings.TrimSpace(lines[pos.Line-1])
		}
		findings = append(findings, fmt.Sprintf("%s:%d:%d: %s", pos.Filename, pos.Line, pos.Column, src))
	}
	return findings, nil
}

// testingImport returns the name the file refers to package testing by
// ("" when it does not import it or imports it blank) and whether it
// dot-imports it.
func testingImport(file *ast.File) (name string, dot bool) {
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != "testing" {
			continue
		}
		switch {
		case imp.Name == nil:
			name = "testing"
		case imp.Name.Name == ".":
			dot = true
		case imp.Name.Name != "_":
			name = imp.Name.Name
		}
	}
	return name, dot
}

// isBenchmark reports whether decl is a top-level benchmark function: no
// receiver and a name of the form Benchmark or BenchmarkXxx, where Xxx does
// not start with a lower-case letter (the go test rule).
func isBenchmark(decl ast.Decl) bool {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok || fn.Recv != nil {
		return false
	}
	rest, ok := strings.CutPrefix(fn.Name.Name, "Benchmark")
	if !ok {
		return false
	}
	return rest == "" || !(rest[0] >= 'a' && rest[0] <= 'z')
}

// readLines returns the lines of the file at path.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}
