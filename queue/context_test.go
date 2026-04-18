package queue

import (
	"context"
	"testing"
	"time"
)

// TestMemoryDriver_CtxMethods_RespectCancellation verifies that PushCtx /
// PushDelayedCtx / PopCtx return ctx.Err() when the caller's context is
// already cancelled — the core reason the Ctx-suffixed methods exist.
func TestMemoryDriver_CtxMethods_RespectCancellation(t *testing.T) {
	d := NewMemoryDriver()
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := d.PushCtx(ctx, &testBatchJob{}, "q"); err != context.Canceled {
		t.Errorf("PushCtx(cancelled): err = %v, want context.Canceled", err)
	}
	if err := d.PushDelayedCtx(ctx, &testBatchJob{}, 0, "q"); err != context.Canceled {
		t.Errorf("PushDelayedCtx(cancelled): err = %v, want context.Canceled", err)
	}
	if _, err := d.PopCtx(ctx, "q"); err != context.Canceled {
		t.Errorf("PopCtx(cancelled): err = %v, want context.Canceled", err)
	}
}

// TestMemoryDriver_CtxMethods_HappyPath sanity-checks that PushCtx enqueues
// and PopCtx returns the job when ctx is live.
func TestMemoryDriver_CtxMethods_HappyPath(t *testing.T) {
	d := NewMemoryDriver()
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := d.PushCtx(ctx, &testBatchJob{}, "q"); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}
	job, err := d.PopCtx(ctx, "q")
	if err != nil {
		t.Fatalf("PopCtx: %v", err)
	}
	if job == nil {
		t.Fatal("PopCtx: got nil job, want one queued")
	}
}
