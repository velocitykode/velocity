package orm

import (
	"sync/atomic"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
)

// loggerForwarder is the one logger a manager hands its connections. Each
// connection receives it once and keeps it; Manager.SetLogger only swaps
// the forwarder's target, so a replaced logger reaches every connection
// without the manager calling any of them again, and never under a lock.
//
// Every line resolves the target when it is written: the installed logger,
// or the framework's standalone fallback logger when none is (or nil
// was). The zero value forwards to the fallback.
type loggerForwarder struct {
	target atomic.Pointer[forwardTarget]
}

// forwardTarget boxes the installed logger so a nil logger can be stored.
type forwardTarget struct{ logger contract.Logger }

var _ contract.Logger = (*loggerForwarder)(nil)

// set installs l as the target; nil forwards to the fallback.
func (f *loggerForwarder) set(l contract.Logger) {
	f.target.Store(&forwardTarget{logger: l})
}

// installed returns the logger set last, or nil.
func (f *loggerForwarder) installed() contract.Logger {
	if t := f.target.Load(); t != nil {
		return t.logger
	}
	return nil
}

// resolve returns the logger a line written now goes to.
func (f *loggerForwarder) resolve() contract.Logger {
	return fallbacklog.Resolve(f.installed())
}

func (f *loggerForwarder) Debug(msg string, kvs ...any) { f.resolve().Debug(msg, kvs...) }
func (f *loggerForwarder) Info(msg string, kvs ...any)  { f.resolve().Info(msg, kvs...) }
func (f *loggerForwarder) Warn(msg string, kvs ...any)  { f.resolve().Warn(msg, kvs...) }
func (f *loggerForwarder) Error(msg string, kvs ...any) { f.resolve().Error(msg, kvs...) }
func (f *loggerForwarder) Fatal(msg string, kvs ...any) { f.resolve().Fatal(msg, kvs...) }

// With binds kvs on the forwarder itself, not on the current target, so
// a bound logger follows a later SetLogger too. The pairs are placed
// before each line's own, which is the contract.Logger With contract; the
// target sees them as the line's first pairs (a redacting target redacts
// them there like any other pair).
func (f *loggerForwarder) With(kvs ...any) contract.Logger {
	return contract.BindFields(f, kvs...)
}
