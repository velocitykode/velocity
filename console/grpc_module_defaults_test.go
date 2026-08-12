package console

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestGenGRPCService_ModuleDoesNotForceReflection guards a contract with
// the runtime: velocity/grpc reads GRPC_REFLECTION from env (default false)
// and refuses to boot in production when reflection is enabled. A scaffold
// that hard-codes `velgrpc.WithReflection(true)` therefore (a) overrides the
// user's env default and (b) makes the generated app crash on prod boot.
// The stub must let the framework default through.
func TestGenGRPCService_ModuleDoesNotForceReflection(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")
	if err := GenGRPCService("Foo", GenGRPCServiceOptions{}); err != nil {
		t.Fatalf("GenGRPCService: %v", err)
	}
	data, err := os.ReadFile(filepath.Join("internal", "modules", "grpc_module.go"))
	if err != nil {
		t.Fatalf("read module: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "WithReflection") {
		t.Errorf("module must not call WithReflection; let GRPC_REFLECTION env decide:\n%s", s)
	}
}

// TestEnsureBufConfigs_PropagatesWriteFailure proves the success-message-and-
// failed-generation hazard is fixed: when buf.yaml cannot be written (here
// because api/proto is a read-only directory) the scaffolder must return a
// real error rather than print a success message and recommend `vel
// gen grpc gen` against missing configs.
func TestEnsureBufConfigs_PropagatesWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires POSIX permissions and non-root user")
	}
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := os.MkdirAll(filepath.Join("api", "proto"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join("api", "proto"), 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join("api", "proto"), 0755) })

	err := ensureBufConfigs()
	if err == nil {
		t.Fatal("expected error when api/proto is read-only")
	}
	if !strings.Contains(err.Error(), "buf.yaml") && !strings.Contains(err.Error(), "buf.gen.yaml") {
		t.Errorf("error should name the file that failed, got: %v", err)
	}
}

// TestEnsureBufConfigs_IdempotentWhenConfigsExist verifies the happy path:
// pre-existing buf configs are not overwritten and no error is returned.
// This guards against a future change that returns an error when the file
// already exists.
func TestEnsureBufConfigs_IdempotentWhenConfigsExist(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join("api", "proto"), 0755); err != nil {
		t.Fatal(err)
	}
	custom := []byte("# user customised\nversion: v2\n")
	yamlPath := filepath.Join("api", "proto", "buf.yaml")
	if err := os.WriteFile(yamlPath, custom, 0644); err != nil {
		t.Fatal(err)
	}

	if err := ensureBufConfigs(); err != nil {
		t.Fatalf("ensureBufConfigs: %v", err)
	}
	got, _ := os.ReadFile(yamlPath)
	if string(got) != string(custom) {
		t.Errorf("user-customised buf.yaml was overwritten:\n%s", got)
	}
}
