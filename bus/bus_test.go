package bus

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/pipeline"
)

// --- test types ---

type createUser struct {
	Name  string
	Email string
}

type deleteUser struct {
	ID int
}

type selfHandlingCmd struct {
	called bool
}

func (s *selfHandlingCmd) Handle() error {
	s.called = true
	return nil
}

type selfHandlingErrCmd struct{}

func (s *selfHandlingErrCmd) Handle() error {
	return errors.New("self-handling error")
}

type mockQueuePusher struct {
	mu   sync.Mutex
	jobs []interface {
		Handle() error
		Failed(error)
	}
	err error // optional error to return
}

func (m *mockQueuePusher) Push(job interface {
	Handle() error
	Failed(error)
}, queue ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.jobs = append(m.jobs, job)
	return nil
}

type mockLogger struct {
	mu       sync.Mutex
	messages []string
}

func (l *mockLogger) Info(msg string, kvs ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, msg)
}

// --- tests ---

func TestBus_Dispatch_RegisteredHandler(t *testing.T) {
	b := New()

	var got createUser
	Register(b, func(cmd createUser) error {
		got = cmd
		return nil
	})

	err := b.Dispatch(createUser{Name: "Alice", Email: "alice@example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "Alice" || got.Email != "alice@example.com" {
		t.Fatalf("handler received wrong command: %+v", got)
	}
}

func TestBus_Dispatch_SelfHandling(t *testing.T) {
	b := New()

	cmd := &selfHandlingCmd{}
	err := b.Dispatch(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cmd.called {
		t.Fatal("SelfHandling.Handle() was not called")
	}
}

func TestBus_Dispatch_SelfHandling_Error(t *testing.T) {
	b := New()

	err := b.Dispatch(&selfHandlingErrCmd{})
	if err == nil {
		t.Fatal("expected error from self-handling command")
	}
	if err.Error() != "self-handling error" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestBus_Dispatch_NoHandler(t *testing.T) {
	b := New()

	err := b.Dispatch(deleteUser{ID: 1})
	if err == nil {
		t.Fatal("expected error for unregistered command")
	}
	if expected := "bus: no handler registered for bus.deleteUser"; err.Error() != expected {
		t.Fatalf("got error %q, want %q", err.Error(), expected)
	}
}

func TestBus_Dispatch_HandlerError(t *testing.T) {
	b := New()

	Register(b, func(cmd createUser) error {
		return errors.New("handler failed")
	})

	err := b.Dispatch(createUser{Name: "Bob"})
	if err == nil || err.Error() != "handler failed" {
		t.Fatalf("expected 'handler failed', got: %v", err)
	}
}

func TestBus_Dispatch_WithMiddleware(t *testing.T) {
	b := New()

	var order []string

	b.Through(Middleware(func(cmd Command, next func(Command) error) error {
		order = append(order, "before")
		err := next(cmd)
		order = append(order, "after")
		return err
	}))

	Register(b, func(cmd createUser) error {
		order = append(order, "handler")
		return nil
	})

	err := b.Dispatch(createUser{Name: "Test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"before", "handler", "after"}
	if len(order) != len(expected) {
		t.Fatalf("got order %v, want %v", order, expected)
	}
	for i, v := range expected {
		if order[i] != v {
			t.Fatalf("order[%d] = %q, want %q", i, order[i], v)
		}
	}
}

func TestBus_Dispatch_MultipleMiddleware(t *testing.T) {
	b := New()

	var order []string

	b.Through(
		Middleware(func(cmd Command, next func(Command) error) error {
			order = append(order, "m1-before")
			err := next(cmd)
			order = append(order, "m1-after")
			return err
		}),
		Middleware(func(cmd Command, next func(Command) error) error {
			order = append(order, "m2-before")
			err := next(cmd)
			order = append(order, "m2-after")
			return err
		}),
	)

	Register(b, func(cmd createUser) error {
		order = append(order, "handler")
		return nil
	})

	err := b.Dispatch(createUser{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"m1-before", "m2-before", "handler", "m2-after", "m1-after"}
	if len(order) != len(expected) {
		t.Fatalf("got order %v, want %v", order, expected)
	}
	for i, v := range expected {
		if order[i] != v {
			t.Fatalf("order[%d] = %q, want %q", i, order[i], v)
		}
	}
}

func TestBus_Dispatch_MiddlewareShortCircuit(t *testing.T) {
	b := New()

	b.Through(Middleware(func(cmd Command, next func(Command) error) error {
		return errors.New("blocked by middleware")
	}))

	handlerCalled := false
	Register(b, func(cmd createUser) error {
		handlerCalled = true
		return nil
	})

	err := b.Dispatch(createUser{})
	if err == nil || err.Error() != "blocked by middleware" {
		t.Fatalf("expected 'blocked by middleware', got: %v", err)
	}
	if handlerCalled {
		t.Fatal("handler should not have been called when middleware short-circuits")
	}
}

func TestBus_DispatchAsync(t *testing.T) {
	b := New()
	q := &mockQueuePusher{}
	b.SetQueue(q)

	Register(b, func(cmd createUser) error {
		return nil
	})

	err := b.DispatchAsync(createUser{Name: "Async"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) != 1 {
		t.Fatalf("expected 1 job pushed, got %d", len(q.jobs))
	}

	// Execute the job to verify it dispatches the command
	if err := q.jobs[0].Handle(); err != nil {
		t.Fatalf("job.Handle() error: %v", err)
	}
}

func TestBus_DispatchAsync_NoQueue(t *testing.T) {
	b := New()

	err := b.DispatchAsync(createUser{})
	if err == nil {
		t.Fatal("expected error when queue not configured")
	}
	if expected := "bus: queue not configured for async dispatch"; err.Error() != expected {
		t.Fatalf("got error %q, want %q", err.Error(), expected)
	}
}

func TestBus_DispatchAsync_QueueError(t *testing.T) {
	b := New()
	q := &mockQueuePusher{err: errors.New("queue full")}
	b.SetQueue(q)

	err := b.DispatchAsync(createUser{})
	if err == nil {
		t.Fatal("expected error when queue push fails")
	}
	if !errors.Is(err, q.err) {
		t.Fatalf("expected wrapped queue error, got: %v", err)
	}
}

func TestBus_DispatchAsync_WithQueueName(t *testing.T) {
	b := New()
	q := &mockQueuePusher{}
	b.SetQueue(q)
	b.SetQueueName("commands")

	err := b.DispatchAsync(createUser{Name: "Named"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) != 1 {
		t.Fatalf("expected 1 job pushed, got %d", len(q.jobs))
	}
}

func TestBus_Events_DispatchingAndCompleted(t *testing.T) {
	b := New()

	var events []string
	b.SetEventDispatcher(func(event any) error {
		switch e := event.(type) {
		case *CommandDispatching:
			events = append(events, "dispatching:"+e.CommandType)
		case *CommandCompleted:
			events = append(events, "completed:"+e.CommandType)
		case *CommandFailed:
			events = append(events, "failed:"+e.CommandType)
		}
		return nil
	})

	Register(b, func(cmd createUser) error {
		return nil
	})

	err := b.Dispatch(createUser{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %v", len(events), events)
	}
	if events[0] != "dispatching:bus.createUser" {
		t.Fatalf("events[0] = %q, want dispatching:bus.createUser", events[0])
	}
	if events[1] != "completed:bus.createUser" {
		t.Fatalf("events[1] = %q, want completed:bus.createUser", events[1])
	}
}

func TestBus_Events_Failed(t *testing.T) {
	b := New()

	var events []string
	b.SetEventDispatcher(func(event any) error {
		switch e := event.(type) {
		case *CommandDispatching:
			events = append(events, "dispatching")
		case *CommandFailed:
			events = append(events, "failed:"+e.Error)
		case *CommandCompleted:
			events = append(events, "completed")
		}
		return nil
	})

	Register(b, func(cmd createUser) error {
		return errors.New("oops")
	})

	_ = b.Dispatch(createUser{})

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %v", len(events), events)
	}
	if events[1] != "failed:oops" {
		t.Fatalf("events[1] = %q, want failed:oops", events[1])
	}
}

func TestBus_Events_Queued(t *testing.T) {
	b := New()
	q := &mockQueuePusher{}
	b.SetQueue(q)

	var events []string
	b.SetEventDispatcher(func(event any) error {
		switch event.(type) {
		case *CommandQueued:
			events = append(events, "queued")
		}
		return nil
	})

	_ = b.DispatchAsync(createUser{})

	if len(events) != 1 || events[0] != "queued" {
		t.Fatalf("expected [queued], got %v", events)
	}
}

func TestBus_Events_NilDispatcher(t *testing.T) {
	b := New()

	Register(b, func(cmd createUser) error {
		return nil
	})

	// Should not panic when event dispatcher is nil
	err := b.Dispatch(createUser{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBus_Through_Chainable(t *testing.T) {
	b := New()

	result := b.Through(
		Middleware(func(cmd Command, next func(Command) error) error { return next(cmd) }),
	)

	if result != b {
		t.Fatal("Through should return the same *Bus for chaining")
	}
}

func TestBus_RegisterOverwrite(t *testing.T) {
	b := New()

	var called string
	Register(b, func(cmd createUser) error {
		called = "first"
		return nil
	})
	Register(b, func(cmd createUser) error {
		called = "second"
		return nil
	})

	_ = b.Dispatch(createUser{})
	if called != "second" {
		t.Fatalf("expected second handler to be called, got %q", called)
	}
}

func TestBus_Concurrent(t *testing.T) {
	b := New()

	var count atomic.Int64
	Register(b, func(cmd createUser) error {
		count.Add(1)
		return nil
	})

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			err := b.Dispatch(createUser{Name: fmt.Sprintf("user-%d", n)})
			if err != nil {
				t.Errorf("dispatch %d failed: %v", n, err)
			}
		}(i)
	}

	wg.Wait()

	if got := count.Load(); got != goroutines {
		t.Fatalf("expected %d dispatches, got %d", goroutines, got)
	}
}

func TestBus_Concurrent_WithMiddleware(t *testing.T) {
	b := New()

	b.Through(Middleware(func(cmd Command, next func(Command) error) error {
		return next(cmd)
	}))

	var count atomic.Int64
	Register(b, func(cmd createUser) error {
		count.Add(1)
		return nil
	})

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			_ = b.Dispatch(createUser{Name: fmt.Sprintf("user-%d", n)})
		}(i)
	}

	wg.Wait()

	if got := count.Load(); got != goroutines {
		t.Fatalf("expected %d dispatches, got %d", goroutines, got)
	}
}

func TestLoggingMiddleware(t *testing.T) {
	b := New()
	logger := &mockLogger{}

	b.Through(LoggingMiddleware(logger))

	Register(b, func(cmd createUser) error {
		return nil
	})

	_ = b.Dispatch(createUser{Name: "Test"})

	logger.mu.Lock()
	defer logger.mu.Unlock()
	if len(logger.messages) != 2 {
		t.Fatalf("expected 2 log messages, got %d: %v", len(logger.messages), logger.messages)
	}
	if logger.messages[0] != "Dispatching command" {
		t.Fatalf("messages[0] = %q, want 'Dispatching command'", logger.messages[0])
	}
	if logger.messages[1] != "Command completed" {
		t.Fatalf("messages[1] = %q, want 'Command completed'", logger.messages[1])
	}
}

func TestLoggingMiddleware_Error(t *testing.T) {
	b := New()
	logger := &mockLogger{}

	b.Through(LoggingMiddleware(logger))

	Register(b, func(cmd createUser) error {
		return errors.New("fail")
	})

	_ = b.Dispatch(createUser{})

	logger.mu.Lock()
	defer logger.mu.Unlock()
	if len(logger.messages) != 2 {
		t.Fatalf("expected 2 log messages, got %d: %v", len(logger.messages), logger.messages)
	}
	if logger.messages[1] != "Command failed" {
		t.Fatalf("messages[1] = %q, want 'Command failed'", logger.messages[1])
	}
}

func TestMiddleware_Helper(t *testing.T) {
	fn := func(cmd Command, next func(Command) error) error {
		return next(cmd)
	}

	stage := Middleware(fn)

	// Verify it's a valid pipeline.Stage
	var _ pipeline.Stage[Command] = stage
}

func TestFormatType_Nil(t *testing.T) {
	result := formatType(nil)
	if result != "<nil>" {
		t.Fatalf("formatType(nil) = %q, want '<nil>'", result)
	}
}

func TestCommandJob_Failed_FiresEvent(t *testing.T) {
	b := New()

	var events []string
	b.SetEventDispatcher(func(event any) error {
		switch e := event.(type) {
		case *CommandFailed:
			events = append(events, "failed:"+e.Error)
		}
		return nil
	})

	Register(b, func(cmd createUser) error {
		return nil
	})

	job := &commandJob{cmd: createUser{Name: "Test"}, bus: b}
	job.Failed(errors.New("queue timeout"))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(events), events)
	}
	if events[0] != "failed:queue timeout" {
		t.Fatalf("events[0] = %q, want 'failed:queue timeout'", events[0])
	}
}

func TestCommandJob_Failed_NilDispatcher(t *testing.T) {
	b := New()
	job := &commandJob{cmd: createUser{}, bus: b}
	// Should not panic when event dispatcher is nil
	job.Failed(errors.New("test"))
}
