package fallbacklog

import "github.com/velocitykode/velocity/contract"

// Contain returns a logger that writes every line through l with Write: a
// line whose write through l panics goes to the fallback Logger instead,
// and the panic never reaches the caller. It is for a component that
// writes many diagnostic lines on paths that must carry on (a worker loop,
// cleanup after a failure), where wrapping each line in Write by hand would
// be noise. A nil l writes through the fallback Logger.
//
// With binds pairs on the contained logger, not on l (as Forwarder does),
// so l's With is never called: every bound pair reaches l as the line's
// first pairs, and reaches the fallback when l panics.
func Contain(l contract.Logger) contract.Logger {
	if c, ok := l.(contained); ok {
		return c
	}
	return contained{l: l}
}

type contained struct{ l contract.Logger }

var _ contract.Logger = contained{}

func (c contained) Debug(msg string, kvs ...any) {
	Write(c.l, func(l contract.Logger) { l.Debug(msg, kvs...) })
}

func (c contained) Info(msg string, kvs ...any) {
	Write(c.l, func(l contract.Logger) { l.Info(msg, kvs...) })
}

func (c contained) Warn(msg string, kvs ...any) {
	Write(c.l, func(l contract.Logger) { l.Warn(msg, kvs...) })
}

func (c contained) Error(msg string, kvs ...any) {
	Write(c.l, func(l contract.Logger) { l.Error(msg, kvs...) })
}

func (c contained) Fatal(msg string, kvs ...any) {
	Write(c.l, func(l contract.Logger) { l.Fatal(msg, kvs...) })
}

// With binds kvs before each line's own pairs.
func (c contained) With(kvs ...any) contract.Logger { return contract.BindFields(c, kvs...) }
