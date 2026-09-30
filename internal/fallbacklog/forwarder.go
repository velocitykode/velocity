package fallbacklog

import (
	"sync/atomic"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// Forwarder is a logger that writes through a target set later. A value
// that hands the logger it writes through to others (connections, children)
// hands them a *Forwarder once; replacing the logger is then one atomic
// Set, with no holder called again and nothing held under a lock.
//
// Every line resolves the target when it is written: the logger set last,
// or the fallback Logger when none is (or nil was). The zero value
// forwards to the fallback. Safe for concurrent use.
type Forwarder struct {
	target atomic.Pointer[forwardTarget]
}

// forwardTarget boxes the target so a nil logger can be stored.
type forwardTarget struct{ logger contract.Logger }

var _ contract.Logger = (*Forwarder)(nil)

// Set makes l the target; nil forwards to the fallback.
func (f *Forwarder) Set(l contract.Logger) {
	f.target.Store(&forwardTarget{logger: l})
}

// Installed returns the logger set last, or nil when none is set.
func (f *Forwarder) Installed() contract.Logger {
	if t := f.target.Load(); t != nil {
		return t.logger
	}
	return nil
}

func (f *Forwarder) Debug(msg string, kvs ...any) { Resolve(f.Installed()).Debug(msg, kvs...) }
func (f *Forwarder) Info(msg string, kvs ...any)  { Resolve(f.Installed()).Info(msg, kvs...) }
func (f *Forwarder) Warn(msg string, kvs ...any)  { Resolve(f.Installed()).Warn(msg, kvs...) }
func (f *Forwarder) Error(msg string, kvs ...any) { Resolve(f.Installed()).Error(msg, kvs...) }
func (f *Forwarder) Fatal(msg string, kvs ...any) { Resolve(f.Installed()).Fatal(msg, kvs...) }

// With binds kvs on the forwarder itself, not on the current target, so a
// bound logger follows a later Set too. The pairs come before each line's
// own, as contract.Logger's With requires; the target sees them as the
// line's first pairs.
func (f *Forwarder) With(kvs ...any) contract.Logger {
	return contract.BindFields(f, kvs...)
}

// Hand hands f to la: la.SetLogger(f). la's SetLogger is user code, so a
// panic in it is contained: warning is written through f with the panic
// under "error" (through Write, so a panicking logger cannot escape
// either), la keeps the logger it had, and the caller goes on to hand the
// rest. A nil la is ignored.
func (f *Forwarder) Hand(la contract.LoggerAware, warning string) {
	if la == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			err := panicerr.FromRecovered(p)
			Write(f, func(l contract.Logger) { l.Warn(warning, "error", err) })
		}
	}()
	la.SetLogger(f)
}
