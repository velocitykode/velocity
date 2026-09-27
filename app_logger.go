package velocity

import "github.com/velocitykode/velocity/contract"

// appLogger forwards every line to the App's Services.Log as it stands when
// the line is written, so a logger that replaces Services.Log after New is
// the one written to by the framework values holding an appLogger. A logger
// its With returns forwards the same way, with the bound pairs placed before
// each line's own.
type appLogger struct{ a *App }

var _ contract.Logger = appLogger{}

func (l appLogger) Debug(msg string, kvs ...any) { l.a.Log.Debug(msg, kvs...) }
func (l appLogger) Info(msg string, kvs ...any)  { l.a.Log.Info(msg, kvs...) }
func (l appLogger) Warn(msg string, kvs ...any)  { l.a.Log.Warn(msg, kvs...) }
func (l appLogger) Error(msg string, kvs ...any) { l.a.Log.Error(msg, kvs...) }
func (l appLogger) Fatal(msg string, kvs ...any) { l.a.Log.Fatal(msg, kvs...) }

// With binds kvs before each line's own pairs; every line still goes to
// Services.Log as it stands when the line is written.
func (l appLogger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }
