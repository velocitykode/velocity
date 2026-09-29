package orm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventemit"
)

// failureWarns returns how many warn lines logger holds for the failure
// policy.
func failureWarns(l *fakeLogger) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, m := range l.msgs {
		if strings.HasPrefix(m, "WARN "+eventemit.FailureMessage) {
			n++
		}
	}
	return n
}

// A listener that panics on a statement event is recovered by the pump,
// which hands the panic to the failure policy: it is counted and its
// event's first failure logged through the manager's logger, not
// swallowed. The pump keeps delivering later events.
func TestQueryEventPump_ListenerPanicReachesThePolicy(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())
	logger := &fakeLogger{}
	m.SetLogger(logger)
	shared := &eventemit.Failures{}
	var hookErr atomic.Value
	shared.SetHook(func(err error, _ any) { hookErr.Store(err) })
	m.ShareEventFailures(shared)

	var delivered atomic.Int32
	m.SetEventDispatcher(func(_ context.Context, ev any) error {
		if _, ok := ev.(*QueryExecuted); ok {
			if delivered.Add(1) == 1 {
				panic("listener broke")
			}
		}
		return nil
	})
	for i := 0; i < 2; i++ {
		if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	if err := m.FlushQueryEvents(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if got := shared.Count(); got != 1 {
		t.Errorf("failed event count = %d, want 1 (the panic)", got)
	}
	if err, _ := hookErr.Load().(error); err == nil || !strings.Contains(err.Error(), "listener broke") {
		t.Errorf("hook error = %v, want the recovered panic", hookErr.Load())
	}
	if got := failureWarns(logger); got != 1 {
		t.Errorf("warn lines through the manager logger = %d, want 1", got)
	}
	if got := delivered.Load(); got != 2 {
		t.Errorf("delivered %d statement events, want 2 (the pump survived the panic)", got)
	}
}

// A statement event the pump drops because its queue is full is recorded
// as a failed event with ErrQueryEventQueueFull.
func TestQueryEventPump_DropIsRecordedAsFailure(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())
	m.SetLogger(&fakeLogger{})
	shared := &eventemit.Failures{}
	var drops atomic.Int32
	shared.SetHook(func(err error, ev any) {
		if errors.Is(err, ErrQueryEventQueueFull) {
			if _, ok := ev.(*QueryExecuted); ok {
				drops.Add(1)
			}
		}
	})
	m.ShareEventFailures(shared)

	release := make(chan struct{})
	var first sync.Once
	entered := make(chan struct{})
	m.SetEventDispatcher(func(context.Context, any) error {
		first.Do(func() { close(entered) })
		<-release
		return nil
	})
	if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	<-entered // the pump is blocked in the listener; the queue now fills
	for i := 0; i < queryEventQueueSize+10; i++ {
		if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
			t.Fatalf("exec %d: %v", i, err)
		}
	}
	close(release)
	if err := m.FlushQueryEvents(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := drops.Load(); got < 10 {
		t.Errorf("drops recorded = %d, want at least 10", got)
	}
	if got := shared.Count(); got != uint64(drops.Load()) {
		t.Errorf("failed event count = %d, want %d (every drop, nothing else)", got, drops.Load())
	}
}

// ShareEventFailures(nil) is safe, before and after a dispatcher is
// installed, and returns the manager to its own count.
func TestManagerShareEventFailures_Nil(t *testing.T) {
	m := &Manager{}
	m.ShareEventFailures(nil)
	m.SetEventDispatcher(func(context.Context, any) error { return errors.New("listener failed") })
	shared := &eventemit.Failures{}
	m.ShareEventFailures(shared)
	m.dispatchEvent(context.Background(), &TxRecover{})
	m.ShareEventFailures(nil)
	m.dispatchEvent(context.Background(), &TxRecover{})
	if shared.Count() != 1 || m.events.FailureCount() != 1 {
		t.Errorf("shared = %d, own = %d; want 1 and 1", shared.Count(), m.events.FailureCount())
	}
}

// SetEventDispatcher and ShareEventFailures race with statements executed
// from many goroutines (run under -race): every failed delivery and every
// drop is recorded exactly once, in whichever Failures the manager held.
func TestManagerEventFailures_ConcurrentSetAndShareWhileQuerying(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())
	m.SetLogger(&fakeLogger{})
	a, b := &eventemit.Failures{}, &eventemit.Failures{}
	var drops atomic.Uint64
	countDrops := func(err error, _ any) {
		if errors.Is(err, ErrQueryEventQueueFull) {
			drops.Add(1)
		}
	}
	a.SetHook(countDrops)
	b.SetHook(countDrops)
	var failed atomic.Uint64
	fail := func(context.Context, any) error { failed.Add(1); return errors.New("listener failed") }
	m.SetEventDispatcher(fail)
	m.ShareEventFailures(a)

	stop := make(chan struct{})
	var configurer sync.WaitGroup
	configurer.Add(1)
	go func() {
		defer configurer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			m.SetEventDispatcher(fail)
			if i%2 == 0 {
				m.ShareEventFailures(b)
			} else {
				m.ShareEventFailures(a)
			}
		}
	}()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
					t.Errorf("exec: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	configurer.Wait()
	if err := m.FlushQueryEvents(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got, want := a.Count()+b.Count(), failed.Load()+drops.Load(); got != want {
		t.Errorf("recorded %d failures, want %d (%d failed deliveries + %d drops, each once)", got, want, failed.Load(), drops.Load())
	}
	if failed.Load() == 0 {
		t.Error("no statement event reached the dispatcher")
	}
}

// countingLogger counts debug lines by message and warn lines by message.
type countingLogger struct {
	mu     sync.Mutex
	counts map[string]int
}

func (l *countingLogger) add(level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts == nil {
		l.counts = map[string]int{}
	}
	l.counts[level+" "+msg]++
}

func (l *countingLogger) count(level, prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, v := range l.counts {
		if strings.HasPrefix(k, level+" "+prefix) {
			n += v
		}
	}
	return n
}

func (l *countingLogger) Debug(msg string, _ ...any)      { l.add("debug", msg) }
func (l *countingLogger) Info(msg string, _ ...any)       { l.add("info", msg) }
func (l *countingLogger) Warn(msg string, _ ...any)       { l.add("warn", msg) }
func (l *countingLogger) Error(msg string, _ ...any)      { l.add("error", msg) }
func (l *countingLogger) Fatal(msg string, _ ...any)      { l.add("fatal", msg) }
func (l *countingLogger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

// The statement log (written at the instrumented pool's single statement
// exit) and the pump's drop recording compose: every statement writes its
// one query line whether its event was delivered or dropped, and every
// dropped event is recorded once, with one policy line for the event name.
func TestQueryEventPump_DropsComposeWithTheStatementLog(t *testing.T) {
	m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:", LogQueries: true})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Shutdown(context.Background())
	logger := &countingLogger{}
	m.SetLogger(logger)
	shared := &eventemit.Failures{}
	m.ShareEventFailures(shared)

	release := make(chan struct{})
	var first sync.Once
	entered := make(chan struct{})
	m.SetEventDispatcher(func(context.Context, any) error {
		first.Do(func() { close(entered) })
		<-release
		return nil
	})
	if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	<-entered
	const statements = queryEventQueueSize + 20
	for i := 0; i < statements; i++ {
		if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
			t.Fatalf("exec %d: %v", i, err)
		}
	}
	close(release)
	if err := m.FlushQueryEvents(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if got := logger.count("debug", "velocity/orm: query executed"); got != statements+1 {
		t.Errorf("query lines = %d, want %d (one per statement, dropped or not)", got, statements+1)
	}
	if shared.Count() < 20 {
		t.Errorf("dropped events recorded = %d, want at least 20", shared.Count())
	}
	if got := logger.count("warn", eventemit.FailureMessage); got != 1 {
		t.Errorf("policy warn lines = %d, want 1 (first drop of orm.query.completed)", got)
	}
}
