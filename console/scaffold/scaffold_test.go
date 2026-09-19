package scaffold

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGeneratorGenerate(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)

	result, err := Generator{
		DefaultDir: "internal/tools",
		Kind:       "tool",
		Stub:       "package {{ .Package }}\n\ntype {{ .Name }} struct{}\n",
	}.Generate("ListUsers", "custom/tools", map[string]any{
		"Package": "tools",
		"Name":    "ListUsers",
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	wantPath := filepath.Join("custom", "tools", "list_users.go")
	if result.Path != wantPath {
		t.Fatalf("Result.Path = %q, want %q", result.Path, wantPath)
	}
	if len(result.Paths) != 1 || result.Paths[0] != wantPath {
		t.Fatalf("Result.Paths = %#v, want [%q]", result.Paths, wantPath)
	}

	content, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	if got := string(content); !strings.Contains(got, "type ListUsers struct{}") {
		t.Fatalf("generated content missing rendered data:\n%s", got)
	}
}

func TestGeneratorGenerateRejectsUnsafeTarget(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir()
	t.Chdir(tmp)

	if err := os.Symlink(outside, filepath.Join(tmp, "custom")); err != nil {
		t.Fatalf("setup symlink: %v", err)
	}

	_, err := Generator{
		DefaultDir: "internal/tools",
		Kind:       "tool",
		Stub:       "package tools\n",
	}.Generate("ListUsers", "custom/tools", nil)
	if err == nil {
		t.Fatal("Generate accepted --dir routed through a symlink")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "tools", "list_users.go")); statErr == nil {
		t.Fatalf("generated file escaped through symlink")
	}
}

// TestWriteNewFile covers the write-time guarantee: whatever sits at the target
// when the write happens (as if it appeared after EnsureWritableTarget passed)
// is neither replaced nor followed.
func TestWriteNewFile(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, target, outside string)
		wantErr string
		check   func(t *testing.T, target, outside string)
	}{
		{
			name:  "creates a new file",
			setup: func(t *testing.T, target, outside string) {},
			check: func(t *testing.T, target, outside string) {
				got, err := os.ReadFile(target)
				if err != nil {
					t.Fatalf("read target: %v", err)
				}
				if string(got) != "generated" {
					t.Fatalf("target content = %q, want %q", got, "generated")
				}
			},
		},
		{
			name: "keeps an existing file",
			setup: func(t *testing.T, target, outside string) {
				if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
					t.Fatalf("setup file: %v", err)
				}
			},
			wantErr: "already exists",
			check: func(t *testing.T, target, outside string) {
				got, err := os.ReadFile(target)
				if err != nil {
					t.Fatalf("read target: %v", err)
				}
				if string(got) != "original" {
					t.Fatalf("existing file was overwritten: %q", got)
				}
			},
		},
		{
			name: "does not write through a symlink",
			setup: func(t *testing.T, target, outside string) {
				if err := os.WriteFile(outside, []byte("original"), 0644); err != nil {
					t.Fatalf("setup outside file: %v", err)
				}
				if err := os.Symlink(outside, target); err != nil {
					t.Fatalf("setup symlink: %v", err)
				}
			},
			wantErr: "is a symlink",
			check: func(t *testing.T, target, outside string) {
				got, err := os.ReadFile(outside)
				if err != nil {
					t.Fatalf("read outside file: %v", err)
				}
				if string(got) != "original" {
					t.Fatalf("wrote through symlink: %q", got)
				}
			},
		},
		{
			name: "does not create through a dangling symlink",
			setup: func(t *testing.T, target, outside string) {
				if err := os.Symlink(outside, target); err != nil {
					t.Fatalf("setup symlink: %v", err)
				}
			},
			wantErr: "is a symlink",
			check: func(t *testing.T, target, outside string) {
				if _, err := os.Lstat(outside); !os.IsNotExist(err) {
					t.Fatalf("file created through dangling symlink (lstat err = %v)", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "list_users.go")
			outside := filepath.Join(t.TempDir(), "outside.go")
			tt.setup(t, target, outside)

			err := WriteNewFile(target, "tool", []byte("generated"))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("WriteNewFile returned error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("WriteNewFile succeeded, want error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("WriteNewFile error = %q, want it to contain %q", err, tt.wantErr)
			}
			tt.check(t, target, outside)
		})
	}
}

// TestWriteNewFileFailedWriteLeavesPathAlone covers the failure path: once the
// file is created, nothing is done to the path by name, so a file another
// process moved there mid-write survives a failed write.
func TestWriteNewFileFailedWriteLeavesPathAlone(t *testing.T) {
	tests := []struct {
		name        string
		replace     bool
		wantContent string
	}{
		{name: "target replaced mid-write", replace: true, wantContent: "other writer"},
		{name: "target untouched", replace: false, wantContent: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.replace && runtime.GOOS == "windows" {
				// The target is still open here, and Windows refuses to
				// rename over a file opened without delete sharing.
				t.Skip("renaming over an open file is not possible on Windows")
			}

			dir := t.TempDir()
			target := filepath.Join(dir, "list_users.go")
			replacement := filepath.Join(dir, "replacement.go")
			if err := os.WriteFile(replacement, []byte("other writer"), 0644); err != nil {
				t.Fatalf("setup replacement: %v", err)
			}

			failingWrite := func(*os.File, []byte) (int, error) {
				if tt.replace {
					if err := os.Rename(replacement, target); err != nil {
						t.Fatalf("replace target: %v", err)
					}
				}
				return 0, errors.New("disk full")
			}

			err := writeNewFile(target, "tool", []byte("generated"), failingWrite)
			if err == nil || !strings.Contains(err.Error(), "disk full") {
				t.Fatalf("writeNewFile error = %v, want it to wrap the write failure", err)
			}

			got, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatalf("target was removed after a failed write: %v", readErr)
			}
			if string(got) != tt.wantContent {
				t.Fatalf("target content = %q, want %q", got, tt.wantContent)
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	if err := ValidateName("SendEmail"); err != nil {
		t.Fatalf("ValidateName accepted name returned error: %v", err)
	}
	if err := ValidateName("Admin/Users"); err == nil {
		t.Fatal("ValidateName accepted nested name")
	}
	if err := ValidateNestedName("Admin/Users"); err != nil {
		t.Fatalf("ValidateNestedName rejected nested name: %v", err)
	}
}
