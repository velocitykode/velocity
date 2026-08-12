package console

import (
	"fmt"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/console/scaffold"
	"github.com/velocitykode/velocity/console/stubs"
)

// requireNormalizedName rejects a name that survives scaffold.ValidateName but
// normalizes to nothing once its redundant kind suffix is stripped (e.g.
// "vel gen module Module"). The generators validate the raw argument and then
// normalize, so without this check the empty result reaches the writer as a
// filename of ".go", which the Go toolchain silently ignores.
func requireNormalizedName(raw, normalized, kind string) error {
	if normalized == "" {
		return fmt.Errorf("invalid %s name %q: nothing remains after stripping the redundant %s suffix; pass the bare name instead", kind, raw, kind)
	}
	return nil
}

func runScaffoldGenerator(g scaffold.Generator, name, dirOverride string, data map[string]any) (scaffold.Result, error) {
	result, err := g.Generate(name, dirOverride, data)
	if err != nil {
		return scaffold.Result{}, err
	}
	prism.Success(fmt.Sprintf("Created: %s", result.Path))
	return result, nil
}

func writeScaffoldedFile(name, dirOverride, defaultDir, kind, filename, stubPath string, data map[string]any) error {
	stubContent, err := stubs.Get(stubPath)
	if err != nil {
		return fmt.Errorf("failed to read stub: %w", err)
	}

	_, err = runScaffoldGenerator(scaffold.Generator{
		DefaultDir: defaultDir,
		Kind:       kind,
		Stub:       string(stubContent),
		Filename:   filename,
	}, name, dirOverride, data)
	return err
}
