package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// Guard for the blacklist store boundary: the store is user code, so every
// call to it goes through one of the three contained calls, each of which
// defers containBlacklist before it touches the store.
//
// The store is reachable only through the manager's blacklistStore field,
// and only blStore hands it out: the manager clears the store from its
// config copy at construction (TestJWT_Manager_KeepsOneStoreReference),
// and the config's BlacklistStore field is named only by Validate and the
// constructor. So the rule is checked on the source of
// this package without types: the field is named only where the store is
// installed or handed out, blStore is called only by the three contained
// calls, and each of those defers containBlacklist as its first deferred
// call. A new store call anywhere else needs blStore or the field, and
// fails here.
func TestJWT_BlacklistStore_CalledOnlyThroughContainment(t *testing.T) {
	fieldOwners := map[string]bool{
		"NewJWTManager":     true, // composite literal key
		"SetBlacklistStore": true,
		"blStore":           true,
	}
	// The exported config field is read only where the store is validated
	// and taken over; the manager's own config copy holds nil after that.
	configOwners := map[string]bool{
		"Validate":      true,
		"NewJWTManager": true,
	}
	contained := map[string]bool{
		"blacklistAdd":     true,
		"blacklistHas":     true,
		"blacklistCleanup": true,
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	fset := token.NewFileSet()
	seenContained := map[string]bool{}
	files := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			fname := fn.Name.Name
			if contained[fname] {
				seenContained[fname] = true
				if !defersContainmentBeforeStoreUse(fn) {
					t.Errorf("%s: %s must defer containBlacklist(&err) before any other statement that uses the store", fset.Position(fn.Pos()), fname)
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if x.Sel.Name == "blacklistStore" && !fieldOwners[fname] {
						t.Errorf("%s: %s reads the blacklistStore field; only blStore hands the store out", fset.Position(x.Pos()), fname)
					}
					if x.Sel.Name == "BlacklistStore" && !configOwners[fname] {
						t.Errorf("%s: %s reads a config's BlacklistStore; the store is reached only through blStore", fset.Position(x.Pos()), fname)
					}
				case *ast.KeyValueExpr:
					if id, ok := x.Key.(*ast.Ident); ok && id.Name == "blacklistStore" && !fieldOwners[fname] {
						t.Errorf("%s: %s sets the blacklistStore field", fset.Position(x.Pos()), fname)
					}
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "blStore" && !contained[fname] {
						t.Errorf("%s: %s calls blStore; a blacklist store call goes through blacklistAdd, blacklistHas or blacklistCleanup", fset.Position(x.Pos()), fname)
					}
				}
				return true
			})
		}
	}
	if files == 0 {
		t.Fatal("no source files read: the guard checked nothing")
	}
	var missing []string
	for name := range contained {
		if !seenContained[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("contained calls not found (renamed? update this guard): %v", missing)
	}
}

// defersContainmentBeforeStoreUse reports whether fn's body is
// "store := j.blStore()" followed directly by "defer containBlacklist(&err)":
// nothing that could call the store runs before the recovery is installed.
func defersContainmentBeforeStoreUse(fn *ast.FuncDecl) bool {
	if len(fn.Body.List) < 3 {
		return false
	}
	assign, ok := fn.Body.List[0].(*ast.AssignStmt)
	if !ok || len(assign.Rhs) != 1 {
		return false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "blStore" {
		return false
	}
	def, ok := fn.Body.List[1].(*ast.DeferStmt)
	if !ok {
		return false
	}
	id, ok := def.Call.Fun.(*ast.Ident)
	return ok && id.Name == "containBlacklist" && len(def.Call.Args) == 1
}
