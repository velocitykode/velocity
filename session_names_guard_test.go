package velocity

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// walkNonTestGo calls fn for every non-test Go file under root, skipping
// directories that hold no framework source.
func walkNonTestGo(t *testing.T, root string, fn func(path string, src []byte)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fn(filepath.ToSlash(path), src)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The CSRF middleware never issues or accepts a token without a real
// session, so no CSRF identifier or comment may describe a session it
// falls back to or an ephemeral id it mints.
func TestCSRFSource_NamesNoSessionFallback(t *testing.T) {
	removed := regexp.MustCompile(`(?i)fall.?back|ephemeral`)
	var offenders []string
	walkNonTestGo(t, "csrf", func(path string, src []byte) {
		for i, line := range strings.Split(string(src), "\n") {
			if removed.MatchString(line) {
				offenders = append(offenders, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	})
	if len(offenders) > 0 {
		t.Errorf("csrf source names a session fallback the middleware does not have:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// The auth manager on the context (contract.AuthManager) has no Session
// method; the session is reached through auth.FromContext(ctx).Session(r).
// A comment naming the other form describes code that does not compile.
func TestFrameworkSource_NamesNoContextAuthSession(t *testing.T) {
	var offenders []string
	walkNonTestGo(t, ".", func(path string, src []byte) {
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "Auth().Session") {
				offenders = append(offenders, path+":"+strconv.Itoa(i+1))
			}
		}
	})
	if len(offenders) > 0 {
		t.Errorf("source names ctx.Auth().Session, which does not exist:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// One type in the module is named SessionStore (auth.SessionStore, the
// store the session scheme loads and saves sessions through); the server
// record store is auth.ServerSessionStore and the CSRF token stores say
// where the token lives (stores.MemoryStore, stores.SessionBagStore).
func TestModule_OneSessionStoreType(t *testing.T) {
	fset := token.NewFileSet()
	var found []string
	walkNonTestGo(t, ".", func(path string, src []byte) {
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok && ts.Name.Name == "SessionStore" {
				found = append(found, fset.Position(ts.Pos()).String())
			}
			return true
		})
	})
	if len(found) > 1 {
		t.Errorf("%d types named SessionStore, want at most 1:\n  %s", len(found), strings.Join(found, "\n  "))
	}
}
