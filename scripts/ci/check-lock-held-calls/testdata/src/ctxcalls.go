package lockheld

import (
	"context"
	"database/sql"
	"net/http"
	"sync"
	"time"

	"example.com/lockheld/internal/ownctx"
)

// Context calls: a caller's context's Done, Err, Value and Deadline are
// user code, and so is any code the context is handed to.

type CtxStore struct {
	mu sync.Mutex
	db *sql.DB
	be Backend
}

// Backend is a pluggable operation taking a context: any code.
type Backend interface {
	Get(ctx context.Context, key string) (string, error)
}

func (s *CtxStore) Methods(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = ctx.Err()            // want ctx
	<-ctx.Done()             // want ctx
	_ = ctx.Value("k")       // want ctx
	_, _ = ctx.Deadline()    // want ctx
	_ = context.Background() // builds nothing from the caller's
}

func (s *CtxStore) HandedOn(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.db.PingContext(ctx)                          // want ctx
	_ = s.db.PingContext(context.WithoutCancel(ctx))   // want ctx
	c, cancel := context.WithTimeout(ctx, time.Second) // want ctx
	_ = cancel
	_ = context.WithoutCancel(ctx)                             // building a wrapper calls nothing
	_ = context.WithValue(ctx, "k", 1)                         // nor does WithValue
	_ = s.db.PingContext(c)                                    // want ctx
	_, _ = s.be.Get(ctx, "k")                                  // want ctx
	_, _ = s.be.Get(context.Background(), "k")                 // want ctx
	req, _ := http.NewRequestWithContext(ctx, "GET", "/", nil) // storing the context calls nothing
	_, _ = http.DefaultClient.Do(req)                          // want ctx
}

// Owned: contexts built from Background through the With functions.
func (s *CtxStore) Owned(parent context.Context) error {
	dl, _ := parent.Deadline()
	ctx, cancel := context.WithDeadline(context.Background(), dl)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "x"); err != nil { // want statement
		return err
	}
	_ = s.db.PingContext(context.TODO())
	return tx.Commit()
}

// Carrier: a transaction begun with the caller's context outside the
// lock still calls that context from its methods.
func (s *CtxStore) Carrier(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() { _ = tx.Rollback() }()             // want ctx
	_, _ = tx.ExecContext(context.Background(), "x") // want ctx
	return tx.Commit()                               // want ctx
}

// Reassigned: a variable assigned a caller's context once is not owned.
func (s *CtxStore) Reassigned(parent context.Context, detach bool) {
	ctx := context.Background()
	if !detach {
		ctx = parent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.db.PingContext(ctx) // want ctx
}

// checkCtx calls the context it is given: reach under a lock.
func checkCtx(ctx context.Context) error { return ctx.Err() }

func (s *CtxStore) Reach(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = checkCtx(ctx) // want reach
}

// ownCtx is a module context whose Value reaches the caller's.
type ownCtx struct{ parent context.Context }

func (c ownCtx) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c ownCtx) Done() <-chan struct{}       { return nil }
func (c ownCtx) Err() error                  { return nil }
func (c ownCtx) Value(key any) any           { return c.parent.Value(key) }

func (s *CtxStore) ModuleContext(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.db.PingContext(ownCtx{ctx}) // want reach
}

// Unlocked: nothing is held.
func (s *CtxStore) Unlocked(ctx context.Context) {
	_ = ctx.Err()
	_ = s.db.PingContext(ctx)
	_, _ = s.be.Get(ctx, "k")
}

// OwnedByOwnctx: contexts the module's ownctx builds are owned, and so is
// a timeout over one.
func (s *CtxStore) OwnedByOwnctx(ctx context.Context) {
	owned := ownctx.Bridge(ctx)
	bounded, cancel := context.WithTimeout(ownctx.Detached(ctx), time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.db.PingContext(owned)
	_ = s.db.PingContext(bounded)
	_ = s.db.PingContext(ownctx.Bridge(ctx)) // want reach
}
