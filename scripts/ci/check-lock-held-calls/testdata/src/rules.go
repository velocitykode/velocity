package lockheld

import (
	"fmt"
	"sync"

	"example.com/lockheld/contract"
	"example.com/lockheld/internal/fallbacklog"
)

// One flagged and one clean case per rule. Only types decide what is user
// code: the names below are chosen to mislead a name match.

// lookalike has the logger method names but does not implement
// contract.Logger (With returns the wrong type): not user code.
type lookalike struct{}

func (lookalike) Debug(string, ...any)     {}
func (lookalike) Info(string, ...any)      {}
func (lookalike) Warn(string, ...any)      {}
func (lookalike) Error(string, ...any)     {}
func (lookalike) Fatal(string, ...any)     {}
func (lookalike) With(...any) lookalike    { return lookalike{} }
func (lookalike) String() string           { return "lookalike" }
func (l lookalike) describe() fmt.Stringer { return l }

// sink implements contract.Logger under a name that says nothing.
type sink struct{}

func (*sink) Debug(string, ...any)          {}
func (*sink) Info(string, ...any)           {}
func (*sink) Warn(string, ...any)           {}
func (*sink) Error(string, ...any)          {}
func (*sink) Fatal(string, ...any)          {}
func (s *sink) With(...any) contract.Logger { return s }

type R struct {
	mu       sync.Mutex
	out      sink
	logger   lookalike
	fallback fallbacklog.Logger
	named    fmt.Stringer
	hook     func()
}

func (r *R) ByTypeNotName() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.out.Warn("x")       // want logger
	r.logger.Warn("x")    // a lookalike named logger is not user code
	r.fallback.Warn("x")  // the fallback logger is framework code
	_ = r.named.String()  // want format
	_ = r.logger.String() // a concrete String method is not flagged
}

func (r *R) MethodValue() {
	r.mu.Lock()
	defer r.mu.Unlock()
	warn := r.out.Warn
	warn("x") // want func
	_ = r.out.Warn
}

func (r *R) DirectCall() {
	r.hook()
	r.mu.Lock()
	r.hook() // want func
	r.mu.Unlock()
	r.hook()
}

func (r *R) ReleasedOnEveryBranch(cond bool) {
	r.mu.Lock()
	if cond {
		r.mu.Unlock()
	} else {
		r.mu.Unlock()
	}
	r.hook()
}

func (r *R) ReleasedOnOneBranch(cond bool) {
	r.mu.Lock()
	if cond {
		r.mu.Unlock()
	}
	r.hook() // want func
	if !cond {
		r.mu.Unlock()
	}
}

func (r *R) OtherBranchReturns(cond bool) {
	r.mu.Lock()
	if cond {
		r.hook() // want func
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	r.hook()
}

func (r *R) OtherBranchPanics(cond bool) {
	r.mu.Lock()
	if !cond {
		r.mu.Unlock()
		panic("no")
	}
	r.hook() // want func
	r.mu.Unlock()
}

func (r *R) SwitchReleasesEverywhere(n int) {
	r.mu.Lock()
	switch n {
	case 0:
		r.mu.Unlock()
	default:
		r.mu.Unlock()
	}
	r.hook()
}

func (r *R) SwitchWithoutDefault(n int) {
	r.mu.Lock()
	switch n {
	case 0:
		r.mu.Unlock()
		return
	}
	r.hook() // want func
	r.mu.Unlock()
}

func (r *R) DeferredUnlockHeldToTheEnd() {
	r.hook()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hook != nil {
		r.hook() // want func
	}
}

func (r *R) Markers() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hook() //lock-held-ok: set once before the component starts
	r.hook() // want func //lock-held-ok:
}

func (r *R) StaleMarker() {
	r.hook() //lock-held-ok: nothing is held here any more // want stale
}
