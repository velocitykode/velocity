package lockheld

import "sync"

// Closed parameters: a func parameter of an unexported function or method
// whose every call site in the package's non-test files passes a func
// literal or a declared function. Calling it counts as calling those
// bodies, reported as reach only when one of them reaches user code.

var pkgLogger Logger

type K struct {
	mu     sync.RWMutex
	logger Logger
	hook   func()
}

func (k *K) withRead(fn func() error) error {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return fn() // closed: every caller passes a literal or a declared function
}

func (k *K) Read() error  { return k.withRead(func() error { return nil }) }
func (k *K) Named() error { return k.withRead(noop) }

func noop() error { return nil }

func (k *K) withLoud(fn func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	fn() // the literals passed in are walked held, and reported there
}

func (k *K) Quiet() { k.withLoud(func() {}) }
func (k *K) Loud()  { k.withLoud(func() { k.logger.Warn("x") }) } // want logger

func runLocked(mu *sync.Mutex, fn func()) {
	mu.Lock()
	defer mu.Unlock()
	fn() // want reach
}

func warnPkg() { pkgLogger.Warn("x") }

func RunsDeclared(mu *sync.Mutex) {
	runLocked(mu, noopFn)
	runLocked(mu, warnPkg)
}

func noopFn() {}

func (k *K) withValue(fn func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	fn() // want func
}

func (k *K) Lit()   { k.withValue(func() {}) }
func (k *K) Field() { k.withValue(k.hook) }

func (k *K) withStored(fn func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	fn() // want func
}

// A literal stored in a variable and then passed is a func value.
func (k *K) Stored() {
	f := func() {}
	k.withStored(f)
}

func (k *K) WithExported(fn func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	fn() // want func
}

func (k *K) CallsExported() { k.WithExported(func() {}) }

func (k *K) withEscaping(fn func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	fn() // want func
}

// The method used as a value may be called with anything.
func (k *K) Escapes() func(func()) {
	k.withEscaping(func() {})
	return k.withEscaping
}

func (k *K) withReassigned(fn func()) {
	fn = k.hook
	k.mu.Lock()
	defer k.mu.Unlock()
	fn() // want func
}

func (k *K) Reassigned() { k.withReassigned(func() {}) }

type ifaceLocker interface{ withIface(fn func()) }

var _ ifaceLocker = (*K)(nil)

func (k *K) withIface(fn func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	fn() // want func
}

func (k *K) Iface() { k.withIface(func() {}) }

// withTested is also called from closed_test.go with a func value; test
// files are outside the scope, so that call does not open the parameter.
func (k *K) withTested(fn func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	fn()
}

func (k *K) Tested() { k.withTested(func() {}) }
