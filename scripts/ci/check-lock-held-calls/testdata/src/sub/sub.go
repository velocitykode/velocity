// Package sub checks reach across packages.
package sub

// CallHook calls a func value: user code.
func CallHook(fn func()) { fn() }

// Pure calls no user code.
func Pure() int { return 1 }
