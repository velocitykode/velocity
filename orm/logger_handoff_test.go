package orm

import (
	"context"
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
	g.mu.Unlock()
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

// Two SetLogger calls that overlap leave the manager and every connection
// on the same logger: the handoff is one step, so the later call cannot be
// overtaken by the earlier call's stale propagation.
func TestManagerSetLogger_OverlappingCallsAgree(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	g := &gateDriver{}
	m.AddConnection("gated", g)
	a, b := &levelLog{}, &levelLog{}

	runHeld(t, g, func() { m.SetLogger(a) }, func() { m.SetLogger(b) })

	if got, want := g.logger(), m.Logger(); got != want {
		t.Errorf("connection logger = %p, manager logger = %p, want the same (a=%p b=%p)", got, want, a, b)
	}
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

	if got, want := g.logger(), m.Logger(); got != want {
		t.Errorf("connection logger = %p, manager logger = %p, want the same (a=%p b=%p)", got, want, a, b)
	}
	if m.Logger() != contract.Logger(b) {
		t.Errorf("manager logger = %p, want b (%p), the last SetLogger", m.Logger(), b)
	}
}

// Many goroutines swapping the logger and adding connections at once end
// with every connection on the manager's logger, under the race detector.
func TestManagerLoggerHandoff_ConcurrentSettersAndAdds(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	loggers := []contract.Logger{&levelLog{}, &levelLog{}, &levelLog{}, nil}
	var gates []*gateDriver
	var gmu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
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
				m.AddConnection(time.Now().String()+string(rune('a'+i))+string(rune('a'+j)), g)
				_ = m.Logger()
			}
		}(i)
	}
	wg.Wait()
	final := loggers[0]
	m.SetLogger(final)
	for _, g := range gates {
		if g.logger() != final {
			t.Fatalf("connection logger = %p, want the final logger %p", g.logger(), final)
		}
	}
}
