package schemes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/sessionclock"
	"github.com/velocitykode/velocity/router"
)

var errFirstWriteOffline = errors.New("test: record store offline for the first write")

// firstWriteHeldRecords holds the first record write (the UpdateData a save
// starts with) until release is closed and then fails it; every later call
// goes straight to the store.
type firstWriteHeldRecords struct {
	auth.ServerSessionStore
	// unarmed lets record writes through until the test arms the hold.
	unarmed atomic.Bool
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (r *firstWriteHeldRecords) UpdateData(ctx context.Context, id string, update func(map[string]any) (map[string]any, error), lastSeen, expiresAt time.Time) error {
	if r.unarmed.Load() {
		return r.ServerSessionStore.UpdateData(ctx, id, update, lastSeen, expiresAt)
	}
	held := false
	r.once.Do(func() {
		held = true
		close(r.entered)
		<-r.release
	})
	if held {
		return errFirstWriteOffline
	}
	return r.ServerSessionStore.UpdateData(ctx, id, update, lastSeen, expiresAt)
}

// A save of the request's session is in its store call on another goroutine
// when the response is committed: it has cleared the session's modified mark
// and, its write failing, puts the mark back. The commit does not look at
// the mark in front of the session's save: it saves, behind the save in
// flight, so the session the failed save left unsaved is saved by the
// response, with its cookie, and a write queued behind the save is delivered
// for a session that was saved.
func TestSessionMiddleware_CommitSavesBehindASaveInFlightThatFails(t *testing.T) {
	cfg := teardownConfig()
	inner := session.NewMemoryStore()
	t.Cleanup(func() { _ = inner.Close(context.Background()) })
	records := &firstWriteHeldRecords{ServerSessionStore: inner, entered: make(chan struct{}), release: make(chan struct{})}
	store, err := session.NewServerStore(cfg, records)
	if err != nil {
		t.Fatalf("NewServerStore: %v", err)
	}
	scheme, err := NewSessionScheme(&mockSessionSchemeUserStore{}, cfg, teardownEncryptor(t), WithSessionStore(store))
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}

	// The commit reaching the session's save is the evidence the save in
	// flight is released on; at a commit that skips the save, the handler
	// having written its response is.
	commitSaving := make(chan struct{})
	orig := saveSessionFromMiddleware
	saveSessionFromMiddleware = func(g *SessionScheme, w http.ResponseWriter, s contract.Session) error {
		close(commitSaving)
		return orig(g, w, s)
	}
	t.Cleanup(func() { saveSessionFromMiddleware = orig })

	var (
		firstSave     error
		queuedRan     bool
		sessionID     string
		handlerWrote  = make(chan struct{})
		firstSaveDone = make(chan struct{})
	)
	r := router.New()
	r.Use(scheme.SessionMiddleware())
	r.Get("/", func(c *router.Context) error {
		sess := scheme.Session(c.Request)
		sess.Put("draft", "hello")
		sessionID = sess.ID()
		QueueAfterSessionSave(c.Request, func(http.ResponseWriter) { queuedRan = true })
		go func() {
			defer close(firstSaveDone)
			firstSave = sess.Save(httptest.NewRecorder())
		}()
		<-records.entered
		go func() {
			select {
			case <-commitSaving:
			case <-handlerWrote:
			}
			close(records.release)
		}()
		err := c.String(http.StatusOK, "ok")
		close(handlerWrote)
		<-firstSaveDone
		return err
	})

	w := httptest.NewRecorder()
	hostile.Within(t, hostile.Deadline, func() { r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil)) })

	if !errors.Is(firstSave, errFirstWriteOffline) {
		t.Fatalf("premise: the save in flight = %v, want the store's failure", firstSave)
	}
	cookie, ok := hasSessionCookie(w.Result(), cfg.Name)
	if !ok || cookie.Value != sessionID {
		t.Fatalf("the response carries no cookie for the session (%v): the commit read the session as unchanged while a save that then failed was in flight, and saved nothing", w.Result().Cookies())
	}
	rec, err := inner.Get(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("the session has no record after the response: %v", err)
	}
	data, _ := rec.Data["data"].(map[string]any)
	if data["draft"] != "hello" {
		t.Fatalf("the record holds draft=%v, want hello", data["draft"])
	}
	if !queuedRan {
		t.Fatal("the write queued behind the save was not delivered after the commit's save")
	}
}

// renewOnActivity reads the session's modified mark to decide one thing:
// whether the server record's debounced refresh runs ahead of the save. A
// save in flight on another goroutine has the mark cleared, so the commit
// can read "unchanged" there for a session it then saves (the save in
// flight fails and puts the mark back). That misread changes nothing that
// is saved: the record and the cookies of the response are the same as on
// a request with no save in flight.
func TestSessionMiddleware_AMisreadMarkAtTheActivityRefreshChangesNothingSaved(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	defer sessionclock.Set(func() time.Time { return now })()

	type outcome struct {
		cookies []string
		data    map[string]any
		userID  string
		seen    time.Time
		expires time.Time
	}
	run := func(t *testing.T, saveInFlight bool) outcome {
		cfg := teardownConfig()
		inner := session.NewMemoryStore()
		t.Cleanup(func() { _ = inner.Close(context.Background()) })
		records := &firstWriteHeldRecords{ServerSessionStore: inner, entered: make(chan struct{}), release: make(chan struct{})}
		records.unarmed.Store(true)
		store, err := session.NewServerStore(cfg, records)
		if err != nil {
			t.Fatalf("NewServerStore: %v", err)
		}
		users := &revokeTestStore{users: map[string]*revokeTestUser{"u1": {id: "u1"}}}
		scheme, err := NewSessionScheme(users, cfg, teardownEncryptor(t), WithSessionStore(store))
		if err != nil {
			t.Fatalf("NewSessionScheme: %v", err)
		}

		commitSaving := make(chan struct{})
		var commitOnce sync.Once
		orig := saveSessionFromMiddleware
		saveSessionFromMiddleware = func(g *SessionScheme, w http.ResponseWriter, s contract.Session) error {
			if !records.unarmed.Load() {
				commitOnce.Do(func() { close(commitSaving) })
			}
			return orig(g, w, s)
		}
		defer func() { saveSessionFromMiddleware = orig }()

		var firstSave error
		r := router.New()
		r.Use(scheme.SessionMiddleware())
		r.Post("/login", func(c *router.Context) error {
			if err := scheme.Login(c.Response, c.Request, users.users["u1"]); err != nil {
				return err
			}
			return c.String(http.StatusOK, "in")
		})
		r.Get("/draft", func(c *router.Context) error {
			sess := scheme.Session(c.Request)
			sess.Put("draft", "hello")
			if !saveInFlight {
				return c.String(http.StatusOK, "ok")
			}
			records.unarmed.Store(false)
			handlerWrote, firstSaveDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(firstSaveDone)
				firstSave = sess.Save(httptest.NewRecorder())
			}()
			<-records.entered
			go func() {
				select {
				case <-commitSaving:
				case <-handlerWrote:
				}
				close(records.release)
			}()
			err := c.String(http.StatusOK, "ok")
			close(handlerWrote)
			<-firstSaveDone
			return err
		})

		login := httptest.NewRecorder()
		r.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/login", nil))
		cookie, ok := hasSessionCookie(login.Result(), cfg.Name)
		if !ok {
			t.Fatalf("premise: no session cookie after sign-in: %v", login.Result().Cookies())
		}
		id := cookie.Value

		req := httptest.NewRequest(http.MethodGet, "/draft", nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		hostile.Within(t, hostile.Deadline, func() { r.ServeHTTP(w, req) })
		if saveInFlight && !errors.Is(firstSave, errFirstWriteOffline) {
			t.Fatalf("premise: the save in flight = %v, want the store's failure", firstSave)
		}

		rec, err := inner.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("the session's record after the response: %v", err)
		}
		out := outcome{userID: rec.UserID, seen: rec.LastSeenAt, expires: rec.ExpiresAt}
		out.data, _ = rec.Data["data"].(map[string]any)
		for _, line := range w.Header().Values("Set-Cookie") {
			out.cookies = append(out.cookies, strings.ReplaceAll(line, id, "<id>"))
		}
		return out
	}

	plain := run(t, false)
	misread := run(t, true)
	if plain.data["draft"] != "hello" {
		t.Fatalf("premise: the request with no save in flight saved draft=%v", plain.data["draft"])
	}
	if len(plain.cookies) == 0 {
		t.Fatal("premise: the request with no save in flight wrote no cookie")
	}
	if strings.Join(misread.cookies, "\n") != strings.Join(plain.cookies, "\n") {
		t.Errorf("cookies with a save in flight:\n%v\nwithout:\n%v", misread.cookies, plain.cookies)
	}
	if misread.data["draft"] != plain.data["draft"] || len(misread.data) != len(plain.data) {
		t.Errorf("record data with a save in flight = %v, without = %v", misread.data, plain.data)
	}
	if misread.userID != plain.userID || !misread.seen.Equal(plain.seen) || !misread.expires.Equal(plain.expires) {
		t.Errorf("record with a save in flight: user %q seen %v expires %v; without: user %q seen %v expires %v",
			misread.userID, misread.seen, misread.expires, plain.userID, plain.seen, plain.expires)
	}
}
