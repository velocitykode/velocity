package maintpath_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/maintpath"
)

// resetForTest clears the cached resolution. Tests must call this between
// scenarios because the package caches its first result for the process.
func resetForTest(t *testing.T) {
	t.Helper()
	maintpath.Reset()
	t.Cleanup(maintpath.Reset)
}

func TestRoot_EnvOverrideWins(t *testing.T) {
	resetForTest(t)
	want := t.TempDir()
	t.Setenv(maintpath.EnvVar, want)
	maintpath.Reset()

	got, err := maintpath.Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got != want {
		t.Errorf("Root: got %q, want %q", got, want)
	}
	if src := maintpath.Source(); src != "env" {
		t.Errorf("Source: got %q, want %q", src, "env")
	}
}

func TestRoot_DoesNotDriftWithCWD(t *testing.T) {
	resetForTest(t)
	target := t.TempDir()
	t.Setenv(maintpath.EnvVar, target)
	maintpath.Reset()

	// Move cwd far away. The resolved path must still point at target.
	t.Chdir("/tmp")

	got, err := maintpath.Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got != target {
		t.Errorf("Root drifted with cwd: got %q, want %q", got, target)
	}
}

func TestRoot_RejectsRelative(t *testing.T) {
	resetForTest(t)
	t.Setenv(maintpath.EnvVar, "relative/path")
	maintpath.Reset()

	_, err := maintpath.Root()
	if !errors.Is(err, maintpath.ErrInvalidRoot) {
		t.Fatalf("Root err: got %v, want ErrInvalidRoot", err)
	}
}

func TestRoot_RejectsDotDot(t *testing.T) {
	resetForTest(t)
	t.Setenv(maintpath.EnvVar, "/var/../etc")
	maintpath.Reset()

	_, err := maintpath.Root()
	if !errors.Is(err, maintpath.ErrInvalidRoot) {
		t.Fatalf("Root err: got %v, want ErrInvalidRoot", err)
	}
	if !strings.Contains(err.Error(), "..") {
		t.Errorf("err should mention .., got %v", err)
	}
}

// Note: NUL byte in env is rejected by the runtime's os.Setenv before
// reaching the resolver, so we exercise the validator branch directly by
// asserting the package public surface still produces ErrInvalidRoot for
// a NUL byte via Root() when the env layer would otherwise accept it. We
// cannot Setenv with a NUL, so this test is omitted.

func TestRoot_DefaultIsCwd(t *testing.T) {
	resetForTest(t)
	os.Unsetenv(maintpath.EnvVar)
	maintpath.Reset()

	got, err := maintpath.Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("os.Getwd unavailable: %v", err)
	}

	if got != wd {
		t.Errorf("Root: got %q, want %q (cwd)", got, wd)
	}
	if src := maintpath.Source(); src != "cwd" {
		t.Errorf("Source: got %q, want %q", src, "cwd")
	}
}

func TestRoot_CachesFirstResolution(t *testing.T) {
	resetForTest(t)
	first := t.TempDir()
	t.Setenv(maintpath.EnvVar, first)
	maintpath.Reset()

	got1, err := maintpath.Root()
	if err != nil {
		t.Fatalf("Root 1: %v", err)
	}

	// Change env. The cached value must not move.
	second := t.TempDir()
	t.Setenv(maintpath.EnvVar, second)

	got2, err := maintpath.Root()
	if err != nil {
		t.Fatalf("Root 2: %v", err)
	}
	if got2 != got1 {
		t.Errorf("Root mutated post-cache: got %q, want %q", got2, got1)
	}
	if got2 != first {
		t.Errorf("Root: got %q, want %q (first resolution sticks)", got2, first)
	}
}

func TestMarkerPath_AppendsKnownSegments(t *testing.T) {
	resetForTest(t)
	root := t.TempDir()
	t.Setenv(maintpath.EnvVar, root)
	maintpath.Reset()

	got, err := maintpath.MarkerPath()
	if err != nil {
		t.Fatalf("MarkerPath: %v", err)
	}
	want := filepath.Join(root, maintpath.MarkerDir, maintpath.MarkerFile)
	if got != want {
		t.Errorf("MarkerPath: got %q, want %q", got, want)
	}
}

func TestMarkerDirPath_AppendsMarkerDir(t *testing.T) {
	resetForTest(t)
	root := t.TempDir()
	t.Setenv(maintpath.EnvVar, root)
	maintpath.Reset()

	got, err := maintpath.MarkerDirPath()
	if err != nil {
		t.Fatalf("MarkerDirPath: %v", err)
	}
	want := filepath.Join(root, maintpath.MarkerDir)
	if got != want {
		t.Errorf("MarkerDirPath: got %q, want %q", got, want)
	}
}
