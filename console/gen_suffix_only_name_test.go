package console

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGenerators_SuffixOnlyNameRejected covers the whole family of generators
// that strip a redundant kind suffix from the user-supplied name. Each one
// validates the RAW argument and then normalises it, so a name consisting of
// nothing but the suffix ("vel gen job Job") survives validation and
// normalises to the empty string. Without the normalised-name check that
// empty result becomes a file named ".go", which the Go toolchain ignores:
// the command reports success and leaves the user with no usable file.
//
// Each row must list EVERY suffix its generator strips, in every case form the
// generator accepts. A missing form is a real hole: "mail" was absent while
// "Mail" was present, and the generator matched that asymmetry by stripping
// only "Mail", so `vel gen mail mail` generated a file named ".go".
func TestGenerators_SuffixOnlyNameRejected(t *testing.T) {
	tests := []struct {
		name string
		args []string
		gen  func(string) error
	}{
		{"handler", []string{"Handler", "handler", "Controller", "controller"}, func(n string) error {
			return GenHandler(n, GenHandlerOptions{})
		}},
		{"model", []string{"Model", "model"}, func(n string) error {
			return GenModel(n, GenModelOptions{})
		}},
		{"middleware", []string{"Middleware", "middleware"}, func(n string) error {
			return GenMiddleware(n, GenMiddlewareOptions{})
		}},
		{"event", []string{"Event", "event"}, func(n string) error {
			return GenEvent(n, GenEventOptions{})
		}},
		{"listener", []string{"Listener", "listener"}, func(n string) error {
			return GenListener(n, GenListenerOptions{})
		}},
		{"job", []string{"Job", "job"}, func(n string) error {
			return GenJob(n, GenJobOptions{})
		}},
		{"mailable", []string{"Mailable", "mailable", "Mail", "mail"}, func(n string) error {
			return GenMail(n, GenMailOptions{})
		}},
		{"notification", []string{"Notification", "notification"}, func(n string) error {
			return GenNotification(n, GenNotificationOptions{})
		}},
		{"resource", []string{"Resource", "resource"}, func(n string) error {
			return GenResource(n, GenResourceOptions{})
		}},
		{"policy", []string{"Policy", "policy"}, func(n string) error {
			return GenPolicy(n, GenPolicyOptions{})
		}},
		{"module", []string{"Module", "module"}, func(n string) error {
			return GenModule(n, GenModuleOptions{})
		}},
		{"command", []string{"Command", "command"}, func(n string) error {
			return GenCommand(n, GenCommandOptions{})
		}},
	}

	for _, tt := range tests {
		for _, arg := range tt.args {
			t.Run(tt.name+"/"+arg, func(t *testing.T) {
				dir := t.TempDir()
				t.Chdir(dir)

				if err := tt.gen(arg); err == nil {
					t.Fatalf("Gen %s(%q) = nil, want error", tt.name, arg)
				}

				// Nothing may have been written under any name, and in
				// particular not the extension-only ".go" the empty
				// normalised name would produce.
				matches, err := filepath.Glob(filepath.Join(dir, "internal", "*", "*.go"))
				if err != nil {
					t.Fatalf("Glob() error: %v", err)
				}
				if len(matches) != 0 {
					t.Errorf("Gen %s(%q) wrote %v, want no files", tt.name, arg, matches)
				}
			})
		}
	}
}

// TestGenerators_SuffixOnlyNameRejected_HandlerNested pins the namespaced
// handler form: the redundant suffix is judged on the final path segment, so
// "Admin/Handler" is rejected the same way a bare "Handler" is.
func TestGenerators_SuffixOnlyNameRejected_HandlerNested(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := GenHandler("Admin/Handler", GenHandlerOptions{}); err == nil {
		t.Fatal("GenHandler(\"Admin/Handler\") = nil, want error")
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "handlers", "admin", ".go")); err == nil {
		t.Error("GenHandler wrote an extension-only .go file")
	}
}
