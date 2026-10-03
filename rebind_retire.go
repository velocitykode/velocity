package velocity

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/teardown"
)

// retireGrace bounds how long a boundary waits for the instances it
// retires to finish shutting down before boot goes on. A retirement still
// running past it stays tracked: App.Shutdown waits for it. Tests shrink
// it.
var retireGrace = 2 * time.Second

// unwindRetireTimeout bounds how long a failed New or bootstrap waits for
// the instances its unwind retires before it closes the services they may
// still use. Tests shrink it.
var unwindRetireTimeout = 30 * time.Second

// appRetirements is what the App retired at its boundaries.
type appRetirements struct {
	// children tracks every retirement close: App.Shutdown waits for them
	// (awaitRetired), and a stop called from inside one is refused as the
	// App's own work (childOwnsCaller).
	children teardown.Children[any]
	// none is the empty registry children's Retire and Shutdown are
	// handed: the App's registry is its service fields, which rebind
	// compares itself.
	none map[string]any

	// mu guards ids, borrowed, and the calls into children (the lock its
	// methods expect a manager to hold). No code of a caller's runs under it.
	mu sync.Mutex
	// ids holds every instance retired or parked, for the App's lifetime:
	// a field that holds one again is refused (a closed instance).
	ids []any
	// borrowed holds displaced databases. The database queue driver and
	// batch repository keep the boot database's *sql.DB for their life, so
	// a displaced database closes after the queue (closeBorrowedRetired).
	borrowed []any
	// borrowedDone is closed once closeBorrowedRetired's first call
	// finished, and borrowedErr is its result, written before the close.
	// borrowedDone is made by the first call, under mu.
	borrowedDone chan struct{}
	borrowedErr  error
}

// OwnsCaller reports whether the calling goroutine runs a retirement
// close, so a stop it calls that waits for App.Shutdown would wait on
// itself.
func (r *appRetirements) OwnsCaller() bool {
	return r.children.OwnsCaller()
}

// retiredReassigned refuses a field holding an instance an earlier
// boundary retired: it has been shut down.
func (a *App) retiredReassigned(cur ownedSet) error {
	r := &a.retired
	field := -1
	r.mu.Lock()
	for i, v := range cur {
		if v != nil && containsIdentity(r.ids, v) {
			field = i
			break
		}
	}
	r.mu.Unlock()
	if field < 0 {
		return nil
	}
	return errchain.Errorf("velocity: Services.%s: retired instance reassigned: the %T it holds was displaced and shut down at an earlier boundary", ownedFieldNames[field], cur[field])
}

// retireDisplaced retires each instance displaced between prev and cur
// that nothing still holds (see displacedSince): it closes each one
// contained on a goroutine of its own, tracked by the App's retirement
// children, and waits up to retireGrace for all of them. A displaced
// database is parked instead, closed after the queue by App.Shutdown. A
// close that fails, and a displaced instance kept because an Unwrap chain
// could not be followed, are reported once through the error handler the
// app holds now.
func (a *App) retireDisplaced(prev, cur ownedSet, wait time.Duration) {
	displaced, unknown := displacedSince(a, prev, cur)
	h := a.Services.Errors
	for _, err := range unknown {
		reportRetirement(h, err)
	}
	if len(displaced) == 0 {
		return
	}
	r := &a.retired
	var done []chan struct{}
	for _, d := range displaced {
		name := fieldNameOf(prev, d)
		r.mu.Lock()
		r.ids = append(r.ids, d)
		if name == ownedFieldNames[fieldDB] {
			r.borrowed = append(r.borrowed, d)
			r.mu.Unlock()
			continue
		}
		closeFn := r.children.Retire(r.none, d)
		r.mu.Unlock()
		ch := make(chan struct{})
		done = append(done, ch)
		async.Go(func() {
			defer close(ch)
			if err := closeFn(); err != nil {
				reportRetirement(h, errchain.Errorf("velocity: shutting down the %T displaced from Services.%s: %w", d, name, err))
			}
		})
	}
	if wait <= 0 {
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for _, ch := range done {
		select {
		case <-ch:
		case <-timer.C:
			return
		}
	}
}

// unwindRetire retires what the owned fields displaced since the last
// boundary, for a New or bootstrap that failed before reaching the next
// one, and waits for it within unwindRetireTimeout: the unwind closes the
// services a retiring instance may still use only after it.
func (a *App) unwindRetire() {
	if !a.boundSet {
		return
	}
	cur := ownedFields(a)
	if checkComparable(cur) != nil {
		// A non-comparable value cannot be told from the one it displaced.
		cur = a.bound
	}
	a.retireDisplaced(a.bound, cur, unwindRetireTimeout)
	a.bound = cur
}

// fieldNameOf names the field set held v in.
func fieldNameOf(set ownedSet, v any) string {
	for i, x := range set {
		if x == v {
			return ownedFieldNames[i]
		}
	}
	return ""
}

// reportRetirement reports err through h, contained, or drops it when the
// app holds no error handler.
func reportRetirement(h contract.ErrorHandler, err error) {
	if h == nil {
		return
	}
	_ = teardown.Step(func() error {
		h.Report(err, backgroundErrorContext(context.Background(), contract.ErrorSourceGoroutine).WithExtra("subsystem", "rebind"))
		return nil
	})
}

// awaitRetired waits, within ctx, for every retirement the App's
// boundaries began. Its result covers the waiting only: a retirement's
// own close error was reported when it failed.
func (a *App) awaitRetired(ctx context.Context) error {
	r := &a.retired
	r.mu.Lock()
	wait := r.children.Shutdown(&r.none, func(_ string, err error) error { return err })
	r.mu.Unlock()
	return wait(ctx)
}

// closeBorrowedRetired closes, within ctx and once, the databases the
// App's boundaries displaced: App.Shutdown calls it right after the queue
// closed, so the queue driver never runs on a closed *sql.DB. A repeated
// call returns the first call's result.
func (a *App) closeBorrowedRetired(ctx context.Context) error {
	r := &a.retired
	r.mu.Lock()
	done, first := r.borrowedDone, r.borrowedDone == nil
	if first {
		done = make(chan struct{})
		r.borrowedDone = done
	}
	dbs := r.borrowed
	r.mu.Unlock()
	if !first {
		select {
		case <-done:
			return r.borrowedErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var errs []error
	for _, d := range dbs {
		if err := teardown.Close(ctx, d); err != nil {
			errs = append(errs, errchain.Errorf("velocity: shutting down the %T displaced from Services.DB: %w", d, err))
		}
	}
	r.borrowedErr = errors.Join(errs...)
	close(done)
	return r.borrowedErr
}
