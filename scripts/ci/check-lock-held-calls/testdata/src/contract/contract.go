// Package contract stands in for the module's contract package: the
// checker knows the logger interface by its package path and name.
package contract

type Logger interface {
	Debug(msg string, kvs ...any)
	Info(msg string, kvs ...any)
	Warn(msg string, kvs ...any)
	Error(msg string, kvs ...any)
	Fatal(msg string, kvs ...any)
	With(kvs ...any) Logger
}

// BindFields returns a Logger that writes each line through l with kvs.
func BindFields(l Logger, kvs ...any) Logger { return l }
