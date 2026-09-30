// Package lockheld holds the cases the checker's test runs it against. A
// line the checker must report ends in a want comment naming the kind.
package lockheld

import (
	"fmt"
	"sync"

	"example.com/lockheld/contract"
	"example.com/lockheld/internal/errchain"
	"example.com/lockheld/sub"
)

type Logger = contract.Logger

type C struct {
	mu     sync.RWMutex
	once   sync.Once
	logger Logger
	hook   func()
	hooks  []func()
	value  any
	name   string
}

func (c *C) LoggerUnderLock() {
	c.mu.Lock()
	c.logger.Warn("x") // want logger
	c.mu.Unlock()
	c.logger.Warn("after unlock is fine")
}

func (c *C) DeferredUnlock() {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.name != "" {
		c.logger.With("k", 1).Info("x") // want logger
	}
}

func (c *C) FuncField() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hook()     // want func
	c.hooks[0]() // want func
	local := c.hook
	local() // want func
}

func (c *C) Format() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = fmt.Errorf("wrap: %w", fmt.Errorf("plain")) // error arguments are not flagged
	_ = fmt.Sprintf("%s", c.name)                   // a string is not flagged
	var err error = fmt.Errorf("e")
	_ = err.Error()                   // want format
	return fmt.Sprintf("%v", c.value) // want format
}

// errchain's formatting entries count as the fmt calls they stand for:
// flagged by their operands, not followed into their bodies.
func (c *C) FormatEntries() {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := fmt.Errorf("e")
	_ = errchain.Errorf("wrap: %w", err) // error arguments are not flagged
	_ = errchain.Sprintf("%s", c.name)   // a string is not flagged
	_ = errchain.Errorf("%v", c.value)   // want format
	_ = errchain.Sprintf("%v", c.value)  // want format
	_ = errchain.Sprint(c.value)         // want format
	_ = wrapLater(err)                   // a caller of an entry is not reach through it
	_ = errchain.Text(err)               // want reach
}

func wrapLater(err error) error { return errchain.Errorf("later: %w", err) }

func (c *C) Once() {
	c.once.Do(func() {
		c.logger.Warn("once") // want logger
	})
}

func (c *C) OnceNamed() {
	c.once.Do(c.warn) // want reach
}

func (c *C) warn() { c.logger.Warn("w") }

func (c *C) Reach() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warn()             // want reach
	sub.CallHook(c.hook) // want reach
	sub.Pure()
}

func (c *C) Suppressed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hook() //lock-held-ok: test seam set only by tests
	c.hook() // want func //lock-held-ok: ab
}

func (c *C) NestedUnlock() {
	c.mu.Lock()
	if c.name == "" {
		c.mu.Unlock()
		c.hook()
		return
	}
	c.hook() // want func
	c.mu.Unlock()
}

func (c *C) LiteralNotRunHere() {
	c.mu.Lock()
	c.hook = func() { c.logger.Warn("runs later, unlocked") }
	go func() { c.logger.Warn("own goroutine") }() //safe-goroutine: checker test case, never run
	func() {
		c.logger.Warn("in place") // want logger
	}()
	c.mu.Unlock()
}

func (c *C) DeferredRecoverRunsHeld() {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("recovered") // want logger
		}
	}()
}

func (c *C) OnceFunc() func() {
	return sync.OnceFunc(func() {
		c.hook() // want func
	})
}

// wrapHook only builds a func: the literal runs when the result is called,
// not during wrapHook, so calling wrapHook under a lock is fine.
func (c *C) wrapHook() func() {
	return pass(func() { c.hook() })
}

func pass(fn func()) func() { return fn }

// hookInPlace runs its literals during the call.
func (c *C) hookInPlace() {
	func() { c.hook() }()
}

func (c *C) hookDeferred() {
	defer func() { c.hook() }()
}

func (c *C) hookInOnce() {
	c.once.Do(func() { c.hook() }) // want func
}

func (c *C) PassedAlongLiteral() {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.wrapHook()
	_ = sub.Wrap(func() { c.hook() })
	c.hookInPlace()  // want reach
	c.hookDeferred() // want reach
	c.hookInOnce()   // want reach
}
