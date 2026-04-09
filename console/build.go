package console

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// BuildOptions holds flags for the build command.
type BuildOptions struct {
	Output string
	OS     string
	Arch   string
	Tags   string
}

// Build compiles the application for production.
func Build(opts BuildOptions) error {
	if opts.OS == "" {
		opts.OS = runtime.GOOS
	}
	if opts.Arch == "" {
		opts.Arch = runtime.GOARCH
	}

	output := opts.Output
	if output == "" {
		cwd, _ := os.Getwd()
		output = filepath.Base(cwd)
		if opts.OS == "windows" {
			output += ".exe"
		}
	}

	fmt.Printf("Building for %s/%s...\n", opts.OS, opts.Arch)

	env := os.Environ()
	env = append(env, fmt.Sprintf("GOOS=%s", opts.OS))
	env = append(env, fmt.Sprintf("GOARCH=%s", opts.Arch))
	env = append(env, "CGO_ENABLED=0")

	buildArgs := []string{"build", "-o", output, "-ldflags", "-s -w"}
	if opts.Tags != "" {
		buildArgs = append(buildArgs, "-tags", opts.Tags)
	}
	buildArgs = append(buildArgs, ".")

	cmd := exec.Command("go", buildArgs...)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	fmt.Printf("Built: %s\n", output)
	return nil
}
