package teardown_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/drivers"
	"github.com/velocitykode/velocity/storage"
)

// The manager contract: every manager that owns children (log channels,
// cache stores, mail and notification channels, storage disks, ORM named
// connections) closes each child contained exactly once per instance,
// whichever way it leaves the registry. One table, one row per manager;
// an exit a manager does not have is a nil func and its cell is skipped.
// A new manager is a new row; a new exit is a new column here.

// probe is one child's close: it counts its calls, runs code, returns
// err. A deadline probe's first close starts its work (the count, code)
// on a goroutine of its own, and every close waits for the work or its
// ctx: at ctx it returns ctx's error while the work goes on.
type probe struct {
	calls    atomic.Int32
	code     *hostile.Code
	err      error
	deadline bool
	once     sync.Once
	done     chan struct{}
}

func (p *probe) close(ctx context.Context) error {
	if !p.deadline {
		p.calls.Add(1)
		p.code.Run()
		return p.err
	}
	p.once.Do(func() {
		p.done = make(chan struct{})
		go func() {
			p.calls.Add(1)
			p.code.Run()
			close(p.done)
		}()
	})
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// The children, one type per manager: the manager's child interface
// (unset: the contract never uses it) and the probe as its closer.
type (
	logChild struct {
		log.NullLogger
		p *probe
	}
	cacheChild struct {
		cache.Store
		p *probe
	}
	mailChild struct {
		mail.Mailer
		p *probe
	}
	notificationChild struct {
		notification.Channel
		p *probe
	}
	storageChild struct {
		storage.Driver
		p *probe
	}
	ormChild struct {
		drivers.Driver
		p *probe
	}
)

func (c *logChild) Shutdown(ctx context.Context) error          { return c.p.close(ctx) }
func (c *cacheChild) Shutdown(ctx context.Context) error        { return c.p.close(ctx) }
func (c *mailChild) Shutdown(ctx context.Context) error         { return c.p.close(ctx) }
func (c *notificationChild) Shutdown(ctx context.Context) error { return c.p.close(ctx) }
func (c *storageChild) Shutdown(ctx context.Context) error      { return c.p.close(ctx) }

// An ORM driver closes through Close() error.
func (c *ormChild) Close() error { return c.p.close(context.Background()) }

// subject is one manager under the contract, driven through its own API.
type subject struct {
	shutdown   func(ctx context.Context) error
	ownsCaller func() bool
	// publish makes p's child the one held under name.
	publish func(t *testing.T, name string, p *probe)
	// Exits beside Shutdown; nil when the manager has none.
	replace func(name string, p *probe)
	remove  func(name string)
	clear   func()
	// buildAcross builds name with gate blocking the build, runs
	// shutdown while it is blocked, and returns the build's error.
	buildAcross func(t *testing.T, name string, p *probe, gate *hostile.Code) error
	// collide builds name with the build blocked, sets set under name
	// meanwhile, and returns the child the build's caller got.
	collide func(t *testing.T, name string, built, set *probe, gate *hostile.Code) any
	// childOf is the child p stands for, as the manager holds it.
	childOf func(p *probe) any
	// sharedShutdown is true when Shutdown runs through
	// teardown.Children (an instance held under two names is closed
	// once; a Shutdown awaits retirements, re-waits a child that returns
	// at its ctx, and refuses one from a child's close).
	sharedShutdown bool
	// terminal is true when a child published once Shutdown began is
	// disposed at once instead of joining the next Shutdown (the ORM).
	terminal bool
}

// children memoises one child per probe, so a probe published under two
// names is one instance.
type children[C any] struct {
	mu   sync.Mutex
	made map[*probe]C
	wrap func(*probe) C
}

func (c *children[C]) of(p *probe) C {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.made[p]; ok {
		return ch
	}
	if c.made == nil {
		c.made = map[*probe]C{}
	}
	ch := c.wrap(p)
	c.made[p] = ch
	return ch
}

// slots is what a registered factory builds for each name: the probe to
// wrap, and the gate the build blocks on first.
type slots struct {
	mu sync.Mutex
	p  map[string]*probe
	g  map[string]*hostile.Code
}

func (s *slots) set(name string, p *probe, gate *hostile.Code) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		s.p, s.g = map[string]*probe{}, map[string]*hostile.Code{}
	}
	s.p[name], s.g[name] = p, gate
}

// build runs name's gate, then returns its probe.
func (s *slots) build(name string) *probe {
	s.mu.Lock()
	p, gate := s.p[name], s.g[name]
	s.mu.Unlock()
	gate.Run()
	return p
}

// names the subjects use: each registers a driver per name.
var contractNames = []string{"a", "b", "late", "x"}

// driverName is a registry name unique to the test and the subject.
func driverName(t *testing.T, kind, name string) string {
	return strings.NewReplacer("/", "-", " ", "-").Replace(fmt.Sprintf("contract-%s-%s-%s", t.Name(), kind, name))
}

func logSubject(t *testing.T) *subject {
	var sl slots
	kids := &children[*logChild]{wrap: func(p *probe) *logChild { return &logChild{p: p} }}
	cfg := log.LoggingConfig{Channels: map[string]log.ChannelConfig{}}
	for _, name := range contractNames {
		drv := driverName(t, "log", name)
		log.Drivers().Override(drv, func(context.Context, log.LogConfig) (log.Logger, error) { return kids.of(sl.build(name)), nil })
		t.Cleanup(func() { log.Drivers().Override(drv, nil) })
		cfg.Channels[name] = log.ChannelConfig{Driver: drv}
	}
	m := log.NewManager(cfg)
	lookup := func(name string) (any, error) { return m.Channel(name) }
	return &subject{
		shutdown:   m.Shutdown,
		ownsCaller: m.OwnsCaller,
		publish: func(t *testing.T, name string, p *probe) {
			sl.set(name, p, nil)
			if _, err := m.Channel(name); err != nil {
				t.Fatalf("Channel(%s): %v", name, err)
			}
		},
		buildAcross: func(t *testing.T, name string, p *probe, gate *hostile.Code) error {
			sl.set(name, p, gate)
			return across(t, lookup, name, gate, m.Shutdown)
		},
		collide: func(t *testing.T, name string, built, set *probe, gate *hostile.Code) any {
			sl.set(name, built, gate)
			return collideWith(t, lookup, name, gate, func() {
				sl.set(name, set, nil)
				if _, err := m.Channel(name); err != nil {
					t.Errorf("Channel(%s) during the build: %v", name, err)
				}
			})
		},
		childOf:        func(p *probe) any { return kids.of(p) },
		sharedShutdown: true,
	}
}

func cacheSubject(t *testing.T) *subject {
	var sl slots
	kids := &children[*cacheChild]{wrap: func(p *probe) *cacheChild { return &cacheChild{p: p} }}
	cfg := &cache.Config{Stores: map[string]cache.StoreConfig{}}
	for _, name := range contractNames {
		drv := driverName(t, "cache", name)
		cache.Drivers().Override(drv, func(context.Context, cache.StoreConfig) (cache.Store, error) { return kids.of(sl.build(name)), nil })
		t.Cleanup(func() { cache.Drivers().Override(drv, nil) })
		cfg.Stores[name] = cache.StoreConfig{Driver: drv}
	}
	m := cache.NewManager(cfg)
	lookup := func(name string) (any, error) { return m.Store(name) }
	return &subject{
		shutdown:   m.Shutdown,
		ownsCaller: m.OwnsCaller,
		publish: func(t *testing.T, name string, p *probe) {
			sl.set(name, p, nil)
			if _, err := m.Store(name); err != nil {
				t.Fatalf("Store(%s): %v", name, err)
			}
		},
		buildAcross: func(t *testing.T, name string, p *probe, gate *hostile.Code) error {
			sl.set(name, p, gate)
			return across(t, lookup, name, gate, m.Shutdown)
		},
		childOf:        func(p *probe) any { return kids.of(p) },
		sharedShutdown: true,
	}
}

func mailSubject(*testing.T) *subject {
	m := mail.NewManager()
	kids := &children[*mailChild]{wrap: func(p *probe) *mailChild { return &mailChild{p: p} }}
	return &subject{
		shutdown:       m.Shutdown,
		ownsCaller:     m.OwnsCaller,
		publish:        func(_ *testing.T, name string, p *probe) { m.SetChannel(name, kids.of(p)) },
		replace:        func(name string, p *probe) { m.SetChannel(name, kids.of(p)) },
		remove:         m.RemoveChannel,
		clear:          m.ClearChannels,
		childOf:        func(p *probe) any { return kids.of(p) },
		sharedShutdown: true,
	}
}

func notificationSubject(t *testing.T) *subject {
	var sl slots
	m := notification.NewManager()
	kids := &children[*notificationChild]{wrap: func(p *probe) *notificationChild { return &notificationChild{p: p} }}
	// A notification channel's name is its driver's.
	names := map[string]string{}
	for _, name := range contractNames {
		drv := driverName(t, "notification", name)
		names[name] = drv
		notification.Drivers().Override(drv, func(context.Context, notification.ChannelConfig) (notification.Channel, error) {
			return kids.of(sl.build(name)), nil
		})
		t.Cleanup(func() { notification.Drivers().Override(drv, nil) })
	}
	lookup := func(name string) (any, error) { return m.Channel(names[name]) }
	set := func(name string, p *probe) { m.SetChannel(names[name], kids.of(p)) }
	return &subject{
		shutdown:   m.Shutdown,
		ownsCaller: m.OwnsCaller,
		publish:    func(_ *testing.T, name string, p *probe) { set(name, p) },
		replace:    set,
		buildAcross: func(t *testing.T, name string, p *probe, gate *hostile.Code) error {
			sl.set(name, p, gate)
			return across(t, lookup, name, gate, m.Shutdown)
		},
		collide: func(t *testing.T, name string, built, setP *probe, gate *hostile.Code) any {
			sl.set(name, built, gate)
			return collideWith(t, lookup, name, gate, func() { set(name, setP) })
		},
		childOf:        func(p *probe) any { return kids.of(p) },
		sharedShutdown: true,
	}
}

func storageSubject(t *testing.T) *subject {
	var sl slots
	m := storage.NewManager(storage.Config{})
	kids := &children[*storageChild]{wrap: func(p *probe) *storageChild { return &storageChild{p: p} }}
	disks := map[string]storage.DiskConfig{}
	for _, name := range contractNames {
		drv := driverName(t, "storage", name)
		storage.Drivers().Override(drv, func(context.Context, storage.DiskConfig) (storage.Driver, error) { return kids.of(sl.build(name)), nil })
		t.Cleanup(func() { storage.Drivers().Override(drv, nil) })
		disks[name] = storage.DiskConfig{Driver: drv}
	}
	configure := func(name string) error {
		return m.Configure(storage.Config{Disks: map[string]storage.DiskConfig{name: disks[name]}})
	}
	return &subject{
		shutdown:   m.Shutdown,
		ownsCaller: m.OwnsCaller,
		publish:    func(_ *testing.T, name string, p *probe) { m.AddDisk(name, kids.of(p)) },
		replace:    func(name string, p *probe) { m.AddDisk(name, kids.of(p)) },
		buildAcross: func(t *testing.T, name string, p *probe, gate *hostile.Code) error {
			sl.set(name, p, gate)
			return across(t, func(name string) (any, error) { return nil, configure(name) }, name, gate, m.Shutdown)
		},
		childOf:        func(p *probe) any { return kids.of(p) },
		sharedShutdown: true,
	}
}

// storageConfigureSubject replaces disks through Configure. Each call
// names its probe in the disk's config (URL), so concurrent Configures
// build what they were given.
func storageConfigureSubject(t *testing.T) *subject {
	var mu sync.Mutex
	probes := map[string]*probe{}
	m := storage.NewManager(storage.Config{})
	kids := &children[*storageChild]{wrap: func(p *probe) *storageChild { return &storageChild{p: p} }}
	drv := driverName(t, "storage-configure", "disk")
	storage.Drivers().Override(drv, func(_ context.Context, cfg storage.DiskConfig) (storage.Driver, error) {
		mu.Lock()
		p := probes[cfg.URL]
		mu.Unlock()
		return kids.of(p), nil
	})
	t.Cleanup(func() { storage.Drivers().Override(drv, nil) })
	configure := func(name string, p *probe) {
		key := fmt.Sprintf("%p", p)
		mu.Lock()
		probes[key] = p
		mu.Unlock()
		err := m.Configure(storage.Config{Disks: map[string]storage.DiskConfig{name: {Driver: drv, URL: key}}})
		if err != nil && !strings.Contains(err.Error(), "shut down while the disks were configured") {
			t.Errorf("Configure(%s): %v", name, err)
		}
	}
	return &subject{
		shutdown:       m.Shutdown,
		ownsCaller:     m.OwnsCaller,
		publish:        func(_ *testing.T, name string, p *probe) { configure(name, p) },
		replace:        configure,
		childOf:        func(p *probe) any { return kids.of(p) },
		sharedShutdown: true,
	}
}

func ormSubject(t *testing.T) *subject {
	drv := driverName(t, "orm", "default")
	orm.Drivers().Override(drv, func(context.Context, drivers.ConnectionConfig) (drivers.Driver, error) {
		return &ormChild{p: &probe{}}, nil
	})
	t.Cleanup(func() { orm.Drivers().Override(drv, nil) })
	m, err := orm.NewManager(orm.ManagerConfig{Driver: drv})
	if err != nil {
		t.Fatalf("orm.NewManager: %v", err)
	}
	kids := &children[*ormChild]{wrap: func(p *probe) *ormChild { return &ormChild{p: p} }}
	return &subject{
		shutdown:       m.Shutdown,
		ownsCaller:     m.OwnsCaller,
		publish:        func(_ *testing.T, name string, p *probe) { m.AddConnection(name, kids.of(p)) },
		replace:        func(name string, p *probe) { m.AddConnection(name, kids.of(p)) },
		childOf:        func(p *probe) any { return kids.of(p) },
		sharedShutdown: true,
		terminal:       true,
	}
}

// across runs a lookup of name whose build blocks on gate, shuts the
// manager down while it is blocked (its registry empty), releases the
// build and returns the lookup's error.
func across(t *testing.T, lookup func(string) (any, error), name string, gate *hostile.Code, shutdown func(context.Context) error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := lookup(name)
		done <- err
	}()
	if !gate.AwaitEntered(t) {
		return nil
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown while the build runs = %v, want nil", err)
	}
	gate.Release()
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = <-done })
	return err
}

// collideWith runs a lookup of name whose build blocks on gate, runs
// meanwhile (publishing another child under name), releases the build and
// returns what the lookup got.
func collideWith(t *testing.T, lookup func(string) (any, error), name string, gate *hostile.Code, meanwhile func()) any {
	t.Helper()
	type result struct {
		v   any
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := lookup(name)
		done <- result{v, err}
	}()
	if !gate.AwaitEntered(t) {
		return nil
	}
	meanwhile()
	gate.Release()
	var r result
	hostile.Within(t, hostile.Deadline, func() { r = <-done })
	if r.err != nil {
		t.Errorf("lookup that lost the publish = %v, want nil", r.err)
	}
	return r.v
}

var subjects = []struct {
	name string
	make func(t *testing.T) *subject
}{
	{"log", logSubject},
	{"cache", cacheSubject},
	{"mail", mailSubject},
	{"notification", notificationSubject},
	{"storage", storageSubject},
	{"storage-configure", storageConfigureSubject},
	{"orm", ormSubject},
}

// eachSubject runs fn for every row, skipping one when skip says so.
func eachSubject(t *testing.T, skip func(*subject) bool, fn func(t *testing.T, s *subject)) {
	for _, row := range subjects {
		t.Run(row.name, func(t *testing.T) {
			s := row.make(t)
			if skip(s) {
				t.Skipf("%s has no such exit", row.name)
			}
			fn(t, s)
		})
	}
}

func calls(t *testing.T, what string, p *probe, want int32) {
	t.Helper()
	if n := p.calls.Load(); n != want {
		t.Errorf("%s closed %d times, want %d", what, n, want)
	}
}

// Shutdown closes every child once; a repeated Shutdown closes nothing
// again.
func TestManagerContract_ShutdownClosesEachChildOnce(t *testing.T) {
	eachSubject(t, func(*subject) bool { return false }, func(t *testing.T, s *subject) {
		a, b := &probe{}, &probe{}
		s.publish(t, "a", a)
		s.publish(t, "b", b)
		hostile.Within(t, hostile.Deadline, func() {
			_ = s.shutdown(context.Background())
			_ = s.shutdown(context.Background())
		})
		calls(t, "a", a, 1)
		calls(t, "b", b, 1)
	})
}

// A replaced child is closed once, by the replacing call, before it
// returns; the new one is closed by Shutdown; re-assigning the same
// instance closes nothing.
func TestManagerContract_ReplaceRetiresTheDisplacedChild(t *testing.T) {
	eachSubject(t, func(s *subject) bool { return s.replace == nil }, func(t *testing.T, s *subject) {
		old, next := &probe{}, &probe{}
		s.publish(t, "a", old)
		s.replace("a", old)
		calls(t, "re-assigned instance", old, 0)
		s.replace("a", next)
		calls(t, "displaced child", old, 1)
		calls(t, "new child before Shutdown", next, 0)
		hostile.Within(t, hostile.Deadline, func() { _ = s.shutdown(context.Background()) })
		calls(t, "displaced child after Shutdown", old, 1)
		calls(t, "new child", next, 1)
	})
}

// A removed child is closed once by the removal.
func TestManagerContract_RemoveRetiresTheChild(t *testing.T) {
	eachSubject(t, func(s *subject) bool { return s.remove == nil }, func(t *testing.T, s *subject) {
		p := &probe{}
		s.publish(t, "a", p)
		s.remove("a")
		calls(t, "removed child", p, 1)
		hostile.Within(t, hostile.Deadline, func() { _ = s.shutdown(context.Background()) })
		calls(t, "removed child after Shutdown", p, 1)
	})
}

// A clear closes every child once, one held under two names included.
func TestManagerContract_ClearRetiresEveryChildOnce(t *testing.T) {
	eachSubject(t, func(s *subject) bool { return s.clear == nil }, func(t *testing.T, s *subject) {
		a, shared := &probe{}, &probe{}
		s.publish(t, "a", a)
		s.publish(t, "b", shared)
		s.publish(t, "x", shared)
		s.clear()
		calls(t, "a", a, 1)
		calls(t, "child under two names", shared, 1)
		hostile.Within(t, hostile.Deadline, func() { _ = s.shutdown(context.Background()) })
		calls(t, "a after Shutdown", a, 1)
	})
}

// One instance under two names: a replace under one name closes nothing
// while the other holds it; Shutdown closes it once.
func TestManagerContract_OneInstanceUnderTwoNames(t *testing.T) {
	eachSubject(t, func(s *subject) bool { return s.replace == nil }, func(t *testing.T, s *subject) {
		shared, other := &probe{}, &probe{}
		s.publish(t, "a", shared)
		s.publish(t, "b", shared)
		s.replace("a", other)
		calls(t, "child still held under b", shared, 0)
		if !s.sharedShutdown {
			return
		}
		s.publish(t, "x", shared)
		hostile.Within(t, hostile.Deadline, func() { _ = s.shutdown(context.Background()) })
		calls(t, "child under two names", shared, 1)
		calls(t, "other", other, 1)
	})
}

// A displaced child's close is contained: its panic does not escape the
// replacing call, its error is written once as a warning, and Shutdown
// does not report it again.
func TestManagerContract_RetirementFailureIsReportedOnce(t *testing.T) {
	eachSubject(t, func(s *subject) bool { return s.replace == nil }, func(t *testing.T, s *subject) {
		out := fallbacklogtest.Capture(t)
		s.publish(t, "a", &probe{code: hostile.New(t, hostile.Panic, nil)})
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("replace panicked: %v", p)
				}
			}()
			s.replace("a", &probe{})
		}()
		if lines := out.Lines(); len(lines) != 1 {
			t.Errorf("fallback lines = %v, want one warning for the displaced child", lines)
		}
		var err error
		hostile.Within(t, hostile.Deadline, func() { err = s.shutdown(context.Background()) })
		if err != nil {
			t.Errorf("Shutdown = %v, want nil (the displaced child's failure was reported by its replace)", err)
		}
		if lines := out.Lines(); len(lines) != 1 {
			t.Errorf("fallback lines after Shutdown = %v, want still one", lines)
		}
	})
}

// A child built across a Shutdown (the registry empty) is not published:
// it is closed once, and the build's caller gets an error holding its
// close error.
func TestManagerContract_BuildAcrossShutdownIsDisposed(t *testing.T) {
	eachSubject(t, func(s *subject) bool { return s.buildAcross == nil }, func(t *testing.T, s *subject) {
		errClose := errors.New("close failed")
		p := &probe{err: errClose}
		err := s.buildAcross(t, "a", p, hostile.New(t, hostile.Block, nil))
		if !errors.Is(err, errClose) {
			t.Errorf("build across a Shutdown = %v, want an error holding the child's close error", err)
		}
		calls(t, "child built across the Shutdown", p, 1)
		hostile.Within(t, hostile.Deadline, func() { _ = s.shutdown(context.Background()) })
		calls(t, "child built across the Shutdown, after another", p, 1)
	})
}

// A child built while another was published under its name is closed
// once; the lookup gets the published one, which Shutdown closes once.
func TestManagerContract_CollisionDiscardsTheBuiltChild(t *testing.T) {
	eachSubject(t, func(s *subject) bool { return s.collide == nil }, func(t *testing.T, s *subject) {
		built, set := &probe{}, &probe{}
		got := s.collide(t, "a", built, set, hostile.New(t, hostile.Block, nil))
		if got != s.childOf(set) {
			t.Errorf("lookup got %v, want the child published meanwhile", got)
		}
		calls(t, "discarded child", built, 1)
		calls(t, "published child", set, 0)
		hostile.Within(t, hostile.Deadline, func() { _ = s.shutdown(context.Background()) })
		calls(t, "discarded child after Shutdown", built, 1)
		calls(t, "published child after Shutdown", set, 1)
	})
}

// A Shutdown waits for a retirement still closing.
func TestManagerContract_ShutdownAwaitsASlowRetirement(t *testing.T) {
	eachSubject(t, func(s *subject) bool { return s.replace == nil || !s.sharedShutdown }, func(t *testing.T, s *subject) {
		slowCode := hostile.New(t, hostile.Block, nil)
		slow := &probe{code: slowCode}
		s.publish(t, "a", slow)
		currentCode := hostile.New(t, hostile.Block, nil)
		current := &probe{code: currentCode}
		replaced := make(chan struct{})
		go func() {
			s.replace("a", current)
			close(replaced)
		}()
		if !slowCode.AwaitEntered(t) {
			return
		}
		result := make(chan error, 1)
		go func() { result <- s.shutdown(context.Background()) }()
		if !currentCode.AwaitEntered(t) {
			return
		}
		currentCode.Release()
		for range 1000 {
			if err := s.shutdown(cancelled()); !errors.Is(err, context.Canceled) {
				t.Fatalf("Shutdown while a retirement closes = %v, want its ctx error", err)
			}
			runtime.Gosched()
		}
		slowCode.Release()
		var err error
		hostile.Within(t, hostile.Deadline, func() {
			<-replaced
			err = <-result
		})
		if err != nil {
			t.Errorf("Shutdown after the retirement = %v, want nil", err)
		}
		calls(t, "slow", slow, 1)
		calls(t, "current", current, 1)
	})
}

// A child whose close returns at the Shutdown's ctx, its work going on,
// keeps the run open: a later Shutdown returns the child's own result.
func TestManagerContract_AChildReturningAtItsDeadlineKeepsTheRunOpen(t *testing.T) {
	eachSubject(t, func(s *subject) bool { return !s.sharedShutdown }, func(t *testing.T, s *subject) {
		errWork := errors.New("work failed")
		p := &probe{code: hostile.New(t, hostile.Block, nil), err: errWork, deadline: true}
		s.publish(t, "a", p)
		if err := s.shutdown(cancelled()); !errors.Is(err, context.Canceled) {
			t.Errorf("Shutdown with a done ctx = %v, want its ctx error", err)
		}
		if !p.code.AwaitEntered(t) {
			return
		}
		p.code.Release()
		var err error
		hostile.Within(t, hostile.Deadline, func() { err = s.shutdown(context.Background()) })
		if !errors.Is(err, errWork) {
			t.Errorf("later Shutdown = %v, want the child's own result", err)
		}
		calls(t, "deadline child", p, 1)
	})
}

// A Shutdown from a child's close is refused and leaves the registry as
// it is: what the child published meanwhile is closed by the next
// Shutdown, or at once by a terminal manager. OwnsCaller answers true inside the close.
func TestManagerContract_NestedShutdownLeavesTheRegistryIntact(t *testing.T) {
	eachSubject(t, func(s *subject) bool { return !s.sharedShutdown }, func(t *testing.T, s *subject) {
		late := &probe{}
		var nested error
		var owns bool
		code := hostile.New(t, hostile.Reenter, func() {
			owns = s.ownsCaller()
			s.publish(t, "late", late)
			nested = s.shutdown(context.Background())
		})
		s.publish(t, "a", &probe{code: code})
		hostile.Within(t, hostile.Deadline, func() { _ = s.shutdown(context.Background()) })
		if !errors.Is(nested, contract.ErrStopFromOwnWork) {
			t.Errorf("Shutdown from a child's close = %v, want ErrStopFromOwnWork", nested)
		}
		if !owns {
			t.Error("OwnsCaller inside a child's close = false, want true")
		}
		if s.ownsCaller() {
			t.Error("OwnsCaller outside = true, want false")
		}
		if s.terminal {
			// Published after its Shutdown began: disposed at once.
			calls(t, "child published from the close of a terminal manager", late, 1)
			return
		}
		calls(t, "child published from the close, before the next Shutdown", late, 0)
		hostile.Within(t, hostile.Deadline, func() { _ = s.shutdown(context.Background()) })
		calls(t, "child published from the close", late, 1)
	})
}

// Replaces racing Shutdowns: every child is closed exactly once (run under
// -race). A replace after a Shutdown joins the next one; the ORM's
// disposes it at once (terminal).
func TestManagerContract_ReplaceConcurrentWithShutdown(t *testing.T) {
	eachSubject(t, func(s *subject) bool { return s.replace == nil }, func(t *testing.T, s *subject) {
		fallbacklogtest.Capture(t) // the ORM warns for each connection added after its Shutdown
		var mu sync.Mutex
		var all []*probe
		next := func() *probe {
			p := &probe{}
			mu.Lock()
			all = append(all, p)
			mu.Unlock()
			return p
		}
		var wg sync.WaitGroup
		for g := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range 50 {
					s.replace(contractNames[(g+i)%len(contractNames)], next())
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				_ = s.shutdown(context.Background())
			}
		}()
		wg.Wait()
		_ = s.shutdown(context.Background())
		for i, p := range all {
			if n := p.calls.Load(); n != 1 {
				t.Errorf("child %d closed %d times, want 1", i, n)
			}
		}
	})
}

// A displaced child's close is user code run with no lock held: it may
// call back into the manager. A replace from it goes through (its own
// displaced child closed once); a Shutdown from it is refused.
func TestManagerContract_RetirementReentersTheManager(t *testing.T) {
	eachSubject(t, func(s *subject) bool { return s.replace == nil }, func(t *testing.T, s *subject) {
		b1, b2 := &probe{}, &probe{}
		s.publish(t, "b", b1)
		var nested error
		code := hostile.New(t, hostile.Reenter, func() {
			s.replace("b", b2)
			nested = s.shutdown(context.Background())
		})
		s.publish(t, "a", &probe{code: code})
		hostile.Within(t, hostile.Deadline, func() { s.replace("a", &probe{}) })
		calls(t, "child displaced from inside a retirement", b1, 1)
		if s.sharedShutdown && !errors.Is(nested, contract.ErrStopFromOwnWork) {
			t.Errorf("Shutdown from a retirement's close = %v, want ErrStopFromOwnWork", nested)
		}
		hostile.Within(t, hostile.Deadline, func() { _ = s.shutdown(context.Background()) })
		calls(t, "child set from inside a retirement", b2, 1)
	})
}

// ormDefault builds an ORM manager whose default connection is the child
// of the returned probe.
func ormDefault(t *testing.T, wrap func(*probe) drivers.Driver) (*orm.Manager, *probe) {
	t.Helper()
	p := &probe{}
	drv := driverName(t, "orm", "aliased")
	orm.Drivers().Override(drv, func(context.Context, drivers.ConnectionConfig) (drivers.Driver, error) {
		return wrap(p), nil
	})
	t.Cleanup(func() { orm.Drivers().Override(drv, nil) })
	m, err := orm.NewManager(orm.ManagerConfig{Driver: drv})
	if err != nil {
		t.Fatalf("orm.NewManager: %v", err)
	}
	return m, p
}

// The ORM default aliased under a name: the default connection lives
// outside the named registry, so replacing its alias closes nothing (the
// manager still serves queries on it as the default), and Shutdown closes
// it once.
func TestManagerContract_ORMDefaultAliasedUnderAName(t *testing.T) {
	m, def := ormDefault(t, func(p *probe) drivers.Driver { return &ormChild{p: p} })
	other := &probe{}
	m.AddConnection("primary", m.DefaultDriver())
	m.AddConnection("primary", &ormChild{p: other})
	calls(t, "default displaced from its alias", def, 0)
	if m.DefaultDriver() == nil {
		t.Fatal("the manager lost its default driver")
	}
	hostile.Within(t, hostile.Deadline, func() { _ = m.Shutdown(context.Background()) })
	calls(t, "default after Shutdown", def, 1)
	calls(t, "replacement", other, 1)
}

// The alias still in place at Shutdown: the default is closed once, by the
// named registry's run.
func TestManagerContract_ORMDefaultStillAliasedAtShutdown(t *testing.T) {
	m, def := ormDefault(t, func(p *probe) drivers.Driver { return &ormChild{p: p} })
	m.AddConnection("primary", m.DefaultDriver())
	hostile.Within(t, hostile.Deadline, func() { _ = m.Shutdown(context.Background()) })
	calls(t, "default held under a name", def, 1)
}

// ormValueChild is a driver of a value type that cannot be compared.
type ormValueChild struct {
	drivers.Driver
	tags map[string]string
	p    *probe
}

func (c ormValueChild) Close() error { return c.p.close(context.Background()) }

// A default driver of a type that cannot be compared, also held under a
// name: the identity checks never panic, Shutdown returns and leaves the
// manager's lock free.
func TestManagerContract_ORMDefaultOfANonComparableType(t *testing.T) {
	m, _ := ormDefault(t, func(p *probe) drivers.Driver { return ormValueChild{tags: map[string]string{}, p: p} })
	m.AddConnection("primary", m.DefaultDriver())
	hostile.Within(t, hostile.Deadline, func() {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		_ = m.DefaultDriver() // takes the manager's lock
	})
}
