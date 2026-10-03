package teardown

import (
	"reflect"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/nilval"
)

// Owners asks the components a stop waits for whether the calling
// goroutine runs their own work, so the stop can be refused instead of
// waiting on itself. A component answers by exporting OwnsCaller() bool,
// which is user code for one an app supplies (a service a module
// installed, a custom disk): Owners calls it contained. The zero value is
// ready; an Owners must not be copied after use.
type Owners struct {
	// mu guards reported. No code of a caller's runs under it.
	mu sync.Mutex
	// reported holds the components whose panic was written, each under
	// itself, or under its type when Go cannot compare it.
	reported map[any]struct{}
}

// Owns reports whether v answers that it owns the calling goroutine. A
// nil or typed-nil v, and one that does not export OwnsCaller, does not.
// Neither does one whose OwnsCaller panics: the panic ends here and is
// written once per component, as a warning through the fallback logger.
// The caller holds no lock.
func (o *Owners) Owns(v any) bool {
	owner, ok := v.(interface{ OwnsCaller() bool })
	if !ok || nilval.Is(v) {
		return false
	}
	var owns bool
	err := Step(func() error {
		owns = owner.OwnsCaller()
		return nil
	})
	if err == nil {
		return owns
	}
	if o.first(v) {
		fallbacklog.Write(nil, func(l contract.Logger) {
			l.Warn("velocity: OwnsCaller panicked and is taken as not owning the caller", "type", reflect.TypeOf(v).String(), "error", err)
		})
	}
	return false
}

// first records v as reported and reports whether it was not yet.
func (o *Owners) first(v any) bool {
	key := v
	if !reflect.ValueOf(v).Comparable() {
		key = reflect.TypeOf(v)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, done := o.reported[key]; done {
		return false
	}
	if o.reported == nil {
		o.reported = make(map[any]struct{})
	}
	o.reported[key] = struct{}{}
	return true
}
