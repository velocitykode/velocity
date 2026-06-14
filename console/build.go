package console

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/velocitykode/prism"
)

// ldflagsValueRe is the allowlist for values interpolated into the -ldflags
// argument. The Go linker re-tokenises -ldflags on whitespace and honours
// shell-style single quotes, so any quote, whitespace, shell-metacharacter,
// or backslash in version/commit lets the caller inject an extra -X
// directive that overwrites exported string vars (signing keys, default
// URLs, feature flags) with attacker-chosen content. The pattern below
// matches the shapes produced by git describe (v1.2.3-4-gabc1234), semver
// (v1.2.3+build.5), and SHA digests while rejecting every injection-capable
// character.
var ldflagsValueRe = regexp.MustCompile(`^[A-Za-z0-9._+/:@-]*$`)

// validateLDFlagsValue returns a clear error when value contains any
// character that would let the Go linker re-tokenise the -ldflags argument
// and inject extra -X directives. The empty string is rejected separately
// at the call site because the caller already substitutes defaults when
// the flag is unset.
func validateLDFlagsValue(field, value string) error {
	if !ldflagsValueRe.MatchString(value) {
		return fmt.Errorf("invalid %s %q: only [A-Za-z0-9._+/:@-] characters are allowed in build metadata", field, value)
	}
	return nil
}

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

	prism.Info(fmt.Sprintf("Building for %s/%s...", opts.OS, opts.Arch))

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
	// Validate any caller-supplied build metadata before it reaches the
	// -ldflags argument. The Go linker re-tokenises that string on
	// whitespace and honours single quotes, so an unfiltered value like
	// "x' -X 'main.foo=bar" would smuggle a second -X directive past the
	// build command and overwrite arbitrary exported string vars.
	if err := validateLDFlagsValue("version", version); err != nil {
		return err
	}
	if err := validateLDFlagsValue("commit", commit); err != nil {
		return err
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

	prism.Success(fmt.Sprintf("Built: %s", output))
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
