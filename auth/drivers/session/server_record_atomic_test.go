package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/sessionclock"
)

// countingRecords counts the writes a ServerStore makes to its record
// store, and fails UpdateData while failUpdate is set.
type countingRecords struct {
	auth.ServerSessionStore
	updates    atomic.Int32
	deletes    atomic.Int32
	failUpdate atomic.Bool
}

var errRecordsOffline = errors.New("test: record store offline")

func (c *countingRecords) UpdateData(ctx context.Context, id string, update func(map[string]any) (map[string]any, error), lastSeen, expiresAt time.Time) error {
	c.updates.Add(1)
	if c.failUpdate.Load() {
		return errRecordsOffline
	}
	return c.ServerSessionStore.UpdateData(ctx, id, update, lastSeen, expiresAt)
}

func (c *countingRecords) Delete(ctx context.Context, id string) error {
	c.deletes.Add(1)
	return c.ServerSessionStore.Delete(ctx, id)
}

// flashFixture saves one session holding the given flash and returns its
// store and id.
func flashFixture(t *testing.T, records auth.ServerSessionStore, flash map[string]any) (*ServerStore, string) {
	t.Helper()
	store, err := NewServerStore(testConfig(), records)
	if err != nil {
		t.Fatalf("NewServerStore: %v", err)
	}
	first, _ := store.Create("")
	first.Put("cart", "one item")
	for k, v := range flash {
		first.Flash(k, v)
	}
	if err := first.Save(httptest.NewRecorder()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return store, first.ID()
}

// N requests of one session that all loaded it before any read its flash:
// exactly one delivers the value, the others get nil, on every record
// store. Each then saves late, and the flash stays consumed.
func TestServerSession_GetFlash_OneOfNConcurrentReadersDelivers(t *testing.T) {
	const readers = 16
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, id := flashFixture(t, backend.new(t), map[string]any{"status": "saved", "other": "kept"})
			sessions := make([]contract.Session, readers)
			for i := range sessions {
				sessions[i] = loadSession(t, store, id)
			}
			var delivered atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for _, s := range sessions {
				wg.Go(func() {
					<-start
					switch got := s.GetFlash("status"); got {
					case nil:
					case "saved":
						delivered.Add(1)
					default:
						t.Errorf("GetFlash = %v, want saved or nil", got)
					}
					s.Put("touched", "yes")
					if err := s.Save(httptest.NewRecorder()); err != nil {
						t.Errorf("Save: %v", err)
					}
				})
			}
			close(start)
			wg.Wait()
			if got := delivered.Load(); got != 1 {
				t.Fatalf("%d of %d concurrent readers delivered the flash, want exactly 1", got, readers)
			}
			after := loadSession(t, store, id)
			if got := after.GetFlash("status"); got != nil {
				t.Fatalf("the consumed flash came back after the late saves: %v", got)
			}
			if got := after.GetFlash("other"); got != "kept" {
				t.Fatalf("a flash nobody read = %v, want kept", got)
			}
		})
	}
}

// FlushFlash takes the record's whole flash section in one step: of N
// concurrent flushes exactly one gets each stored value.
func TestServerSession_FlushFlash_OneOfNConcurrentFlushesDelivers(t *testing.T) {
	const readers = 16
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, id := flashFixture(t, backend.new(t), map[string]any{"status": "saved", "warning": "low stock"})
			sessions := make([]contract.Session, readers)
			for i := range sessions {
				sessions[i] = loadSession(t, store, id)
			}
			var status, warning atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for _, s := range sessions {
				wg.Go(func() {
					<-start
					out := s.FlushFlash()
					if out["status"] == "saved" {
						status.Add(1)
					}
					if out["warning"] == "low stock" {
						warning.Add(1)
					}
					if err := s.Save(httptest.NewRecorder()); err != nil {
						t.Errorf("Save: %v", err)
					}
				})
			}
			close(start)
			wg.Wait()
			if status.Load() != 1 || warning.Load() != 1 {
				t.Fatalf("status delivered %d times, warning %d times, want each exactly once", status.Load(), warning.Load())
			}
			if out := loadSession(t, store, id).FlushFlash(); out != nil {
				t.Fatalf("the flushed flash came back: %v", out)
			}
		})
	}
}

// A request that read a flash and saves late does not erase the flash
// another request set under the same key in between.
func TestServerSession_LateSaveKeepsAFlashSetAfterItsRead(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, id := flashFixture(t, backend.new(t), map[string]any{"status": "first"})

			a := loadSession(t, store, id)
			if got := a.GetFlash("status"); got != "first" {
				t.Fatalf("flash = %v, want first", got)
			}

			b := loadSession(t, store, id)
			b.Flash("status", "second")
			if err := b.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save b: %v", err)
			}

			a.Put("draft", "hello")
			if err := a.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save a: %v", err)
			}

			after := loadSession(t, store, id)
			if got := after.GetFlash("status"); got != "second" {
				t.Fatalf("flash after the late save = %v, want second (set after the first was read)", got)
			}
			if after.Get("draft") != "hello" {
				t.Fatalf("the late save lost its own change: draft=%v", after.Get("draft"))
			}
		})
	}
}

// Save carries only what its request did to the flash: a flash the request
// loaded and never read stays, and so does one another request set since.
func TestServerSession_SaveNeverDeletesAFlashItDidNotConsume(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, id := flashFixture(t, backend.new(t), map[string]any{"status": "saved"})

			a := loadSession(t, store, id)

			b := loadSession(t, store, id)
			b.Flash("notice", "new message")
			if err := b.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save b: %v", err)
			}

			a.Put("draft", "hello")
			a.Flash("own", "mine")
			if err := a.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save a: %v", err)
			}

			out := loadSession(t, store, id).FlushFlash()
			if out["status"] != "saved" || out["notice"] != "new message" || out["own"] != "mine" || len(out) != 3 {
				t.Fatalf("flash after a save that read none = %v, want status, notice and own", out)
			}
		})
	}
}

// The loaded snapshot decides whether the store is asked: a request that
// reads no stored flash makes no store call for it, and a flash set during
// the request is consumed in memory.
func TestServerSession_FlashReadsCostNoStoreCallWithoutStoredFlash(t *testing.T) {
	inner := NewMemoryStore()
	t.Cleanup(func() { _ = inner.Close(context.Background()) })
	records := &countingRecords{ServerSessionStore: inner}
	store, id := flashFixture(t, records, nil)

	s := loadSession(t, store, id)
	before := records.updates.Load()
	if got := s.GetFlash("status"); got != nil {
		t.Fatalf("GetFlash on an empty bag = %v", got)
	}
	if out := s.FlushFlash(); out != nil {
		t.Fatalf("FlushFlash on an empty bag = %v", out)
	}
	s.Flash("status", "set now")
	if got := s.GetFlash("status"); got != "set now" {
		t.Fatalf("a flash set in this request = %v, want set now", got)
	}
	s.Flash("again", "set now")
	if out := s.FlushFlash(); len(out) != 1 || out["again"] != "set now" {
		t.Fatalf("FlushFlash of this request's flash = %v", out)
	}
	if got := records.updates.Load() - before; got != 0 {
		t.Fatalf("%d store writes for flash reads with no stored flash, want 0", got)
	}

	// A session with no record yet consumes in memory too.
	fresh, _ := store.Create("")
	fresh.Flash("status", "unsaved")
	if got := fresh.GetFlash("status"); got != "unsaved" {
		t.Fatalf("unsaved session flash = %v", got)
	}
	if got := records.updates.Load() - before; got != 0 {
		t.Fatalf("%d store writes for an unsaved session's flash, want 0", got)
	}
}

// A value this request set over a loaded key is the request's own: it is
// returned, and FlushFlash lays this request's values over the record's.
func TestServerSession_FlashSetInTheRequestWinsOverTheLoadedValue(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, id := flashFixture(t, backend.new(t), map[string]any{"status": "stored", "other": "stored too"})

			s := loadSession(t, store, id)
			s.Flash("status", "replaced")
			if got := s.GetFlash("status"); got != "replaced" {
				t.Fatalf("GetFlash = %v, want the value this request set", got)
			}
			s.Flash("other", "replaced too")
			s.Flash("fresh", "new")
			out := s.FlushFlash()
			if out["other"] != "replaced too" || out["fresh"] != "new" {
				t.Fatalf("FlushFlash = %v, want this request's values", out)
			}
			if err := s.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save: %v", err)
			}
			if out := loadSession(t, store, id).FlushFlash(); out != nil {
				t.Fatalf("flash left after it was consumed: %v", out)
			}
		})
	}
}

// When the store cannot run the step nothing is consumed: the reader gets
// nil and the record still holds the flash for the next request.
func TestServerSession_FlashStoreFailureConsumesNothing(t *testing.T) {
	inner := NewMemoryStore()
	t.Cleanup(func() { _ = inner.Close(context.Background()) })
	records := &countingRecords{ServerSessionStore: inner}
	store, id := flashFixture(t, records, map[string]any{"status": "saved"})

	s := loadSession(t, store, id)
	records.failUpdate.Store(true)
	if got := s.GetFlash("status"); got != nil {
		t.Fatalf("GetFlash with the store offline = %v, want nil", got)
	}
	if out := s.FlushFlash(); out != nil {
		t.Fatalf("FlushFlash with the store offline = %v, want nil", out)
	}
	records.failUpdate.Store(false)

	if got := loadSession(t, store, id).GetFlash("status"); got != "saved" {
		t.Fatalf("flash after the outage = %v, want saved (nothing was consumed)", got)
	}
	// The record is gone (revoked): nothing is delivered either.
	store2, id2 := flashFixture(t, records, map[string]any{"status": "saved"})
	revoked := loadSession(t, store2, id2)
	if err := inner.Delete(context.Background(), id2); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := revoked.GetFlash("status"); got != nil {
		t.Fatalf("GetFlash on a revoked session = %v, want nil", got)
	}
}

// renewDuringDeleteIf lands a renewal of the record between a conditional
// delete's caller deciding (on its earlier read) and the store's own
// decision: the write another request makes in that window.
type renewDuringDeleteIf struct {
	auth.ServerSessionStore
	renew func()
	once  sync.Once
}

func (s *renewDuringDeleteIf) DeleteIf(ctx context.Context, id string, cond func(*auth.SessionMeta) bool) (bool, error) {
	s.once.Do(s.renew)
	return s.ServerSessionStore.DeleteIf(ctx, id, cond)
}

// ServerStore.Get retires a record the lifetime policy ended, but decides
// on the record the store holds when the removal lands: one a request that
// loaded the session in time renewed after Get's read is kept. The load is
// refused either way. An idle record nobody renewed is removed.
func TestServerStore_GetRetiresAPolicyEndedRecordOnlyIfStillEnded(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			now := time.Now()
			defer sessionclock.Set(func() time.Time { return now })()
			inner := backend.new(t)
			seed, id := flashFixture(t, inner, nil)

			// A request that loaded the session in time, and saves after
			// the second request's read.
			inTime := loadSession(t, seed, id)
			records := &renewDuringDeleteIf{ServerSessionStore: inner}
			records.renew = func() {
				inTime.Put("draft", "hello")
				if err := inTime.Save(httptest.NewRecorder()); err != nil {
					t.Errorf("the renewal: %v", err)
				}
			}
			store, err := NewServerStore(testConfig(), records)
			if err != nil {
				t.Fatalf("NewServerStore: %v", err)
			}

			// Past the policy's idle end, inside the record's grace.
			now = now.Add(120*time.Minute + 30*time.Second)
			got, err := store.Get(httptest.NewRequest(http.MethodGet, "/", nil), id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.ID() == id {
				t.Fatal("a record read past the policy end still loaded its session")
			}
			if loadSession(t, store, id).Get("draft") != "hello" {
				t.Fatal("the record renewed after the read lost its save")
			}

			// Nobody renews it now: the next refused load removes it.
			now = now.Add(120*time.Minute + 30*time.Second)
			if got, _ := store.Get(httptest.NewRequest(http.MethodGet, "/", nil), id); got.ID() == id {
				t.Fatal("an idle record still loaded its session")
			}
			if _, err := inner.Get(context.Background(), id); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("the idle record after the refused load: %v, want ErrSessionNotFound", err)
			}
		})
	}
}

// MemoryStore.Get removes an expired record only if the record the store
// holds when the removal runs is still expired: one written under the id
// after Get's read stays.
func TestMemoryStore_GetKeepsARecordRenewedAfterItsRead(t *testing.T) {
	s := NewMemoryStore()
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	ctx := context.Background()
	base := time.Now()
	if err := s.Put(ctx, &auth.StoredSession{ID: "sid", UserID: "u1", ExpiresAt: base.Add(time.Hour)}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The store reads its clock after it took the snapshot: the renewal
	// lands there, between Get's read and its removal.
	late := base.Add(2 * time.Hour)
	// (Put reads the clock too, so the hook must not wait on itself.)
	var renewed atomic.Bool
	s.clock = func() time.Time {
		if renewed.CompareAndSwap(false, true) {
			if err := s.Put(ctx, &auth.StoredSession{ID: "sid", UserID: "u1", ExpiresAt: late.Add(time.Hour)}); err != nil {
				t.Errorf("renewing Put: %v", err)
			}
		}
		return late
	}
	if _, err := s.Get(ctx, "sid"); !errors.Is(err, auth.ErrSessionExpired) {
		t.Fatalf("Get = %v, want ErrSessionExpired for the record it read", err)
	}
	rec, err := s.Get(ctx, "sid")
	if err != nil {
		t.Fatalf("the record renewed after the read was removed: %v", err)
	}
	if !rec.ExpiresAt.Equal(late.Add(time.Hour)) {
		t.Fatalf("record ExpiresAt = %v, want the renewal's", rec.ExpiresAt)
	}
	if metas, _ := s.ListForUser(ctx, "u1"); len(metas) != 1 {
		t.Fatalf("the renewed record left the user index: %v", metas)
	}
}

// CacheStore evicts through a compare-and-delete against the bytes it
// read: a record another instance wrote under the id after the read (its
// clock had not reached the expiry, or the id was put again) stays, with
// its index membership.
func TestCacheStore_EvictKeepsARecordWrittenAfterItsRead(t *testing.T) {
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			backend := bf.new(t)
			s := newCacheStore(t, backend)
			other := newCacheStore(t, backend)
			ctx := context.Background()
			if err := s.Put(ctx, cacheSession("sid", "u1")); err != nil {
				t.Fatalf("Put: %v", err)
			}

			// The store reads its clock after it read the record: the
			// other instance's write lands there, before the eviction.
			late := time.Now().Add(2 * time.Hour)
			var once sync.Once
			s.clock = func() time.Time {
				once.Do(func() {
					renewed := cacheSession("sid", "u1")
					renewed.ExpiresAt = late.Add(time.Hour)
					renewed.Data = map[string]any{"k": "renewed"}
					if err := other.Put(ctx, renewed); err != nil {
						t.Errorf("renewing Put: %v", err)
					}
				})
				return late
			}
			if _, err := s.Get(ctx, "sid"); !errors.Is(err, auth.ErrSessionExpired) {
				t.Fatalf("Get = %v, want ErrSessionExpired for the record it read", err)
			}
			rec, err := s.Get(ctx, "sid")
			if err != nil {
				t.Fatalf("the record written after the read was evicted: %v", err)
			}
			if rec.Data["k"] != "renewed" {
				t.Fatalf("record data = %v, want the renewal's", rec.Data)
			}
			if !listedIDs(t, s, "u1")["sid"] {
				t.Fatal("the renewed record left the user index")
			}
		})
	}
}

// N instances that all read one expired record and evict it at once: the
// record goes exactly once and nothing else is touched.
func TestCacheStore_ConcurrentEvictionsOfOneExpiredRecord(t *testing.T) {
	const callers = 16
	for _, bf := range sharedBackends() {
		t.Run(bf.name, func(t *testing.T) {
			backend := bf.new(t)
			ctx := context.Background()
			seed := newCacheStore(t, backend)
			if err := seed.Put(ctx, cacheSession("old", "u1")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := seed.Put(ctx, cacheSession("live", "u1")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			raw, ok := backend.GetStringCtx(ctx, cacheMetaKey("old"))
			if !ok {
				t.Fatal("seed record missing")
			}
			rec, err := decodeRecord(raw)
			if err != nil {
				t.Fatalf("decodeRecord: %v", err)
			}
			var wg sync.WaitGroup
			start := make(chan struct{})
			for range callers {
				s := newCacheStore(t, backend)
				wg.Go(func() {
					<-start
					s.evict(ctx, raw, rec)
				})
			}
			close(start)
			wg.Wait()
			if _, ok := backend.GetStringCtx(ctx, cacheMetaKey("old")); ok {
				t.Fatal("the evicted record is still in the backend")
			}
			got := listedIDs(t, seed, "u1")
			if got["old"] || !got["live"] || len(got) != 1 {
				t.Fatalf("index after the evictions = %v, want only live", got)
			}
		})
	}
}

// signedInFixture saves a signed-in session the way a sign-in does (the
// record with its owner first, then the session into it) and returns its
// store and id.
func signedInFixture(t *testing.T, records auth.ServerSessionStore, userID string) (*ServerStore, string) {
	t.Helper()
	store, err := NewServerStore(testConfig(), records)
	if err != nil {
		t.Fatalf("NewServerStore: %v", err)
	}
	first, _ := store.Create("")
	if err := records.Put(context.Background(), &auth.StoredSession{
		ID: first.ID(), UserID: userID, IPAddress: "10.0.0.1", UserAgent: "test-agent/1.0",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	first.Put(auth.UserIDSessionKey, userID)
	first.Put("cart", "one item")
	if err := first.Save(httptest.NewRecorder()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return store, first.ID()
}

// N requests that loaded one signed-out session and each regenerate it:
// every one gets its own successor record holding the payload it saw, and
// the old id is retired (the later retirements find nothing, which is not
// an error).
func TestServerSession_ConcurrentRegeneratesOfAVisitorSessionEachGetASuccessor(t *testing.T) {
	const requests = 8
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			inner := backend.new(t)
			store, oldID := flashFixture(t, inner, nil)
			sessions := make([]contract.Session, requests)
			for i := range sessions {
				sessions[i] = loadSession(t, store, oldID)
			}
			var wg sync.WaitGroup
			start := make(chan struct{})
			for _, s := range sessions {
				wg.Go(func() {
					<-start
					if err := s.Regenerate(); err != nil {
						t.Errorf("Regenerate: %v", err)
						return
					}
					if err := s.Save(httptest.NewRecorder()); err != nil {
						t.Errorf("Save: %v", err)
					}
				})
			}
			close(start)
			wg.Wait()

			if _, err := inner.Get(context.Background(), oldID); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("the old id's record: %v, want ErrSessionNotFound", err)
			}
			seen := map[string]bool{}
			for _, s := range sessions {
				id := s.ID()
				if id == oldID || seen[id] {
					t.Fatalf("successor id %q is the old id or a duplicate", id)
				}
				seen[id] = true
				if got := loadSession(t, store, id).Get("cart"); got != "one item" {
					t.Fatalf("successor %q lost the payload its request saw: cart=%v", id, got)
				}
			}
		})
	}
}

// A signed-in session regenerated on its own (no sign-in wrote a record
// for the new id) keeps its record: Save moves it to the new id with its
// owner, and the old id is gone.
func TestServerSession_RegenerateOfASignedInSessionMovesItsRecord(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			inner := backend.new(t)
			store, oldID := signedInFixture(t, inner, "u1")

			s := loadSession(t, store, oldID)
			if err := s.Regenerate(); err != nil {
				t.Fatalf("Regenerate: %v", err)
			}
			w := httptest.NewRecorder()
			if err := s.Save(w); err != nil {
				t.Fatalf("Save after Regenerate signed the user out: %v", err)
			}
			if sessionCookieOf(w, testConfig().Name) == "" {
				t.Fatal("no cookie for the new id")
			}
			if _, err := inner.Get(context.Background(), oldID); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("the old id's record: %v, want ErrSessionNotFound", err)
			}
			rec, err := inner.Get(context.Background(), s.ID())
			if err != nil {
				t.Fatalf("the successor record: %v", err)
			}
			if rec.UserID != "u1" || rec.IPAddress != "10.0.0.1" || rec.UserAgent != "test-agent/1.0" {
				t.Fatalf("successor record = %+v, want the old record's owner, address and user agent", rec)
			}
			after := loadSession(t, store, s.ID())
			if after.Get(auth.UserIDSessionKey) != "u1" || after.Get("cart") != "one item" {
				t.Fatalf("successor lost the session: user=%v cart=%v", after.Get(auth.UserIDSessionKey), after.Get("cart"))
			}
			list, err := inner.ListForUser(context.Background(), "u1")
			if err != nil || len(list) != 1 || list[0].ID != s.ID() {
				t.Fatalf("the user's sessions = %v (%v), want only the successor", list, err)
			}
			// The moved session saves again like any other.
			after.Put("draft", "hello")
			if err := after.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save of the moved session: %v", err)
			}
		})
	}
}

// N requests that loaded one signed-in session and each regenerate it on
// their own: the record moves once. Exactly one Save succeeds and keeps its
// successor; the others fail with auth.ErrSessionNotFound, write no cookie
// and leave no record behind.
func TestServerSession_ConcurrentRegeneratesOfASignedInSessionMoveItOnce(t *testing.T) {
	const requests = 8
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			inner := backend.new(t)
			store, oldID := signedInFixture(t, inner, "u1")
			sessions := make([]contract.Session, requests)
			for i := range sessions {
				sessions[i] = loadSession(t, store, oldID)
			}
			var saved atomic.Int32
			var winner atomic.Value
			var wg sync.WaitGroup
			start := make(chan struct{})
			for _, s := range sessions {
				wg.Go(func() {
					<-start
					if err := s.Regenerate(); err != nil {
						t.Errorf("Regenerate: %v", err)
						return
					}
					w := httptest.NewRecorder()
					err := s.Save(w)
					switch {
					case err == nil:
						saved.Add(1)
						winner.Store(s.ID())
					case errors.Is(err, auth.ErrSessionNotFound):
						if sessionCookieOf(w, testConfig().Name) != "" {
							t.Error("a save that lost the move wrote a cookie")
						}
					default:
						t.Errorf("Save: %v", err)
					}
				})
			}
			close(start)
			wg.Wait()

			if got := saved.Load(); got != 1 {
				t.Fatalf("%d of %d regenerating requests kept a successor, want exactly 1", got, requests)
			}
			list, err := inner.ListForUser(context.Background(), "u1")
			if err != nil || len(list) != 1 || list[0].ID != winner.Load() {
				t.Fatalf("the user's sessions = %v (%v), want only the winner %v", list, err, winner.Load())
			}
		})
	}
}

// A session revoked after it was loaded gets no successor: a regenerate
// and save after the revocation fails and leaves no record.
func TestServerSession_RegenerateAfterRevocationCreatesNoSuccessor(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			inner := backend.new(t)
			store, oldID := signedInFixture(t, inner, "u1")
			s := loadSession(t, store, oldID)
			if err := inner.DeleteAllForUser(context.Background(), "u1"); err != nil {
				t.Fatalf("DeleteAllForUser: %v", err)
			}
			if err := s.Regenerate(); err != nil {
				t.Fatalf("Regenerate: %v", err)
			}
			if err := s.Save(httptest.NewRecorder()); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("Save of a revoked session after Regenerate = %v, want ErrSessionNotFound", err)
			}
			if _, err := inner.Get(context.Background(), s.ID()); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("a revoked session got a successor record: %v", err)
			}
			if list, _ := inner.ListForUser(context.Background(), "u1"); len(list) != 0 {
				t.Fatalf("the revoked user's sessions = %v, want none", list)
			}
		})
	}
}

// IssuedAt is read while another goroutine of the request saves.
func TestServerSession_IssuedAtDuringSave(t *testing.T) {
	inner := NewMemoryStore()
	t.Cleanup(func() { _ = inner.Close(context.Background()) })
	store, id := flashFixture(t, inner, nil)
	s := loadSession(t, store, id).(*ServerSession)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			s.Put("k", i)
			if err := s.Save(httptest.NewRecorder()); err != nil {
				t.Errorf("Save: %v", err)
			}
		})
		wg.Go(func() { _ = s.IssuedAt() })
	}
	wg.Wait()
	if s.IssuedAt().IsZero() {
		t.Fatal("IssuedAt is zero after a save")
	}
}

// swapWithoutDelete is a cache backend with the compare-and-swap and the
// set operations but no compare-and-delete: what a driver written for the
// earlier swapper contract offers.
type swapWithoutDelete struct {
	contract.Cache
	contract.CacheSetStore
	swap func(ctx context.Context, key string, expected, value interface{}, ttl time.Duration) (bool, error)
}

func (b swapWithoutDelete) CompareAndSwapCtx(ctx context.Context, key string, expected, value interface{}, ttl time.Duration) (bool, error) {
	return b.swap(ctx, key, expected, value, ttl)
}

// The compare-and-delete the eviction runs is the backend's own: a backend
// that has the compare-and-swap and the set operations but not the
// compare-and-delete is refused at construction.
func TestNewCacheStore_BackendWithoutCompareAndDelete(t *testing.T) {
	b := drivers.NewMemoryStore("sessions")
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	backend := swapWithoutDelete{Cache: b, CacheSetStore: b, swap: b.CompareAndSwapCtx}
	if _, ok := contract.Cache(backend).(interface {
		CompareAndDeleteCtx(context.Context, string, interface{}) (bool, error)
	}); ok {
		t.Fatal("fixture: the backend has a compare-and-delete")
	}
	if _, err := NewCacheStore(backend); !errors.Is(err, ErrCacheStoreUnsupported) {
		t.Fatalf("NewCacheStore over a backend without the compare-and-delete = %v, want ErrCacheStoreUnsupported", err)
	}
}
