package console

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuild_DefaultOptions(t *testing.T) {
	// Create a minimal Go project
	tmpDir := t.TempDir()
	os.Chdir(tmpDir)

	os.WriteFile("go.mod", []byte("module testapp\n\ngo 1.21\n"), 0644)
	os.WriteFile("main.go", []byte("package main\n\nfunc main() {}\n"), 0644)

	err := Build(BuildOptions{Output: "testbin"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, "testbin")); err != nil {
		t.Error("expected testbin to be created")
	}
}
