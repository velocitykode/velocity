package lockheld

import "sync"

// Lock effects: helpers that take a lock and return the func releasing
// it, and deferred statements run in execution order.

type E struct {
	mu     sync.Mutex
	rw     sync.RWMutex
	rows   bool
	logger Logger
	hook   func()
}

// lockPath takes e.mu on one branch and returns its unlock: a caller holds
// e.mu until it calls the result.
func (e *E) lockPath() (unlock func()) {
	if e.rows {
		return func() {}
	}
	e.mu.Lock()
	return e.mu.Unlock
}

// lockRead returns a literal that releases the read lock.
func (e *E) lockRead() func() {
	e.rw.RLock()
	return func() { e.rw.RUnlock() }
}

// lockGiven takes the lock it is given.
func lockGiven(mu *sync.Mutex) func() {
	mu.Lock()
	return mu.Unlock
}

// lockTwice is a helper built on a helper.
func (e *E) lockTwice() func() {
	release := e.lockPath()
	return release
}

// build returns a func but takes no lock.
func (e *E) build() func() { return func() {} }

func (e *E) HelperDeferred() {
	unlock := e.lockPath()
	defer unlock()
	e.hook() // want func
}

func (e *E) HelperReleased() {
	unlock := e.lockPath()
	e.hook() // want func
	unlock()
	e.hook()
}

func (e *E) HelperUnlockedDirectly() {
	_ = e.lockPath()
	e.hook() // want func
	e.mu.Unlock()
	e.hook()
}

func (e *E) HelperReadLiteral() {
	done := e.lockRead()
	e.logger.Warn("x") // want logger
	done()
	e.logger.Warn("after")
}

func (e *E) HelperParam(mu *sync.Mutex) {
	unlock := lockGiven(mu)
	e.hook() // want func
	unlock()
	e.hook()
}

func (e *E) HelperOfHelper() {
	unlock := e.lockTwice()
	defer unlock()
	e.hook() // want func
}

func (e *E) NoLockReturned() {
	f := e.build()
	f()
	e.hook()
}

// Deferred statements run last registered first, each with what the ones
// registered after it left held.

func (e *E) DeferredLogThenUnlock() {
	e.mu.Lock()
	defer func() {
		e.logger.Warn("x") // want logger
		e.mu.Unlock()
	}()
	e.name()
}

func (e *E) DeferredUnlockThenLog() {
	e.mu.Lock()
	defer func() {
		e.mu.Unlock()
		e.logger.Warn("x")
	}()
	e.name()
}

func (e *E) DeferredHookAfterUnlockRuns() {
	e.mu.Lock()
	defer e.hook()
	defer e.mu.Unlock()
}

func (e *E) DeferredHookBeforeUnlockRuns() {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer e.hook() // want func
}

func (e *E) DeferredLiteralUnlocksForEarlierDefer() {
	e.mu.Lock()
	defer e.hook()
	defer func() { e.mu.Unlock() }()
}

func (e *E) DeferredOnPanicPath(bad bool) {
	e.mu.Lock()
	defer func() {
		if recover() != nil {
			e.logger.Error("recovered") // want logger
		}
	}()
	if bad {
		panic("bad")
	}
	e.mu.Unlock()
}

func (e *E) DeferredReleaseOfHelperFirst() {
	unlock := e.lockPath()
	defer e.hook()
	defer unlock()
}

func (e *E) DeferredReleaseOfHelperLast() {
	unlock := e.lockPath()
	defer unlock()
	defer e.hook() // want func
}

func (e *E) InPlaceLiteralOwnDefers() {
	e.mu.Lock()
	func() {
		defer e.mu.Unlock()
		e.hook() // want func
	}()
	e.hook()
}

func (e *E) name() string { return "e" }
