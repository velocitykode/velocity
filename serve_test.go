package velocity

import (
	"os"
	"testing"
)

// TestServeRunCmd_DoesNotRecurseThroughServe is the regression test for the
// "./vel serve" stack-overflow bug.
//
// Pre-fix, the hot-reload subprocess entry point recursed indefinitely:
//
//	Serve()             // os.Args = ["bin", "serve", "run"], len > 1
//	  → Run()           // dispatches on os.Args[1]
//	    → runCommand(["serve", "run"])
//	      → serveRunCmd.run
//	        → a.Serve() // back to the top, recurse forever
//
// until the goroutine stack hit 1 GB and the process crashed with
// `fatal error: stack overflow`.
//
// The fix splits Serve() into a public dispatcher and a private serveHTTP()
// helper; serveRunCmd.run now invokes serveHTTP() directly, bypassing the
// args-check entirely. This test proves the new call path:
//
//  1. Save and restore os.Args so the subprocess condition (len > 1) holds
//     for the duration of the test.
//  2. Install an App.serveHTTPHook that records a call and returns nil —
//     this short-circuits serveHTTP() before it touches services or the
//     network.
//  3. Invoke serveRunCmd{}.run(a, nil) and assert the hook fires exactly
//     once. On the pre-fix code, the call would re-enter Serve(), which
//     would re-dispatch to serveRunCmd.run, so the hook counter would
//     either never increment (if Serve recurses before the hook) or, if
//     the hook lives on a later path, overflow the stack.
func TestServeRunCmd_DoesNotRecurseThroughServe(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	// Simulate the hot-reload subprocess invocation. If serveRunCmd.run
	// calls a.Serve() (the pre-fix behavior), Serve() sees len(os.Args)>1
	// and delegates to Run(), which dispatches on os.Args[1:] = ["serve", "run"]
	// and re-enters serveRunCmd.run — infinite recursion.
	saved := os.Args
	os.Args = []string{"vel", "serve", "run"}
	t.Cleanup(func() { os.Args = saved })

	var calls int
	a.serveHTTPHook = func() error {
		calls++
		return nil
	}

	if err := (serveRunCmd{}).run(a, nil); err != nil {
		t.Fatalf("serveRunCmd.run returned error: %v", err)
	}

	if calls != 1 {
		t.Fatalf("serveHTTP hook fired %d times, want exactly 1", calls)
	}
}

// TestServe_DelegatesToRunWhenArgsPresent locks in the other half of the
// contract: Serve() with CLI args still goes through Run(), so the single-
// entry-point behavior documented on Serve() is preserved. Without this
// guard, a future refactor could short-circuit Serve() directly to
// serveHTTP() and silently break `./vel serve` (the public entry most
// consumer-generated main.go files use).
func TestServe_DelegatesToRunWhenArgsPresent(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	// Use "help" because it runs without bootstrap and without touching
	// stdin/stdout assertions — we only care that Run() is reached.
	saved := os.Args
	os.Args = []string{"vel", "help"}
	t.Cleanup(func() { os.Args = saved })

	// If Serve() skipped Run() and called serveHTTP() directly, this hook
	// would fire. It must not.
	hookFired := false
	a.serveHTTPHook = func() error {
		hookFired = true
		return nil
	}

	if err := a.Serve(); err != nil {
		t.Fatalf("Serve() returned error: %v", err)
	}

	if hookFired {
		t.Fatal("Serve() with CLI args reached serveHTTP() — should have dispatched through Run()")
	}
}
