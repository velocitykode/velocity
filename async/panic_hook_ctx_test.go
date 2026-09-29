package async

import (
	"context"
	"testing"
	"time"
)

type hookCtxKey struct{}

// The panic hook receives the context of the work that panicked: the ctx
// given to GoCtx and RunWithContext, context.Background otherwise.
func TestSetPanicHook_ReceivesTheWorkContext(t *testing.T) {
	got := make(chan context.Context, 4)
	SetPanicHook(func(ctx context.Context, _ any) { got <- ctx })
	t.Cleanup(func() { SetPanicHook(nil) })
	ctx := context.WithValue(context.Background(), hookCtxKey{}, "work")

	wait := func(name string) context.Context {
		t.Helper()
		select {
		case c := <-got:
			return c
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: hook never ran", name)
			return nil
		}
	}
	GoCtx(ctx, func(context.Context) { panic("goctx") })
	if c := wait("GoCtx"); c.Value(hookCtxKey{}) != "work" {
		t.Errorf("GoCtx hook ctx lacks the work context")
	}
	_, _ = RunWithContext(ctx, func() int { panic("run") }).Get()
	if c := wait("RunWithContext"); c.Value(hookCtxKey{}) != "work" {
		t.Errorf("RunWithContext hook ctx lacks the work context")
	}
	Go(func() { panic("go") })
	if c := wait("Go"); c == nil || c.Value(hookCtxKey{}) != nil {
		t.Errorf("Go hook ctx = %v, want a non-nil context without the work value", c)
	}
	//lint:ignore SA1012 exercising the nil-context code path is the point of this check
	GoCtx(nil, func(context.Context) { panic("nil ctx") })
	if c := wait("GoCtx(nil)"); c == nil {
		t.Errorf("GoCtx(nil) hook ctx = nil, want context.Background")
	}
}
