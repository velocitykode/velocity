package console

import (
	"os"
	"strings"
	"testing"
)

func TestBuild_DefaultOptions(t *testing.T) {
	// Create a minimal Go project
	t.Chdir(t.TempDir())

	os.WriteFile("go.mod", []byte("module testapp\n\ngo 1.21\n"), 0644)
	os.WriteFile("main.go", []byte("package main\n\nfunc main() {}\n"), 0644)

	err := Build(BuildOptions{Output: "testbin"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if _, err := os.Stat("testbin"); err != nil {
		t.Error("expected testbin to be created")
	}
}

func TestBuild_RejectsLDFlagsInjection(t *testing.T) {
	cases := []struct {
		name  string
		opts  BuildOptions
		field string
	}{
		{
			name:  "version with quote and -X",
			opts:  BuildOptions{Output: "x", Version: "x' -X 'main.foo=bar"},
			field: "version",
		},
		{
			name:  "version with newline",
			opts:  BuildOptions{Output: "x", Version: "v1.2.3\n-X main.foo=bar"},
			field: "version",
		},
		{
			name:  "version with whitespace",
			opts:  BuildOptions{Output: "x", Version: "v1.2.3 -X main.foo=bar"},
			field: "version",
		},
		{
			name:  "version with backslash",
			opts:  BuildOptions{Output: "x", Version: `v1.2.3\nfoo`},
			field: "version",
		},
		{
			name:  "version with double quote",
			opts:  BuildOptions{Output: "x", Version: `v1.2.3" -X "main.foo=bar`},
			field: "version",
		},
		{
			name:  "version with backtick",
			opts:  BuildOptions{Output: "x", Version: "v1.2.3`id`"},
			field: "version",
		},
		{
			name:  "version with dollar",
			opts:  BuildOptions{Output: "x", Version: "v1.2.3$(whoami)"},
			field: "version",
		},
		{
			name:  "commit with quote",
			opts:  BuildOptions{Output: "x", Version: "v1.0.0", Commit: "abc' -X 'main.foo=bar"},
			field: "commit",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			os.WriteFile("go.mod", []byte("module testapp\n\ngo 1.21\n"), 0644)
			os.WriteFile("main.go", []byte("package main\n\nfunc main() {}\n"), 0644)

			err := Build(tc.opts)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), "invalid "+tc.field) {
				t.Errorf("expected %q in error, got %v", "invalid "+tc.field, err)
			}
			// The binary must NOT exist if validation rejected the input.
			if _, statErr := os.Stat("x"); statErr == nil {
				t.Errorf("binary was written despite rejected build metadata")
			}
		})
	}
}

func TestBuild_AcceptsValidVersionShapes(t *testing.T) {
	cases := []struct {
		name    string
		version string
		commit  string
	}{
		{name: "semver", version: "v1.2.3", commit: "abc1234"},
		{name: "semver with build metadata", version: "v1.2.3+build.5", commit: "abc1234"},
		{name: "git describe", version: "v1.2.3-4-gabc1234", commit: "abc1234"},
		{name: "full sha", version: "v0.32.0", commit: "abc1234567890abcdef1234567890abcdef12345"},
		{name: "devel default", version: "devel", commit: "devel"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			os.WriteFile("go.mod", []byte("module testapp\n\ngo 1.21\n"), 0644)
			os.WriteFile("main.go", []byte("package main\n\nfunc main() {}\n"), 0644)

			err := Build(BuildOptions{Output: "testbin", Version: tc.version, Commit: tc.commit})
			if err != nil {
				t.Fatalf("Build(%q, %q) returned error: %v", tc.version, tc.commit, err)
			}
			if _, err := os.Stat("testbin"); err != nil {
				t.Errorf("expected testbin to be created")
			}
		})
	}
}
