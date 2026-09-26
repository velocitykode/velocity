// Package authtest provides executable specifications (contract tests) for
// [auth.UserStore], [auth.ServerSessionStore], and [contract.LoginThrottler]
// implementations.
//
// Each runner is independent so drivers that only implement one of the three
// can run the relevant runner without forcing implementations of the others.
package authtest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
)

// UserStoreFactory builds a fresh user store seeded with a known user per
// sub-test. SeedUser is the user the runner asks the user store to look up;
// it must be findable by id and by the credentials map
// {"email": SeedEmail, "password": SeedPassword}.
type UserStoreFactory struct {
	// New returns a fresh user store seeded with the SeedUser (or an empty
	// store if the runner only exercises miss paths).
	New func(t *testing.T) auth.UserStore
	// SeedUser is the user the seeded user store must return.
	SeedUser auth.Authenticatable
	// SeedEmail is the email key in the credentials map.
	SeedEmail string
	// SeedPassword is the plaintext password the user store must accept
	// for the seeded user.
	SeedPassword string
}

// RunUserStoreContractTests exercises [auth.UserStore].
func RunUserStoreContractTests(t *testing.T, f UserStoreFactory) {
	t.Helper()

	t.Run("FindByIDCtx_KnownID_ReturnsUser", func(t *testing.T) {
		p := f.New(t)
		got, err := p.FindByIDCtx(context.Background(), f.SeedUser.GetAuthIdentifier())
		if err != nil {
			t.Fatalf("FindByIDCtx: %v", err)
		}
		if got == nil || got.GetAuthIdentifier() != f.SeedUser.GetAuthIdentifier() {
			t.Fatalf("FindByIDCtx returned wrong user: %v", got)
		}
	})

	t.Run("FindByIDCtx_UnknownID_ReturnsErrUserNotFound", func(t *testing.T) {
		p := f.New(t)
		_, err := p.FindByIDCtx(context.Background(), "definitely-not-a-real-id")
		if err == nil {
			t.Fatal("expected error for unknown ID, got nil")
		}
		if !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("expected ErrUserNotFound, got %v", err)
		}
	})

	t.Run("FindByIDCtx_NilID_ReturnsErrUserNotFound", func(t *testing.T) {
		p := f.New(t)
		_, err := p.FindByIDCtx(context.Background(), nil)
		if err == nil {
			t.Fatal("expected error for nil ID, got nil")
		}
		if !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("expected ErrUserNotFound, got %v", err)
		}
	})

	t.Run("FindByCredentialsCtx_ValidEmail_ReturnsUser", func(t *testing.T) {
		p := f.New(t)
		got, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{
			"email": f.SeedEmail,
		})
		if err != nil {
			t.Fatalf("FindByCredentialsCtx: %v", err)
		}
		if got == nil || got.GetAuthIdentifier() != f.SeedUser.GetAuthIdentifier() {
			t.Fatalf("FindByCredentialsCtx returned wrong user: %v", got)
		}
	})

	t.Run("FindByCredentialsCtx_UnknownEmail_ReturnsErrUserNotFound", func(t *testing.T) {
		p := f.New(t)
		_, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{
			"email": "ghost@example.com",
		})
		if err == nil {
			t.Fatal("expected error for unknown email, got nil")
		}
		if !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("expected ErrUserNotFound, got %v", err)
		}
	})

	t.Run("ValidateCredentials_CorrectPassword_True", func(t *testing.T) {
		p := f.New(t)
		got, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{
			"email": f.SeedEmail,
		})
		if err != nil {
			t.Fatalf("seed lookup: %v", err)
		}
		ok := p.ValidateCredentials(got, map[string]interface{}{"password": f.SeedPassword})
		if !ok {
			t.Fatal("expected ValidateCredentials=true for correct password")
		}
	})

	t.Run("ValidateCredentials_WrongPassword_False", func(t *testing.T) {
		p := f.New(t)
		got, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{
			"email": f.SeedEmail,
		})
		if err != nil {
			t.Fatalf("seed lookup: %v", err)
		}
		ok := p.ValidateCredentials(got, map[string]interface{}{"password": "definitely-wrong"})
		if ok {
			t.Fatal("expected ValidateCredentials=false for wrong password")
		}
	})

	t.Run("ValidateCredentials_NoPassword_False", func(t *testing.T) {
		p := f.New(t)
		got, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{
			"email": f.SeedEmail,
		})
		if err != nil {
			t.Fatalf("seed lookup: %v", err)
		}
		// Missing or wrong-type password must collapse to false; the
		// user store must NOT panic.
		if p.ValidateCredentials(got, map[string]interface{}{}) {
			t.Fatal("expected false when no password supplied")
		}
		if p.ValidateCredentials(got, map[string]interface{}{"password": 1234}) {
			t.Fatal("expected false when password is non-string")
		}
	})
}

// ServerSessionStoreFactory returns a fresh empty store per sub-test.
type ServerSessionStoreFactory func(t *testing.T) auth.ServerSessionStore

// RunServerSessionStoreContractTests exercises [auth.ServerSessionStore].
func RunServerSessionStoreContractTests(t *testing.T, factory ServerSessionStoreFactory) {
	t.Helper()

	makeSession := func(id, userID string) *auth.StoredSession {
		now := time.Now()
		return &auth.StoredSession{
			ID:         id,
			UserID:     userID,
			Data:       map[string]any{"k": "v"},
			CreatedAt:  now,
			LastSeenAt: now,
			ExpiresAt:  now.Add(time.Hour),
			IPAddress:  "127.0.0.1",
			UserAgent:  "contract-test",
		}
	}

	t.Run("Get_UnknownID_ReturnsErrSessionNotFound", func(t *testing.T) {
		s := factory(t)
		_, err := s.Get(context.Background(), "no-such-id")
		if err == nil {
			t.Fatal("expected error for unknown id")
		}
		if !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("expected ErrSessionNotFound, got %v", err)
		}
	})

	t.Run("Put_Then_Get_RoundTripsSession", func(t *testing.T) {
		s := factory(t)
		sess := makeSession("sess-1", "user-1")
		if err := s.Put(context.Background(), sess); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, err := s.Get(context.Background(), "sess-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.UserID != "user-1" {
			t.Fatalf("UserID mismatch: %q", got.UserID)
		}
	})

	t.Run("Get_ExpiredSession_ReturnsErrSessionExpired", func(t *testing.T) {
		s := factory(t)
		sess := makeSession("expired-1", "user-1")
		sess.ExpiresAt = time.Now().Add(-time.Hour)
		_ = s.Put(context.Background(), sess)
		_, err := s.Get(context.Background(), "expired-1")
		if err == nil {
			t.Fatal("expected error for expired session")
		}
		// The interface doc promises ErrSessionExpired specifically for
		// records past ExpiresAt (the store also removes the record).
		// Collapsing to ErrSessionNotFound would lose the "lease expired"
		// vs. "never existed" distinction admin telemetry relies on.
		if !errors.Is(err, auth.ErrSessionExpired) {
			t.Fatalf("expected ErrSessionExpired, got %v", err)
		}
	})

	t.Run("Touch_PresentSession_UpdatesLastSeenAndSlidesExpiry", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		sess := makeSession("touch-1", "user-1")
		sess.LastSeenAt = time.Now().Add(-time.Hour)
		if err := s.Put(ctx, sess); err != nil {
			t.Fatalf("Put: %v", err)
		}
		stamp := time.Now().Add(10 * time.Minute).Truncate(time.Second)
		slid := sess.ExpiresAt.Add(2 * time.Hour).Truncate(time.Second)
		if err := s.Touch(ctx, "touch-1", stamp, slid); err != nil {
			t.Fatalf("Touch: %v", err)
		}
		got, err := s.Get(ctx, "touch-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !got.LastSeenAt.Equal(stamp) {
			t.Fatalf("LastSeenAt = %v, want %v", got.LastSeenAt, stamp)
		}
		// The activity refresh slides the record's expiry: an active
		// session must outlive the ExpiresAt it was created with.
		if !got.ExpiresAt.Equal(slid) {
			t.Fatalf("ExpiresAt = %v, want the slid %v", got.ExpiresAt, slid)
		}
		if got.UserID != "user-1" || got.Data["k"] != "v" || got.CreatedAt.Sub(sess.CreatedAt).Abs() > time.Second {
			t.Fatalf("Touch altered fields other than LastSeenAt and ExpiresAt: %+v", got)
		}
	})

	t.Run("Touch_ExpiredSession_ReturnsErrSessionExpiredAndNeverRevives", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		sess := makeSession("touch-expired", "user-1")
		sess.ExpiresAt = time.Now().Add(-time.Minute)
		_ = s.Put(ctx, sess)
		err := s.Touch(ctx, "touch-expired", time.Now(), time.Now().Add(time.Hour))
		if !errors.Is(err, auth.ErrSessionExpired) {
			t.Fatalf("Touch on an expired record: expected ErrSessionExpired, got %v", err)
		}
		if _, err := s.Get(ctx, "touch-expired"); err == nil {
			t.Fatal("Touch revived an expired session")
		}
	})

	t.Run("Touch_UnknownID_ReturnsErrSessionNotFoundAndNeverInserts", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		err := s.Touch(ctx, "never-existed", time.Now(), time.Now().Add(time.Hour))
		if !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("expected ErrSessionNotFound, got %v", err)
		}
		if _, err := s.Get(ctx, "never-existed"); !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("Touch inserted a record for an unknown id: %v", err)
		}
	})

	t.Run("Touch_AfterDelete_DoesNotResurrect", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.Put(ctx, makeSession("touch-del", "user-1"))
		if err := s.Delete(ctx, "touch-del"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if err := s.Touch(ctx, "touch-del", time.Now(), time.Now().Add(time.Hour)); !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("expected ErrSessionNotFound after Delete, got %v", err)
		}
		if _, err := s.Get(ctx, "touch-del"); !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("Touch resurrected a deleted session: %v", err)
		}
	})

	t.Run("Touch_AfterDeleteAllForUser_DoesNotResurrect", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.Put(ctx, makeSession("touch-bulk-A", "user-bulk"))
		_ = s.Put(ctx, makeSession("touch-bulk-B", "user-bulk"))
		if err := s.DeleteAllForUser(ctx, "user-bulk"); err != nil {
			t.Fatalf("DeleteAllForUser: %v", err)
		}
		for _, id := range []string{"touch-bulk-A", "touch-bulk-B"} {
			if err := s.Touch(ctx, id, time.Now(), time.Now().Add(time.Hour)); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("%s: expected ErrSessionNotFound after bulk revoke, got %v", id, err)
			}
		}
		got, err := s.ListForUser(ctx, "user-bulk")
		if err != nil {
			t.Fatalf("ListForUser: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("Touch resurrected %d bulk-revoked sessions", len(got))
		}
	})

	t.Run("UpdateData_PresentSession_ReplacesDataAndSlides", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		sess := makeSession("data-1", "user-1")
		if err := s.Put(ctx, sess); err != nil {
			t.Fatalf("Put: %v", err)
		}
		stamp := time.Now().Add(5 * time.Minute).Truncate(time.Second)
		slid := sess.ExpiresAt.Add(time.Hour).Truncate(time.Second)
		if err := s.UpdateData(ctx, "data-1", setData(map[string]any{"cart": "three items"}), stamp, slid); err != nil {
			t.Fatalf("UpdateData: %v", err)
		}
		got, err := s.Get(ctx, "data-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Data["cart"] != "three items" || got.Data["k"] != nil {
			t.Fatalf("Data = %v, want exactly the new payload", got.Data)
		}
		if !got.LastSeenAt.Equal(stamp) || !got.ExpiresAt.Equal(slid) {
			t.Fatalf("UpdateData did not slide: LastSeenAt %v ExpiresAt %v, want %v %v", got.LastSeenAt, got.ExpiresAt, stamp, slid)
		}
		if got.UserID != "user-1" || got.IPAddress != sess.IPAddress || got.UserAgent != sess.UserAgent {
			t.Fatalf("UpdateData altered the record's identity fields: %+v", got)
		}
	})

	t.Run("UpdateData_UnknownID_ReturnsErrSessionNotFoundAndNeverInserts", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		err := s.UpdateData(ctx, "never-existed", setData(map[string]any{"k": "v"}), time.Now(), time.Now().Add(time.Hour))
		if !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("expected ErrSessionNotFound, got %v", err)
		}
		if _, err := s.Get(ctx, "never-existed"); !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("UpdateData inserted a record for an unknown id: %v", err)
		}
	})

	t.Run("UpdateData_AfterRevocation_DoesNotResurrect", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.Put(ctx, makeSession("data-del", "user-1"))
		_ = s.Put(ctx, makeSession("data-bulk", "user-bulk"))
		if err := s.Delete(ctx, "data-del"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if err := s.DeleteAllForUser(ctx, "user-bulk"); err != nil {
			t.Fatalf("DeleteAllForUser: %v", err)
		}
		for _, id := range []string{"data-del", "data-bulk"} {
			if err := s.UpdateData(ctx, id, setData(map[string]any{"k": "v"}), time.Now(), time.Now().Add(time.Hour)); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("%s: expected ErrSessionNotFound after revocation, got %v", id, err)
			}
			if _, err := s.Get(ctx, id); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("%s: UpdateData resurrected a revoked session: %v", id, err)
			}
		}
	})

	t.Run("UpdateData_ExpiredSession_ReturnsErrSessionExpired", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		sess := makeSession("data-expired", "user-1")
		sess.ExpiresAt = time.Now().Add(-time.Minute)
		_ = s.Put(ctx, sess)
		err := s.UpdateData(ctx, "data-expired", setData(map[string]any{"k": "v"}), time.Now(), time.Now().Add(time.Hour))
		if !errors.Is(err, auth.ErrSessionExpired) {
			t.Fatalf("expected ErrSessionExpired, got %v", err)
		}
	})

	t.Run("UpdateData_SeesTheDataTheRecordHolds", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		sess := makeSession("data-seen", "user-1")
		sess.Data = map[string]any{"first": "one"}
		if err := s.Put(ctx, sess); err != nil {
			t.Fatalf("Put: %v", err)
		}
		addKey := func(k string) func(map[string]any) (map[string]any, error) {
			return func(current map[string]any) (map[string]any, error) {
				out := map[string]any{k: "set"}
				for key, v := range current {
					out[key] = v
				}
				return out, nil
			}
		}
		if err := s.UpdateData(ctx, "data-seen", addKey("second"), time.Now(), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("UpdateData: %v", err)
		}
		if err := s.Touch(ctx, "data-seen", time.Now(), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("Touch: %v", err)
		}
		got, err := s.Get(ctx, "data-seen")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Data["first"] != "one" || got.Data["second"] != "set" {
			t.Fatalf("Data = %v, want the update applied to what the record held, kept by Touch", got.Data)
		}
		boom := errors.New("update refused")
		refuse := func(map[string]any) (map[string]any, error) { return nil, boom }
		if err := s.UpdateData(ctx, "data-seen", refuse, time.Now(), time.Now().Add(time.Hour)); !errors.Is(err, boom) {
			t.Fatalf("UpdateData with a failing update = %v, want its error", err)
		}
		if got, _ := s.Get(ctx, "data-seen"); got == nil || got.Data["second"] != "set" {
			t.Fatalf("a failed update changed the record: %v", got)
		}
	})

	t.Run("UpdateData_FailedUpdateLeavesNestedDataUntouched", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		sess := makeSession("data-nested-fail", "user-1")
		sess.Data = map[string]any{
			"data":  map[string]any{"cart": []any{"a", "b"}},
			"flash": map[string]any{"status": "saved"},
		}
		if err := s.Put(ctx, sess); err != nil {
			t.Fatalf("Put: %v", err)
		}
		boom := errors.New("update refused")
		mutateThenFail := func(current map[string]any) (map[string]any, error) {
			if flash, ok := current["flash"].(map[string]any); ok {
				delete(flash, "status")
			}
			if data, ok := current["data"].(map[string]any); ok {
				if cart, ok := data["cart"].([]any); ok && len(cart) > 0 {
					cart[0] = "changed"
				}
				data["added"] = true
			}
			return nil, boom
		}
		if err := s.UpdateData(ctx, "data-nested-fail", mutateThenFail, time.Now(), time.Now().Add(time.Hour)); !errors.Is(err, boom) {
			t.Fatalf("UpdateData with a failing update = %v, want its error", err)
		}
		got, err := s.Get(ctx, "data-nested-fail")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		flash, _ := got.Data["flash"].(map[string]any)
		data, _ := got.Data["data"].(map[string]any)
		cart, _ := data["cart"].([]any)
		if flash["status"] != "saved" || len(cart) == 0 || cart[0] != "a" || data["added"] != nil {
			t.Fatalf("a failed update changed the record through its nested values: %v", got.Data)
		}
	})

	t.Run("UpdateData_StoredDataSharesNothingWithCallers", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		if err := s.Put(ctx, makeSession("data-unshared", "user-1")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		handed := map[string]any{"flash": map[string]any{"status": "saved"}}
		if err := s.UpdateData(ctx, "data-unshared", setData(handed), time.Now(), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("UpdateData: %v", err)
		}
		delete(handed["flash"].(map[string]any), "status")
		got, err := s.Get(ctx, "data-unshared")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		flash, _ := got.Data["flash"].(map[string]any)
		if flash["status"] != "saved" {
			t.Fatalf("changing the tree handed to the store changed the record: %v", got.Data)
		}
		delete(flash, "status")
		again, err := s.Get(ctx, "data-unshared")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if f, _ := again.Data["flash"].(map[string]any); f["status"] != "saved" {
			t.Fatalf("changing a read's nested value changed the record: %v", again.Data)
		}
	})

	t.Run("UpdateData_WriteDuringUpdateIsKept", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		sess := makeSession("data-interleaved", "user-1")
		sess.Data = map[string]any{"flash": map[string]any{"status": "saved"}}
		if err := s.Put(ctx, sess); err != nil {
			t.Fatalf("Put: %v", err)
		}
		// drain empties the flash and keeps every other key.
		drain := func(current map[string]any) (map[string]any, error) {
			out := map[string]any{}
			for k, v := range current {
				out[k] = v
			}
			out["flash"] = map[string]any{}
			return out, nil
		}
		// The first run of the slow update starts another write to the
		// record and gives it the chance to land before this update's own
		// write: a store that serializes writes holds it back until this
		// one is done, a store that swaps against its read must see it and
		// run the update again on the Data it left.
		other := make(chan error, 1)
		var once sync.Once
		slow := func(current map[string]any) (map[string]any, error) {
			once.Do(func() {
				done := make(chan struct{})
				go func() {
					defer close(done)
					other <- s.UpdateData(ctx, "data-interleaved", drain, time.Now(), time.Now().Add(time.Hour))
				}()
				select {
				case <-done:
				case <-time.After(100 * time.Millisecond):
				}
			})
			out := map[string]any{"seen": "slow"}
			for k, v := range current {
				out[k] = v
			}
			return out, nil
		}
		if err := s.UpdateData(ctx, "data-interleaved", slow, time.Now(), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("UpdateData: %v", err)
		}
		if err := <-other; err != nil {
			t.Fatalf("interleaved UpdateData: %v", err)
		}
		got, err := s.Get(ctx, "data-interleaved")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		flash, _ := got.Data["flash"].(map[string]any)
		if len(flash) != 0 || got.Data["seen"] != "slow" {
			t.Fatalf("a write that landed during an update was lost: %v", got.Data)
		}
	})

	t.Run("UpdateData_ConcurrentUpdatesAllLand", func(t *testing.T) {
		const writers = 8
		s := factory(t)
		ctx := context.Background()
		if err := s.Put(ctx, makeSession("data-concurrent", "user-1")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 2*writers)
		for i := 0; i < writers; i++ {
			key := fmt.Sprintf("k%d", i)
			wg.Go(func() {
				errs <- s.UpdateData(ctx, "data-concurrent", func(current map[string]any) (map[string]any, error) {
					out := map[string]any{key: "set"}
					for k, v := range current {
						out[k] = v
					}
					return out, nil
				}, time.Now(), time.Now().Add(time.Hour))
			})
			wg.Go(func() {
				errs <- s.Touch(ctx, "data-concurrent", time.Now(), time.Now().Add(time.Hour))
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent write: %v", err)
			}
		}
		got, err := s.Get(ctx, "data-concurrent")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		for i := 0; i < writers; i++ {
			if got.Data[fmt.Sprintf("k%d", i)] != "set" {
				t.Fatalf("an update was written over by a concurrent one: %v", got.Data)
			}
		}
	})

	t.Run("Put_SignedOutSession_IsStoredButNeverListed", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		sess := makeSession("visitor-1", "")
		if err := s.Put(ctx, sess); err != nil {
			t.Fatalf("Put of a signed-out record: %v", err)
		}
		if err := s.UpdateData(ctx, "visitor-1", setData(map[string]any{"flash": "hi"}), time.Now(), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("UpdateData of a signed-out record: %v", err)
		}
		got, err := s.Get(ctx, "visitor-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.UserID != "" || got.Data["flash"] != "hi" {
			t.Fatalf("signed-out record = %+v", got)
		}
		if list, err := s.ListForUser(ctx, ""); err != nil || len(list) != 0 {
			t.Fatalf("ListForUser(\"\") = %v, %v; a signed-out record is never listed", list, err)
		}
	})

	t.Run("Delete_UnknownID_IsIdempotent", func(t *testing.T) {
		s := factory(t)
		if err := s.Delete(context.Background(), "never-existed"); err != nil {
			t.Fatalf("Delete on absent id must be nil error, got %v", err)
		}
	})

	t.Run("Delete_PresentSession_Removes", func(t *testing.T) {
		s := factory(t)
		sess := makeSession("del-1", "user-1")
		_ = s.Put(context.Background(), sess)
		if err := s.Delete(context.Background(), "del-1"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		_, err := s.Get(context.Background(), "del-1")
		if !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("expected ErrSessionNotFound after Delete, got %v", err)
		}
	})

	t.Run("DeleteAllForUser_RemovesEverySession", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.Put(ctx, makeSession("s-A", "user-bulk"))
		_ = s.Put(ctx, makeSession("s-B", "user-bulk"))
		if err := s.DeleteAllForUser(ctx, "user-bulk"); err != nil {
			t.Fatalf("DeleteAllForUser: %v", err)
		}
		if _, err := s.Get(ctx, "s-A"); !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("s-A survived: %v", err)
		}
		if _, err := s.Get(ctx, "s-B"); !errors.Is(err, auth.ErrSessionNotFound) {
			t.Fatalf("s-B survived: %v", err)
		}
	})

	t.Run("DeleteAllForUser_UnknownUser_IsNoop", func(t *testing.T) {
		s := factory(t)
		if err := s.DeleteAllForUser(context.Background(), "no-sessions-for-this-user"); err != nil {
			t.Fatalf("DeleteAllForUser must be no-op on unknown user, got %v", err)
		}
	})

	t.Run("ListForUser_ReturnsMetaSansData", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.Put(ctx, makeSession("list-1", "user-list"))
		_ = s.Put(ctx, makeSession("list-2", "user-list"))
		got, err := s.ListForUser(ctx, "user-list")
		if err != nil {
			t.Fatalf("ListForUser: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 sessions, got %d", len(got))
		}
		// SessionMeta intentionally omits Data; that is a struct-level
		// invariant rather than a behavioural one. We assert IDs.
		seen := map[string]bool{}
		for _, m := range got {
			seen[m.ID] = true
		}
		if !seen["list-1"] || !seen["list-2"] {
			t.Fatalf("ListForUser missing entries: %v", seen)
		}
	})
}

// LoginThrottlerFactory returns a fresh throttler per sub-test.
type LoginThrottlerFactory func(t *testing.T) contract.LoginThrottler

// RunLoginThrottlerContractTests exercises [contract.LoginThrottler].
func RunLoginThrottlerContractTests(t *testing.T, factory LoginThrottlerFactory) {
	t.Helper()

	t.Run("Allow_FreshKey_ReturnsTrue", func(t *testing.T) {
		th := factory(t)
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		if !th.Allow(r, "fresh-key") {
			t.Fatal("expected Allow=true for fresh key")
		}
	})

	t.Run("RecordSuccess_DoesNotPanic", func(t *testing.T) {
		th := factory(t)
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("RecordSuccess panicked: %v", rec)
			}
		}()
		th.RecordSuccess(r, "any-key")
	})

	t.Run("RecordFailure_DoesNotPanic", func(t *testing.T) {
		th := factory(t)
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("RecordFailure panicked: %v", rec)
			}
		}()
		th.RecordFailure(r, "any-key")
	})

	t.Run("NilRequest_DoesNotPanic", func(t *testing.T) {
		th := factory(t)
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("throttler panicked on nil *http.Request: %v", rec)
			}
		}()
		// Contract.LoginThrottler does not document nil-tolerance; we
		// only check the no-op throttler (and any well-behaved variant)
		// does not panic. Implementations that require r are free to
		// return false / no-op silently.
		_ = th.Allow(nil, "no-req")
		th.RecordFailure(nil, "no-req")
		th.RecordSuccess(nil, "no-req")
	})

	// Optional capability: a throttler implementing contract.LoginDelayer
	// must report no delay for a fresh key and tolerate a nil request.
	t.Run("LoginDelayer_FreshKey_NoDelay", func(t *testing.T) {
		th := factory(t)
		d, ok := th.(contract.LoginDelayer)
		if !ok {
			t.Skip("throttler does not implement contract.LoginDelayer")
		}
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		if got := d.Delay(r, "fresh-delay-key"); got != 0 {
			t.Fatalf("Delay for fresh key = %v, want 0", got)
		}
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("Delay panicked on nil *http.Request: %v", rec)
			}
		}()
		_ = d.Delay(nil, "no-req")
	})

	// Optional capability: a throttler implementing contract.LoginReserver
	// admits a fresh key, counts each reservation, and clears the key on
	// RecordSuccess.
	t.Run("LoginReserver_FreshKey_Reserves", func(t *testing.T) {
		th := factory(t)
		rs, ok := th.(contract.LoginReserver)
		if !ok {
			t.Skip("throttler does not implement contract.LoginReserver")
		}
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		if within, delay := rs.Reserve(r, "fresh-reserve-key"); !within || delay != 0 {
			t.Fatalf("Reserve for fresh key = (%v, %v), want (true, 0)", within, delay)
		}
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("Reserve panicked on nil *http.Request: %v", rec)
			}
		}()
		_, _ = rs.Reserve(nil, "no-req")
	})
}

// setData returns an UpdateData update that stores data as it is.
func setData(data map[string]any) func(map[string]any) (map[string]any, error) {
	return func(map[string]any) (map[string]any, error) { return data, nil }
}
