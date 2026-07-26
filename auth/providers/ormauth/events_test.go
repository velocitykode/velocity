package ormauth_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/orm"
)

// TestProvider_EmitsQueryEvents is the regression test for the blind spot
// that motivated moving query telemetry into the database/sql driver: the
// login lookup runs on every single authentication attempt and used to emit
// nothing at all, leaving authentication invisible to APM.
//
// It doubles as the pinned-SQL test. The statements are no longer written by
// hand, so what is pinned is the shape the ORM grammar produces for the
// default model: the users table, a parameterised WHERE, and - critically -
// an UPDATE that touches only remember_token. The default model composes
// orm.IDInt without orm.Timestamps precisely so token rotation does not
// start stamping users.updated_at on every remember-me recall.
func TestProvider_EmitsQueryEvents(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)

	var (
		mu       sync.Mutex
		executed []*orm.QueryExecuted
	)
	m.SetEventDispatcher(func(_ context.Context, ev any) error {
		mu.Lock()
		defer mu.Unlock()
		if q, ok := ev.(*orm.QueryExecuted); ok {
			executed = append(executed, q)
		}
		return nil
	})

	seen := func(needle string) []*orm.QueryExecuted {
		// Statement events are delivered asynchronously so a listener can
		// never stall a database call; force delivery before asserting.
		if err := m.FlushQueryEvents(context.Background()); err != nil {
			t.Fatalf("FlushQueryEvents: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		var out []*orm.QueryExecuted
		for _, q := range executed {
			if strings.Contains(q.SQL, needle) {
				out = append(out, q)
			}
		}
		return out
	}

	p := newProvider(t)
	ctx := context.Background()

	user, err := p.FindByCredentialsCtx(ctx, map[string]interface{}{"email": testEmail})
	if err != nil {
		t.Fatalf("FindByCredentialsCtx: %v", err)
	}
	byCreds := seen("FROM `users`")
	if len(byCreds) != 1 {
		t.Fatalf("credentials lookup: want 1 QueryExecuted, got %d", len(byCreds))
	}
	if !strings.Contains(byCreds[0].SQL, "`email` = ?") {
		t.Errorf("credentials SQL = %q, want a parameterised email predicate", byCreds[0].SQL)
	}
	if byCreds[0].Connection != "sqlite" {
		t.Errorf("Connection = %q, want sqlite", byCreds[0].Connection)
	}
	if byCreds[0].Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", byCreds[0].Duration)
	}

	if _, err := p.FindByIDCtx(ctx, user.GetAuthIdentifier()); err != nil {
		t.Fatalf("FindByIDCtx: %v", err)
	}
	if got := len(seen("`id` = ?")); got == 0 {
		t.Error("id lookup emitted no QueryExecuted")
	}

	if err := p.UpdateRememberTokenCtx(ctx, user, "token-1"); err != nil {
		t.Fatalf("UpdateRememberTokenCtx: %v", err)
	}
	updates := seen("UPDATE `users`")
	if len(updates) != 1 {
		t.Fatalf("remember-token update: want 1 QueryExecuted, got %d", len(updates))
	}
	if !strings.Contains(updates[0].SQL, "`remember_token`") {
		t.Errorf("update SQL = %q, want it to set remember_token", updates[0].SQL)
	}
	if strings.Contains(updates[0].SQL, "updated_at") {
		t.Errorf("update SQL = %q, want no updated_at stamp on the default auth model", updates[0].SQL)
	}

	swapped, err := p.(auth.RememberTokenCompareAndSwapper).
		CompareAndSwapRememberToken(ctx, user, "token-1", "token-2")
	if err != nil {
		t.Fatalf("CompareAndSwapRememberToken: %v", err)
	}
	if !swapped {
		t.Fatal("CompareAndSwapRememberToken reported no swap")
	}
	updates = seen("UPDATE `users`")
	if len(updates) != 2 {
		t.Fatalf("remember-token writes: want 2 QueryExecuted total, got %d", len(updates))
	}
	// The swap must be conditional on the old token, in one statement.
	if !strings.Contains(updates[1].SQL, "`remember_token` = ?") {
		t.Errorf("swap SQL = %q, want a conditional predicate on the old token", updates[1].SQL)
	}
}

// TestProvider_EmitsQueryFailed covers the failure side of the same path: a
// statement against a missing table must surface as query.failed rather than
// as a zero-row success.
func TestProvider_EmitsQueryFailed(t *testing.T) {
	m := newManager(t) // no users table on this connection

	var (
		mu       sync.Mutex
		failed   []*orm.QueryFailed
		executed int
	)
	m.SetEventDispatcher(func(_ context.Context, ev any) error {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case *orm.QueryFailed:
			failed = append(failed, e)
		case *orm.QueryExecuted:
			executed++
		}
		return nil
	})

	if _, err := newProvider(t).FindByCredentialsCtx(context.Background(), map[string]interface{}{
		"email": testEmail,
	}); err == nil {
		t.Fatal("expected the lookup to fail against a missing table")
	}
	if err := m.FlushQueryEvents(context.Background()); err != nil {
		t.Fatalf("FlushQueryEvents: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(failed) != 1 {
		t.Fatalf("want 1 QueryFailed, got %d", len(failed))
	}
	if executed != 0 {
		t.Errorf("failing lookup also emitted %d QueryExecuted events", executed)
	}
	if !strings.Contains(failed[0].Query, "FROM `users`") {
		t.Errorf("Query = %q, want the provider statement", failed[0].Query)
	}
	if failed[0].Error == "" {
		t.Error("Error is empty")
	}
}
