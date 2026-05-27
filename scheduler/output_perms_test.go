//go:build unix

package scheduler

import (
	"os"
	"path/filepath"
	"testing"
)

// TestJob_OutputFileDefaultsToTightPerms verifies that a scheduled command
// job writing stdout/stderr to a file lands at 0o600. The output capture
// can contain PII, partial secrets, or stack traces and must never default
// to world-readable. Regression for the cross-cutting file permissions
// sweep (Tier 4 LOW).
func TestJob_OutputFileDefaultsToTightPerms(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "scheduled.out")

	job := &Job{
		name:     "true",
		command:  "/bin/sh",
		args:     []string{"-c", "echo ran"},
		schedule: &Schedule{},
	}
	job.SendOutputTo(outPath)

	if err := job.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("output file mode = %#o, want 0600", got)
	}
}

// TestJob_OutputFileAppendDefaultsToTightPerms covers the AppendOutputTo
// branch which uses O_APPEND.
func TestJob_OutputFileAppendDefaultsToTightPerms(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "scheduled.out")

	job := &Job{
		name:     "echo",
		command:  "/bin/sh",
		args:     []string{"-c", "echo ran"},
		schedule: &Schedule{},
	}
	job.AppendOutputTo(outPath)

	if err := job.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("output file mode = %#o, want 0600", got)
	}
}

// TestJob_OutputFileTightensPreExistingLooseMode verifies that an output
// file laid down with 0o644 by an older binary is tightened to 0o600 on
// the next scheduled run. os.OpenFile does not chmod pre-existing files
// so without the explicit post-open Chmod the loose mode would persist.
func TestJob_OutputFileTightensPreExistingLooseMode(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "scheduled.out")

	// Seed the file at 0o644 (the pre-fix default).
	if err := os.WriteFile(outPath, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(outPath, 0o644); err != nil {
		t.Fatalf("seed chmod: %v", err)
	}

	job := &Job{
		name:     "echo",
		command:  "/bin/sh",
		args:     []string{"-c", "echo ran"},
		schedule: &Schedule{},
	}
	job.AppendOutputTo(outPath)

	if err := job.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("output file mode = %#o, want 0600 (pre-existing loose mode must be tightened)", got)
	}
}
