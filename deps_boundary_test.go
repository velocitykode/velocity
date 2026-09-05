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
