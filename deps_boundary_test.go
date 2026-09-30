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

// TestEventEnvelopeHelperOutsideRouterGraph pins that the router's
// dependency graph does not grow with the event envelope: the router and
// the packages it depends on (scheduler among them) build contract.EventMeta
// themselves from contract and trace, never through internal/eventmeta.
func TestEventEnvelopeHelperOutsideRouterGraph(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := exec.Command("go", "list", "-deps", "./router").Output()
	if err != nil {
		t.Fatalf("go list -deps ./router: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "github.com/velocitykode/velocity/internal/eventmeta" {
			t.Errorf("./router links %s; build the envelope from contract and trace in the router's dependencies", dep)
		}
	}
}

// TestEventEmitterImportsOnlyItsLeaves pins that internal/eventemit, which
// the router and every event-dispatching package link, imports only the
// standard library, contract, internal/errchain, internal/fallbacklog,
// internal/goroutine, internal/panicerr and trace (each stdlib-only or
// built from those, all already in the router's graph), so it adds itself
// to the router's dependency graph and nothing heavy.
func TestEventEmitterImportsOnlyItsLeaves(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	const module = "github.com/velocitykode/velocity"
	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./internal/eventemit").Output()
	if err != nil {
		t.Fatalf("go list -deps ./internal/eventemit: %v", err)
	}
	allowed := map[string]bool{
		module + "/internal/eventemit":   true,
		module + "/contract":             true,
		module + "/internal/errchain":    true,
		module + "/internal/fallbacklog": true,
		module + "/internal/goroutine":   true,
		module + "/internal/panicerr":    true,
		module + "/internal/tracekeys":   true,
		module + "/trace":                true,
	}
	for _, dep := range strings.Fields(string(out)) {
		if !allowed[dep] {
			t.Errorf("internal/eventemit links %s; it may import only the standard library, contract, internal/errchain, internal/fallbacklog, internal/goroutine, internal/panicerr, internal/tracekeys and trace", dep)
		}
	}
}

// internal/goroutine backs the re-entry guards of eventemit, events, the
// ORM and the scheduler, and so sits under router: it stays a leaf.
func TestGoroutineImportsOnlyTheStandardLibrary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./internal/goroutine").Output()
	if err != nil {
		t.Fatalf("go list -deps ./internal/goroutine: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep != "github.com/velocitykode/velocity/internal/goroutine" {
			t.Errorf("internal/goroutine links %s; it may import only the standard library", dep)
		}
	}
}

// TestLeafPackagesImportOnlyStdlibAndErrchain pins the leaf rule: contract
// and resource import the standard library and, of the module, only
// internal/errchain (the bounded, contained error walk contract's
// predicates run on); errchain itself imports the standard library alone.
// Every package below router stays free of the framework's own graph.
func TestLeafPackagesImportOnlyStdlibAndErrchain(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	const (
		module   = "github.com/velocitykode/velocity"
		errchain = module + "/internal/errchain"
	)
	allowed := map[string][]string{
		"./contract":          {module + "/contract", errchain},
		"./resource":          {module + "/resource", errchain},
		"./internal/errchain": {errchain},
	}
	for pkg, allow := range allowed {
		out, err := exec.Command("go", "list", "-deps", pkg).Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", pkg, err)
		}
		for _, dep := range strings.Fields(string(out)) {
			if !strings.HasPrefix(dep, module) {
				if strings.Contains(strings.SplitN(dep, "/", 2)[0], ".") {
					t.Errorf("%s depends on %s, outside the standard library", pkg, dep)
				}
				continue
			}
			ok := false
			for _, a := range allow {
				ok = ok || dep == a
			}
			if !ok {
				t.Errorf("%s depends on %s; it may import only the standard library and %v", pkg, dep, allow)
			}
		}
	}
}

// internal/tracekeys holds the trace package's context keys, and sits
// under router and eventemit through trace: it imports nothing.
func TestTraceKeysImportsNothing(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := exec.Command("go", "list", "-f", "{{join .Imports \" \"}}", "./internal/tracekeys").Output()
	if err != nil {
		t.Fatalf("go list ./internal/tracekeys: %v", err)
	}
	if imports := strings.TrimSpace(string(out)); imports != "" {
		t.Errorf("internal/tracekeys imports %s; it may import nothing", imports)
	}
}
