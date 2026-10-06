package auth

// yes unwraps a blacklist answer from a store or manager that must not
// fail in the calling test: it panics on an error, so a failure is never
// read as a plain "no".
func yes(ok bool, err error) bool {
	if err != nil {
		panic(err)
	}
	return ok
}

// noErr panics on an error from a call that must not fail in the calling
// test.
func noErr(err error) {
	if err != nil {
		panic(err)
	}
}
