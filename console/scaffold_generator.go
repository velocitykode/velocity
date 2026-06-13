package console

import (
	"fmt"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/console/scaffold"
	"github.com/velocitykode/velocity/console/stubs"
)

func runScaffoldGenerator(g scaffold.Generator, name, dirOverride string, data map[string]any) (scaffold.Result, error) {
	result, err := g.Generate(name, dirOverride, data)
	if err != nil {
		return scaffold.Result{}, err
	}
	cli.Success(fmt.Sprintf("Created: %s", result.Path))
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
