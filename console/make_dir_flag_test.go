package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/console/scaffold"
)

// TestResolveMakeDir covers the shared --dir resolver that every gen
// generator now routes through. An empty override must pass the default
// untouched; a legitimate nested override must be cleaned and returned; and
// traversal / absolute / charset-violating overrides must be rejected before
// any directory is created.
func TestResolveMakeDir(t *testing.T) {
	cases := []struct {
		name       string
		defaultDir string
		override   string
		want       string
		wantErr    bool
	}{
		{name: "empty override keeps default", defaultDir: "internal/models", override: "", want: "internal/models"},
		{name: "nested override accepted", defaultDir: "internal/models", override: "internal/domain/models", want: filepath.Join("internal", "domain", "models")},
		{name: "trailing slash rejected", defaultDir: "internal/models", override: "app/Models/", wantErr: true},
		{name: "parent traversal rejected", defaultDir: "internal/models", override: "../../tmp/owned", wantErr: true},
		{name: "absolute rejected", defaultDir: "internal/models", override: "/etc/passwd", wantErr: true},
		{name: "dot segment rejected", defaultDir: "internal/models", override: "./internal/models", wantErr: true},
		{name: "bad charset rejected", defaultDir: "internal/models", override: "internal/mod els", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scaffold.ResolveDir(tc.defaultDir, tc.override)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("scaffold.ResolveDir(%q, %q) = %q, want error", tc.defaultDir, tc.override, got)
				}
				if !strings.Contains(err.Error(), "--dir") {
					t.Errorf("error should name the --dir flag, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("scaffold.ResolveDir(%q, %q) unexpected error: %v", tc.defaultDir, tc.override, err)
			}
			if got != tc.want {
				t.Errorf("scaffold.ResolveDir(%q, %q) = %q, want %q", tc.defaultDir, tc.override, got, tc.want)
			}
		})
	}
}

// TestMake_DirFlag_WritesToCustomDir confirms each generator honours a --dir
// override end-to-end: the generated file lands under the custom directory and
// NOT under the package default. One row per generator so a future command
// that forgets to thread opts.Dir through scaffold.ResolveDir is caught here.
func TestMake_DirFlag_WritesToCustomDir(t *testing.T) {
	const dir = "custom/output"

	cases := []struct {
		name    string
		run     func(string) error
		input   string
		file    string // expected basename under dir
		setup   func(t *testing.T)
		defltAt string // package default dir that must stay empty
	}{
		{name: "gen command", run: func(n string) error { return MakeCommand(n, MakeCommandOptions{Dir: dir}) }, input: "SendEmail", file: "send_email.go", defltAt: "internal/commands"},
		{name: "gen event", run: func(n string) error { return MakeEvent(n, MakeEventOptions{Dir: dir}) }, input: "UserRegistered", file: "user_registered.go", defltAt: "internal/events"},
		{name: "gen job", run: func(n string) error { return MakeJob(n, MakeJobOptions{Dir: dir}) }, input: "ProcessImport", file: "process_import.go", defltAt: "internal/jobs"},
		{name: "gen listener", run: func(n string) error { return MakeListener(n, MakeListenerOptions{Dir: dir}) }, input: "SendWelcome", file: "send_welcome.go", defltAt: "internal/listeners"},
		{name: "gen mail", run: func(n string) error { return MakeMail(n, MakeMailOptions{Dir: dir}) }, input: "OrderShipped", file: "order_shipped.go", defltAt: "internal/mail"},
		{name: "gen middleware", run: func(n string) error { return MakeMiddleware(n, MakeMiddlewareOptions{Dir: dir}) }, input: "RateLimit", file: "rate_limit.go", defltAt: "internal/middleware"},
		{name: "gen model", run: func(n string) error { return MakeModel(n, MakeModelOptions{Dir: dir}) }, input: "Invoice", file: "invoice.go", defltAt: "internal/models"},
		{name: "gen notification", run: func(n string) error { return MakeNotification(n, MakeNotificationOptions{Dir: dir}) }, input: "PaymentFailed", file: "payment_failed.go", defltAt: "internal/notifications"},
		{name: "gen policy", run: func(n string) error { return MakePolicy(n, MakePolicyOptions{Dir: dir}) }, input: "Post", file: "post.go", defltAt: "internal/policies"},
		{name: "gen module", run: func(n string) error { return MakeModule(n, MakeModuleOptions{Dir: dir}) }, input: "Payment", file: "payment.go", defltAt: "internal/providers"},
		{name: "gen resource", run: func(n string) error { return MakeResource(n, MakeResourceOptions{Dir: dir}) }, input: "User", file: "user.go", defltAt: "internal/resources"},
		{name: "gen handler", run: func(n string) error { return MakeHandler(n, MakeHandlerOptions{Dir: dir}) }, input: "Dashboard", file: "dashboard.go", defltAt: "internal/handlers"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Chdir(tmp)
			if tc.setup != nil {
				tc.setup(t)
			}

			if err := tc.run(tc.input); err != nil {
				t.Fatalf("%s with --dir %q: %v", tc.name, dir, err)
			}

			want := filepath.Join(tmp, dir, tc.file)
			if _, err := os.Stat(want); err != nil {
				t.Errorf("%s: expected file at %s, stat err: %v", tc.name, want, err)
			}
			if _, err := os.Stat(filepath.Join(tmp, tc.defltAt)); err == nil {
				t.Errorf("%s: default dir %s should not have been created when --dir was set", tc.name, tc.defltAt)
			}
		})
	}
}

// TestResolveMakeDir_RejectsSymlinkComponent confirms a --dir whose lexical
// path stays in-tree but whose existing components include a symlink is
// rejected. A symlink pointing outside the project (custom -> /tmp/outside)
// passes the lexical within-root check, so without this guard the eventual
// write follows the link and escapes the sandbox.
func TestResolveMakeDir_RejectsSymlinkComponent(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir()
	t.Chdir(tmp)

	if err := os.Symlink(outside, filepath.Join(tmp, "custom")); err != nil {
		t.Fatalf("setup symlink: %v", err)
	}

	if _, err := scaffold.ResolveDir("internal/models", "custom/models"); err == nil {
		t.Fatal("scaffold.ResolveDir accepted a --dir routed through a symlink")
	}

	// And end-to-end: the generator must write nothing through the link.
	if err := MakeModel("Invoice", MakeModelOptions{Dir: "custom/models"}); err == nil {
		t.Fatal("gen model accepted a --dir routed through a symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "models", "invoice.go")); err == nil {
		t.Errorf("a file escaped to %s via the symlink", outside)
	}
}

// TestMakeHandler_RejectsSymlinkInNameNesting confirms the handler guard
// covers the name-derived subdirectory, not just the --dir root. A benign
// --dir plus a name like Admin/Dashboard assembles internal/web/handlers/admin;
// if that admin component is a pre-existing symlink out of the tree the lexical
// within-root check passes, so the final-path symlink guard must catch it.
func TestMakeHandler_RejectsSymlinkInNameNesting(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir()
	t.Chdir(tmp)

	nested := filepath.Join(tmp, "internal", "web", "handlers")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("setup dirs: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(nested, "admin")); err != nil {
		t.Fatalf("setup symlink: %v", err)
	}

	err := MakeHandler("Admin/Dashboard", MakeHandlerOptions{Dir: "internal/web/handlers"})
	if err == nil {
		t.Fatal("gen handler accepted a name-nested path routed through a symlink")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "dashboard.go")); statErr == nil {
		t.Errorf("a handler file escaped to %s via the symlink", outside)
	}
}

// TestMake_DirFlag_RejectsDanglingFinalSymlink confirms the generator refuses
// to write when the final output FILE path is itself a pre-existing (dangling)
// symlink pointing outside the tree. os.Stat follows the link and reports
// not-exist, so without an Lstat-based guard the write would create the target
// outside the project root.
func TestMake_DirFlag_RejectsDanglingFinalSymlink(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir()
	t.Chdir(tmp)

	dir := filepath.Join(tmp, "custom", "output")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup dir: %v", err)
	}
	// Dangling: target does not exist yet, so os.Stat would say "not exist".
	link := filepath.Join(dir, "invoice.go")
	target := filepath.Join(outside, "invoice.go")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("setup symlink: %v", err)
	}

	if err := MakeModel("Invoice", MakeModelOptions{Dir: "custom/output"}); err == nil {
		t.Fatal("gen model wrote through a dangling final-path symlink")
	}
	if _, err := os.Stat(target); err == nil {
		t.Errorf("a file escaped to %s via the final-path symlink", target)
	}
}

// TestMake_DirFlag_RejectsTraversal confirms a traversal supplied via --dir
// (rather than via the name) is rejected and writes nothing outside the root.
// The name is benign here so only the --dir guard can fail the call.
func TestMake_DirFlag_RejectsTraversal(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)

	err := MakeModel("Invoice", MakeModelOptions{Dir: "../../tmp/owned"})
	if err == nil {
		t.Fatal("gen model accepted traversal via --dir")
	}
	if !strings.Contains(err.Error(), "--dir") {
		t.Errorf("expected --dir rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmp, "..", "owned")); statErr == nil {
		t.Error("a file appeared outside the project root")
	}
}
