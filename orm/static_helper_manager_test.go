package orm

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// swapDefaultCtx swaps the package default manager to next the first time
// anything reads a value from it: the static helpers read the transaction
// slot after resolving their driver, so this lands between that lookup
// and every later one.
type swapDefaultCtx struct {
	context.Context
	next *Manager
	once sync.Once
}

func (c *swapDefaultCtx) Value(key any) any {
	c.once.Do(func() { SetDefault(c.next) })
	return c.Context.Value(key)
}

// A static write helper resolves one manager and takes both its driver and
// its hook logger from it: when the default changes while the helper runs
// (another app being built), the row and the hook's failure line both stay
// with the manager the helper started on.
func TestStaticWriteHelpers_ResolveOneManager(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(ctx context.Context) error
	}{
		{"FirstOrCreate", func(ctx context.Context) error {
			_, err := (failingHookModel{}).FirstOrCreate(ctx, map[string]any{"name": "static-first"}, nil)
			return err
		}},
		{"UpdateOrCreate", func(ctx context.Context) error {
			_, err := (failingHookModel{}).UpdateOrCreate(ctx, map[string]any{"name": "static-update"}, nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, cleanupA := setupTxTest(t)
			defer cleanupA()
			b, cleanupB := setupTxTest(t)
			defer cleanupB()
			logsA, logsB := &levelLog{}, &levelLog{}
			a.SetLogger(logsA)
			b.SetLogger(logsB)
			SetDefault(a)

			if err := tc.run(&swapDefaultCtx{Context: context.Background(), next: b}); err != nil {
				t.Fatalf("write: %v", err)
			}
			if Default() != b {
				t.Fatal("the default did not change while the helper ran; the test proves nothing")
			}

			var inA, inB int
			if err := a.DB().QueryRow("SELECT COUNT(*) FROM tx_hook_models").Scan(&inA); err != nil {
				t.Fatalf("count a: %v", err)
			}
			if err := b.DB().QueryRow("SELECT COUNT(*) FROM tx_hook_models").Scan(&inB); err != nil {
				t.Fatalf("count b: %v", err)
			}
			if inA != 1 || inB != 0 {
				t.Fatalf("rows: manager a %d, manager b %d, want the write on a", inA, inB)
			}
			if got := logsA.count("WARN " + hookFailedLine); got != 1 {
				t.Errorf("hook failure lines on a's logger = %d, want 1 (a ran the write)", got)
			}
			if got := logsB.count("WARN " + hookFailedLine); got != 0 {
				t.Errorf("hook failure lines on b's logger = %d, want 0: b's logger took a's failure", got)
			}
		})
	}
}

// A builder made before any default manager existed adopts the default
// once, at its first transaction-bound statement, for both its driver and
// its manager: an inline hook failure then reaches that manager's logger,
// not the fallback.
func TestDetachedBuilder_AdoptsTheDefaultForDriverAndLogger(t *testing.T) {
	prev := Default()
	ResetDefault()
	q := (failingHookModel{}).Where("id > ?", 0)
	SetDefault(prev)
	if q.mgr != nil || q.driver != nil {
		t.Fatalf("builder made without a default holds mgr %v driver %v, want neither", q.mgr, q.driver)
	}
	a, cleanup := setupTxTest(t)
	defer cleanup()
	logs := &levelLog{}
	a.SetLogger(logs)
	fallback := fallbacklogtest.Capture(t)

	if err := a.Transaction(context.Background(), func(ctx context.Context) error {
		return q.Save(ctx, &failingHookModel{Name: "detached"})
	}); err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if got := logs.count("WARN " + hookFailedLine); got != 1 {
		t.Errorf("hook failure lines on the adopted manager's logger = %d, want 1", got)
	}
	if out := fallback.String(); strings.Contains(out, hookFailedLine) {
		t.Errorf("fallback got the hook failure: %q", out)
	}
}
