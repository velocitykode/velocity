package orm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// failingHookModel's AfterCommit hook fails, so its inline (auto-commit)
// run writes one "tx callback returned error" line.
type failingHookModel struct {
	Model[failingHookModel]
	Name string
}

func (failingHookModel) TableName() string          { return "tx_hook_models" }
func (failingHookModel) AssignableFields() []string { return []string{"name"} }

func (m *failingHookModel) AfterCommit(context.Context) error { return errors.New("hook failed") }

// failingBulkHookModel's BulkAfterCommit hook fails.
type failingBulkHookModel struct {
	Model[failingBulkHookModel]
	Name string
}

func (failingBulkHookModel) TableName() string     { return "tx_hook_models" }
func (failingBulkHookModel) AllowAllColumns() bool { return true }
func (failingBulkHookModel) BulkAfterCommit(context.Context, []any, BulkOp) error {
	return errors.New("bulk hook failed")
}

const hookFailedLine = "velocity/orm: tx callback returned error"

// Every route that runs an after-commit hook inline, outside a
// Transaction, writes the hook's failure through the logger of the manager
// the write went through, never through the fallback.
func TestInlineHookFailure_EveryRouteLogsThroughTheManagerLogger(t *testing.T) {
	ctx := context.Background()
	routes := []struct {
		name string
		run  func(t *testing.T) error
	}{
		{"Save", func(*testing.T) error { return Save(ctx, nil, &failingHookModel{Name: "a"}) }},
		{"Model.Create", func(*testing.T) error {
			_, err := (failingHookModel{}).Create(ctx, map[string]any{"name": "a"})
			return err
		}},
		{"Query.Save", func(*testing.T) error {
			return (failingHookModel{}).Where("id > ?", 0).Save(ctx, &failingHookModel{Name: "a"})
		}},
		{"Query.Create map", func(*testing.T) error {
			_, err := (failingHookModel{}).Where("id > ?", 0).Create(ctx, map[string]any{"name": "a"})
			return err
		}},
		{"Query.Create struct", func(*testing.T) error {
			_, err := (failingHookModel{}).Where("id > ?", 0).Create(ctx, &failingHookModel{Name: "a"})
			return err
		}},
		{"Query.CreateMany", func(*testing.T) error {
			return (failingHookModel{}).Where("id > ?", 0).CreateMany(ctx, []failingHookModel{{Name: "a"}})
		}},
		{"Query.FirstOrCreate", func(*testing.T) error {
			_, err := (failingHookModel{}).Where("id > ?", 0).FirstOrCreate(ctx, map[string]any{"name": "new-first"}, nil)
			return err
		}},
		{"Query.UpdateOrCreate", func(*testing.T) error {
			_, err := (failingHookModel{}).Where("id > ?", 0).UpdateOrCreate(ctx, map[string]any{"name": "new-update"}, nil)
			return err
		}},
		{"Model.FirstOrCreate", func(*testing.T) error {
			_, err := (failingHookModel{}).FirstOrCreate(ctx, map[string]any{"name": "model-first"}, nil)
			return err
		}},
		{"Model.UpdateOrCreate", func(*testing.T) error {
			_, err := (failingHookModel{}).UpdateOrCreate(ctx, map[string]any{"name": "model-update"}, nil)
			return err
		}},
		{"Query.Save inside Transaction", func(*testing.T) error {
			return Default().Transaction(ctx, func(ctx context.Context) error {
				return (failingHookModel{}).Where("id > ?", 0).Save(ctx, &failingHookModel{Name: "a"})
			})
		}},
		{"bulk Update", func(t *testing.T) error {
			if err := Save(ctx, nil, &failingBulkHookModel{Name: "b"}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			_, err := (failingBulkHookModel{}).Where("id > ?", 0).Update(ctx, map[string]any{"name": "c"})
			return err
		}},
		{"bulk Update with row hooks", func(t *testing.T) error {
			if err := Save(ctx, nil, &failingBulkHookModel{Name: "b"}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			_, err := (failingHookModel{}).Where("id > ?", 0).WithRowHooks().Update(ctx, map[string]any{"name": "c"})
			return err
		}},
	}
	for _, r := range routes {
		t.Run(r.name, func(t *testing.T) {
			m, cleanup := setupTxTest(t)
			defer cleanup()
			logs := &levelLog{}
			m.SetLogger(logs)
			fallback := fallbacklogtest.Capture(t)

			if err := r.run(t); err != nil {
				t.Fatalf("write: %v", err)
			}

			if got := logs.count("WARN " + hookFailedLine); got < 1 {
				t.Errorf("manager logger hook failure lines = %d, want at least 1: %v", got, logs.entries)
			}
			if out := fallback.String(); strings.Contains(out, hookFailedLine) {
				t.Errorf("fallback got the hook failure: %q", out)
			}
		})
	}
}

// The hook logger is bound only where the inline branch will read it: not
// for a detached builder (nil manager), not under a transaction callback
// list, and not for a model without an AfterCommit hook.
func TestWithHookLogger_BindsOnlyForTheInlineBranch(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	logs := &levelLog{}
	m.SetLogger(logs)
	bg := context.Background()

	if got := lookupTxRecoverLogger(withHookLogger(bg, nil)); got != nil {
		t.Errorf("nil manager bound %v, want nothing", got)
	}
	if got := lookupTxRecoverLogger(withModelHookLogger[failingHookModel](bg, m)); got != logs {
		t.Errorf("hook model bound %v, want the manager logger", got)
	}
	if ctx := withModelHookLogger[User](bg, m); ctx != bg {
		t.Error("model without a hook: ctx changed, want it untouched")
	}
	txCtx := PrepareTxCallbacks(bg)
	_, _, release := installTxCallbacks(txCtx)
	defer release()
	if ctx := withHookLogger(txCtx, m); ctx != txCtx {
		t.Error("under a transaction callback list: ctx changed, want it untouched")
	}
}
