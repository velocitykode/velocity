package velocity

import (
	"os/exec"
	"strings"
	"testing"
)

// TestMarkdownEngineOutsideDefaultGraph pins the dependency boundary that
// keeps the Markdown engine (goldmark and its YAML parser) out of every
// binary that does not use it. The root package and the ORM reach the
// inflection helpers through internal/inflect, never through str, so only an
// import of str or markdown links the engine.
func TestMarkdownEngineOutsideDefaultGraph(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	for _, pkg := range []string{".", "./orm", "./console"} {
		out, err := exec.Command("go", "list", "-deps", pkg).Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", pkg, err)
		}
		deps := string(out)
		for _, forbidden := range []string{"github.com/yuin/goldmark", "go.yaml.in/yaml", "github.com/velocitykode/velocity/str", "github.com/velocitykode/velocity/markdown"} {
			if strings.Contains(deps, forbidden) {
				t.Errorf("%s links %s; route the helper through internal/inflect instead of str", pkg, forbidden)
			}
		}
	}
}

// TestRouterAndProblemImportedTogetherOnlyAtTheBoundary pins that the root
// package and problem/routerbridge are the only packages whose non-test
// code imports both router and problem: subsystems build their HTTP errors
// from the contract vocabulary, never from the error package.
func TestRouterAndProblemImportedTogetherOnlyAtTheBoundary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	const (
		module        = "github.com/velocitykode/velocity"
		routerPkg     = module + "/router"
		problemPkg    = module + "/problem"
		routerbridge  = module + "/problem/routerbridge"
		importsFormat = `{{.ImportPath}}{{range .Imports}} {{.}}{{end}}`
	)
	out, err := exec.Command("go", "list", "-f", importsFormat, "./...").Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pkg, imports := fields[0], fields[1:]
		if pkg == module || pkg == routerbridge {
			continue
		}
		hasRouter, hasProblem := false, false
		for _, imp := range imports {
			hasRouter = hasRouter || imp == routerPkg
			hasProblem = hasProblem || imp == problemPkg
		}
		if hasRouter && hasProblem {
			t.Errorf("%s imports both router and problem; build the error from contract instead", pkg)
		}
	}
}
