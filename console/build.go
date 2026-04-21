package console

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	cli "github.com/velocitykode/velocity-cli"
)

// BuildOptions holds flags for the build command.
type BuildOptions struct {
	Output  string
	OS      string
	Arch    string
	Tags    string
	Version string // overrides velocity.BuildInfo.Version
	Commit  string // overrides velocity.BuildInfo.Commit
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

	cli.Info(fmt.Sprintf("Building for %s/%s...", opts.OS, opts.Arch))

	env := os.Environ()
	env = append(env, fmt.Sprintf("GOOS=%s", opts.OS))
	env = append(env, fmt.Sprintf("GOARCH=%s", opts.Arch))
	env = append(env, "CGO_ENABLED=0")

	// Populate version metadata so bin reports the right build when
	// velocity.BuildInfo is accessed at runtime.
	commit := opts.Commit
	if commit == "" {
		commit = gitShortSHAOrDefault()
	}
	version := opts.Version
	if version == "" {
		version = "devel"
	}
	date := time.Now().UTC().Format(time.RFC3339)

	ldflags := strings.Join([]string{
		"-s -w",
		fmt.Sprintf("-X 'github.com/velocitykode/velocity.BuildInfo.Version=%s'", version),
		fmt.Sprintf("-X 'github.com/velocitykode/velocity.BuildInfo.Commit=%s'", commit),
		fmt.Sprintf("-X 'github.com/velocitykode/velocity.BuildInfo.Date=%s'", date),
	}, " ")

	buildArgs := []string{"build", "-o", output, "-ldflags", ldflags}
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

	cli.Success(fmt.Sprintf("Built: %s", output))
	return nil
}

// gitShortSHAOrDefault returns the current commit's short SHA, or "devel"
// when git is unavailable (e.g. installing from a release tarball).
func gitShortSHAOrDefault() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "devel"
	}
	return strings.TrimSpace(string(out))
}
