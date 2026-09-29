package orm

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm/drivers"
)

// gateDriver is a connection whose SetLogger can be held open, to force the
// interleavings a scheduler only produces now and then. It embeds the
// Driver interface for the rest of the method set, which the handoff never
// calls.
type gateDriver struct {
	drivers.Driver

	mu      sync.Mutex
	last    contract.Logger
	calls   int
	armed   bool
	entered chan struct{}
	release chan struct{}
}

// arm makes the next SetLogger call signal entered and wait for release
// before it records its logger.
func (g *gateDriver) arm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed = true
	g.entered = make(chan struct{})
	g.release = make(chan struct{})
}

func (g *gateDriver) SetLogger(l contract.Logger) {
	g.mu.Lock()
	hold := g.armed
	g.armed = false
	entered, release := g.entered, g.release
	g.mu.Unlock()
	if hold {
		close(entered)
		<-release
	}
	g.mu.Lock()
	g.last = l
	g.calls++
	g.mu.Unlock()
}

// handoffs returns how many times the manager called SetLogger.
func (g *gateDriver) handoffs() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// Close lets Manager.Shutdown release the connection.
func (g *gateDriver) Close() error { return nil }

func (g *gateDriver) logger() contract.Logger {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

// runHeld runs held, which the armed gate stops inside the driver's
// SetLogger, then runs racer while held is stopped there, gives racer the
// time it needs to finish when nothing holds it back, and lets held go.
// It returns once both have returned.
func runHeld(t *testing.T, g *gateDriver, held, racer func()) {
	t.Helper()
	g.arm()
	heldDone := make(chan struct{})
	go func() { defer close(heldDone); held() }()
	<-g.entered

	racerDone := make(chan struct{})
	go func() { defer close(racerDone); racer() }()
	select {
	case <-racerDone: // nothing serialised the racer behind the held handoff
	case <-time.After(200 * time.Millisecond): // the racer waits for the held handoff
	}
	close(g.release)
	<-heldDone
	<-racerDone
}

// assertOnManagerLogger fails unless g was handed the manager's
// forwarding logger exactly once and a line g writes through it lands on
// want, the logger the manager holds.
func assertOnManagerLogger(t *testing.T, m *Manager, g *gateDriver, want *levelLog) {
	t.Helper()
	if n := g.handoffs(); n != 1 {
		t.Errorf("SetLogger calls on the connection = %d, want exactly 1", n)
	}
	if g.logger() != contract.Logger(&m.logger) {
		t.Fatalf("connection logger = %T %p, want the manager's forwarder", g.logger(), g.logger())
	}
	if m.Logger() != contract.Logger(want) {
		t.Errorf("manager logger = %p, want %p", m.Logger(), want)
	}
	before := want.count("WARN probe")
	g.logger().Warn("probe")
	if got := want.count("WARN probe"); got != before+1 {
		t.Errorf("a line the connection wrote reached the manager's logger %d times, want 1", got-before)
	}
}

// Two SetLogger calls that overlap leave the manager and every connection
// on the same logger: the connection holds the forwarder, handed once, and
// the later call only swaps its target.
func TestManagerSetLogger_OverlappingCallsAgree(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	g := &gateDriver{}
	m.AddConnection("gated", g)
	a, b := &levelLog{}, &levelLog{}

	runHeld(t, g, func() { m.SetLogger(a) }, func() { m.SetLogger(b) })

	assertOnManagerLogger(t, m, g, b)
}

// A connection added while SetLogger runs ends on the manager's logger, not
// on the one AddConnection read before SetLogger replaced it.
func TestManagerAddConnection_OverlappingSetLoggerAgrees(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	a, b := &levelLog{}, &levelLog{}
	m.SetLogger(a)
	g := &gateDriver{}

	runHeld(t, g, func() { m.AddConnection("gated", g) }, func() { m.SetLogger(b) })

	assertOnManagerLogger(t, m, g, b)
}

// A connection added while the manager has no logger keeps its own until
// the first SetLogger, which hands it the forwarder once; later calls do
// not call it again.
func TestManagerAddConnection_WithoutALoggerKeepsTheDriversOwn(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	g := &gateDriver{}
	m.AddConnection("gated", g)
	if n := g.handoffs(); n != 0 {
		t.Fatalf("SetLogger calls before the manager had a logger = %d, want 0", n)
	}
	a, b := &levelLog{}, &levelLog{}
	m.SetLogger(a)
	m.SetLogger(b)
	assertOnManagerLogger(t, m, g, b)

	// SetLogger(nil) on a fresh manager hands the forwarder too, as it
	// always handed nil: the connection then writes to the fallback.
	m2 := newTestManager(t)
	t.Cleanup(func() { _ = m2.Shutdown(context.Background()) })
	g2 := &gateDriver{}
	m2.AddConnection("gated", g2)
	m2.SetLogger(nil)
	if n := g2.handoffs(); n != 1 || g2.logger() != contract.Logger(&m2.logger) {
		t.Errorf("after SetLogger(nil): calls %d, logger %T, want the forwarder once", n, g2.logger())
	}
	// A connection added after SetLogger(nil) keeps its own again.
	g3 := &gateDriver{}
	m2.AddConnection("later", g3)
	if n := g3.handoffs(); n != 0 {
		t.Errorf("connection added after SetLogger(nil): calls %d, want 0", n)
	}
}

// After Shutdown the manager releases the connections it had not handed a
// logger yet: a later SetLogger calls none of them.
func TestManagerShutdown_ReleasesUnhandedConnections(t *testing.T) {
	m := newTestManager(t)
	g := &gateDriver{}
	m.AddConnection("gated", g)
	_ = m.Shutdown(context.Background())
	m.SetLogger(&levelLog{})
	if n := g.handoffs(); n != 0 {
		t.Errorf("SetLogger calls on a connection of a shut-down manager = %d, want 0", n)
	}
}

// Many goroutines swapping the logger and adding connections at once end
// with every connection handed the forwarder exactly once and writing to
// the manager's final logger, under the race detector.
func TestManagerLoggerHandoff_ConcurrentSettersAndAdds(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	loggers := []contract.Logger{&levelLog{}, &levelLog{}, &levelLog{}, nil}
	var gates []*gateDriver
	var gmu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				m.SetLogger(loggers[(i+j)%len(loggers)])
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				g := &gateDriver{}
				gmu.Lock()
				gates = append(gates, g)
				gmu.Unlock()
				m.AddConnection(fmt.Sprintf("c-%d-%d", i, j), g)
				_ = m.Logger()
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				m.log().Debug("while swapping")
			}
		}()
	}
	wg.Wait()
	final := &levelLog{}
	m.SetLogger(final)
	for _, g := range gates {
		assertOnManagerLogger(t, m, g, final)
	}
}
