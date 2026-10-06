package contract

// BindFields returns a Logger that writes each line through l with kvs
// placed before the line's own key-value pairs. It is the With of a Logger
// that has no field binding of its own; such a type implements With as
//
//	func (x T) With(kvs ...any) contract.Logger { return contract.BindFields(x, kvs...) }
//
// With on the returned Logger binds further pairs after kvs and still
// writes through l. A trailing key without a value is dropped, so every
// line's own pairs keep their pairing. BindFields(nil) returns nil.
func BindFields(l Logger, kvs ...any) Logger {
	if l == nil { //error-inspection-ok: contract imports no internal/nilval (leaf rule); With on a nil receiver reaches here with a typed nil
		return nil
	}
	return &boundLogger{next: l, fields: appendPairs(nil, kvs)}
}

// boundLogger writes through next with fields before each line's pairs. It
// is used by pointer so a bound logger compares by identity, like the
// driver loggers, instead of panicking on its slice field.
type boundLogger struct {
	next   Logger
	fields []any
}

func (b *boundLogger) Debug(msg string, kvs ...any) { b.next.Debug(msg, b.line(kvs)...) }
func (b *boundLogger) Info(msg string, kvs ...any)  { b.next.Info(msg, b.line(kvs)...) }
func (b *boundLogger) Warn(msg string, kvs ...any)  { b.next.Warn(msg, b.line(kvs)...) }
func (b *boundLogger) Error(msg string, kvs ...any) { b.next.Error(msg, b.line(kvs)...) }
func (b *boundLogger) Fatal(msg string, kvs ...any) { b.next.Fatal(msg, b.line(kvs)...) }

// With binds kvs after the pairs b already carries.
func (b *boundLogger) With(kvs ...any) Logger {
	fields := make([]any, 0, len(b.fields)+len(kvs))
	fields = append(fields, b.fields...)
	return &boundLogger{next: b.next, fields: appendPairs(fields, kvs)}
}

// line returns the bound pairs followed by kvs in a new slice, so the
// logger written through may keep or change it without touching the
// bound pairs.
func (b *boundLogger) line(kvs []any) []any {
	out := make([]any, 0, len(b.fields)+len(kvs))
	out = append(out, b.fields...)
	return append(out, kvs...)
}

// appendPairs appends kvs to dst, leaving out a trailing key that has no
// value.
func appendPairs(dst, kvs []any) []any {
	if len(kvs)%2 == 1 {
		kvs = kvs[:len(kvs)-1]
	}
	return append(dst, kvs...)
}
