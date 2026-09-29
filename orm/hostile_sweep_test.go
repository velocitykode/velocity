package orm

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/orm/drivers"
)

// runContained runs call against user code the ORM contains: no panic
// escapes; in Block mode other must return while call is blocked. The code
// is then disarmed and released, and call runs again as the retry.
func runContained(t *testing.T, mode hostile.Mode, code *hostile.Code, call, other func()) {
	t.Helper()
	blocked := make(chan struct{})
	if mode == hostile.Block {
		go func() { defer close(blocked); call() }()
		<-code.Entered()
		hostile.Within(t, hostile.Deadline, other)
	} else {
		close(blocked)
		if p := hostile.Within(t, hostile.Deadline, call); p != nil {
			t.Fatalf("a panic escaped: %v", p)
		}
	}
	code.Disarm()
	code.Release()
	hostile.Within(t, hostile.Deadline, func() { <-blocked })
	if p := hostile.Within(t, hostile.Deadline, call); p != nil {
		t.Fatalf("retry panicked: %v", p)
	}
}

// managerAccessors calls the manager entry points a blocked user code must
// not hold up: none of them may wait on a lock held across user code.
func managerAccessors(t *testing.T, m *Manager) func() {
	return func() {
		m.SetLogger(m.Logger())
		_ = m.DatabaseName()
		_ = m.DriverName()
		_, _ = m.Connection("missing")
		m.AddConnection("sweep-other", &callbackDriver{})
	}
}

// The statement log and the statement observer against a hostile query
// logger or observer: no panic leaves the driver callback, a blocked one
// holds no manager lock, one that calls back into the manager returns, and
// statements log again once it behaves.
func TestStatementLog_HostileSweep(t *testing.T) {
	for _, mode := range hostile.Modes() {
		for _, target := range []string{"logger", "observer"} {
			t.Run(mode.String()+"/"+target, func(t *testing.T) {
				fallbacklogtest.Capture(t)
				m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:", LogQueries: true})
				if err != nil {
					t.Fatalf("NewManager: %v", err)
				}
				t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
				db := m.DB()
				db.SetMaxOpenConns(2)
				code := hostile.New(t, mode, func() { m.SetLogger(m.Logger()); _ = m.DB().Stats() })
				logs := hostile.NewLogger(code)
				if target == "logger" {
					m.SetLogger(logs)
				} else {
					m.SetLogger(hostile.NewLogger(nil))
					m.DefaultDriver().(drivers.StatementObservable).SetStatementObserver(hostileObserver{code})
				}
				call := func() {
					rows, err := db.QueryContext(context.Background(), "SELECT 1")
					if err != nil {
						t.Errorf("query: %v", err)
						return
					}
					for rows.Next() {
					}
					_ = rows.Close()
				}
				runContained(t, mode, code, call, managerAccessors(t, m))
			})
		}
	}
}

// hostileObserver runs its code on every statement.
type hostileObserver struct{ code *hostile.Code }

func (hostileObserver) Observing() bool                           { return true }
func (o hostileObserver) ObserveStatement(drivers.StatementEvent) { o.code.Run() }

// Transaction diagnostics, events and callbacks against hostile user code:
// the manager's logger on a failed rollback, its dispatcher on the
// transaction's events, and a commit callback. The outcome the caller sees
// is fn's, every callback runs, and the manager stays usable.
func TestTransaction_HostileSweep(t *testing.T) {
	for _, mode := range hostile.Modes() {
		for _, target := range []string{"logger", "dispatcher", "callback"} {
			t.Run(mode.String()+"/"+target, func(t *testing.T) {
				fallbacklogtest.Capture(t)
				m := newTestManager(t)
				t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
				code := hostile.New(t, mode, func() {
					_ = m.Transaction(context.Background(), func(context.Context) error { return nil })
				})
				switch target {
				case "logger":
					m.SetLogger(hostile.NewLogger(code))
				case "dispatcher":
					m.SetEventDispatcher(hostile.NewDispatcher(code).Dispatch)
				}
				want := errors.New("fn failed")
				var later atomic.Int32
				call := func() {
					ctx := PrepareTxCallbacks(context.Background())
					err := m.Transaction(ctx, func(ctx context.Context) error {
						if target == "callback" {
							_ = OnRollback(ctx, func(context.Context) error { code.Run(); return nil })
						}
						_ = OnRollback(ctx, func(context.Context) error { later.Add(1); return nil })
						commitInside(ctx)
						return want
					})
					if !errors.Is(err, want) {
						t.Errorf("Transaction = %v, want fn's error", err)
					}
				}
				runContained(t, mode, code, call, managerAccessors(t, m))
				if got := later.Load(); got != 2 {
					t.Errorf("the callback after the hostile code ran %d times, want 2 (the hostile run and the retry)", got)
				}
			})
		}
	}
}

// hostileDriver runs its code from every method the manager calls on a
// driver.
type hostileDriver struct {
	nopDriver
	code *hostile.Code
}

func (d *hostileDriver) Close() error              { d.code.Run(); return nil }
func (d *hostileDriver) DB() *sql.DB               { d.code.Run(); return nil }
func (d *hostileDriver) DriverName() string        { d.code.Run(); return "hostile" }
func (d *hostileDriver) SetLogger(contract.Logger) { d.code.Run() }

// A hostile driver's Close and SetLogger are contained by the manager (the
// drivers after it are still closed or handed the logger); its DB and
// DriverName, called for the manager's caller, hand a panic to that caller
// and leave the manager usable. None runs under a manager lock.
func TestManager_HostileDriverSweep(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String()+"/Close", func(t *testing.T) {
			m := newTestManager(t)
			code := hostile.New(t, mode, func() { _ = m.DatabaseName(); _, _ = m.Connection("other") })
			m.AddConnection("hostile", &hostileDriver{code: code})
			after := &callbackDriver{}
			m.AddConnection("after", after)
			shutdown := func() { _ = m.Shutdown(context.Background()) }
			if mode == hostile.Block {
				go shutdown()
				<-code.Entered()
				hostile.Within(t, hostile.Deadline, func() { _ = m.DatabaseName(); m.SetLogger(nil) })
				code.Release()
			} else if p := hostile.Within(t, hostile.Deadline, shutdown); p != nil {
				t.Fatalf("a driver's Close panic escaped Shutdown: %v", p)
			}
			hostile.Within(t, hostile.Deadline, shutdown)
			if after.closes.Load() != 1 {
				t.Errorf("the driver after the hostile one closed %d times, want 1", after.closes.Load())
			}
		})
		t.Run(mode.String()+"/SetLogger", func(t *testing.T) {
			fallbacklogtest.Capture(t)
			m := newTestManager(t)
			t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
			code := hostile.New(t, mode, func() { _ = m.Logger(); _, _ = m.Connection("other") })
			m.AddConnection("hostile", &hostileDriver{code: code})
			after := &reentrantDriver{}
			handed := make(chan struct{}, 1)
			after.onSetLogger = func() { handed <- struct{}{} }
			m.AddConnection("after", after)
			runContained(t, mode, code, func() { m.SetLogger(hostile.NewLogger(nil)) }, managerAccessors(t, m))
			select {
			case <-handed:
			default:
				t.Error("the driver after the hostile one was not handed the logger")
			}
		})
		for _, entry := range []string{"DB", "DriverName"} {
			t.Run(mode.String()+"/"+entry, func(t *testing.T) {
				m := newTestManager(t)
				prev := m.DefaultDriver()
				code := hostile.New(t, mode, func() { m.SetLogger(nil); _ = m.DatabaseName() })
				m.mu.Lock()
				m.defaultDriver = &hostileDriver{code: code}
				m.mu.Unlock()
				call := func() { _ = m.DB() }
				if entry == "DriverName" {
					call = func() { _ = m.DriverName() }
				}
				switch mode {
				case hostile.Block:
					go call()
					<-code.Entered()
					hostile.Within(t, hostile.Deadline, func() { m.SetLogger(nil); _ = m.DatabaseName() })
				case hostile.Panic:
					if p := hostile.Within(t, hostile.Deadline, call); p != hostile.PanicValue {
						t.Fatalf("panic = %v, want the driver's own to reach the caller", p)
					}
				default:
					hostile.Within(t, hostile.Deadline, call)
				}
				code.Disarm()
				code.Release()
				hostile.Within(t, hostile.Deadline, call)
				m.mu.Lock()
				m.defaultDriver = prev
				m.mu.Unlock()
				_ = m.Shutdown(context.Background())
			})
		}
	}
}

// Statement-event listeners against hostile user code: the pump contains
// a panic, a Shutdown whose ctx ends while one blocks returns ctx's error,
// and a listener calling Shutdown or FlushQueryEvents is refused at once.
func TestQueryEventPump_HostileListenerSweep(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			fallbacklogtest.Capture(t)
			m := newTestManager(t)
			var reentryErrs []error
			code := hostile.New(t, mode, func() {
				reentryErrs = append(reentryErrs, m.Shutdown(context.Background()), m.FlushQueryEvents(context.Background()))
			})
			m.SetEventDispatcher(hostile.NewDispatcher(code).Dispatch)
			if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
				t.Fatalf("exec: %v", err)
			}
			<-code.Entered()
			if mode == hostile.Block {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				hostile.Within(t, hostile.Deadline, func() {
					if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
						t.Errorf("Shutdown while a listener blocks = %v, want ctx's DeadlineExceeded", err)
					}
				})
			}
			code.Disarm()
			code.Release()
			hostile.Within(t, hostile.Deadline, func() {
				if err := m.Shutdown(context.Background()); err != nil {
					t.Errorf("Shutdown once the listener behaved: %v", err)
				}
			})
			if mode == hostile.Reenter {
				for _, err := range reentryErrs {
					if !errors.Is(err, ErrQueryEventsFlushFromPump) {
						t.Errorf("a listener's Shutdown or flush = %v, want ErrQueryEventsFlushFromPump", err)
					}
				}
			}
		})
	}
}

// The outbox relay against a hostile dispatch callback or logger: the row
// is recorded (dead-lettered after its attempts), no panic leaves a relay
// goroutine, Stop honours its ctx while one blocks, and a Stop from the
// relay's own goroutine is refused.
func TestRelay_HostileSweep(t *testing.T) {
	for _, mode := range hostile.Modes() {
		for _, target := range []string{"callback", "logger"} {
			t.Run(mode.String()+"/"+target, func(t *testing.T) {
				fallbacklogtest.Capture(t)
				m, _ := newOutboxFileManager(t)
				enqueueOne(t, m)
				var relay *Relay
				var reentryErr atomic.Pointer[error]
				code := hostile.New(t, mode, func() {
					err := relay.Stop(context.Background())
					reentryErr.Store(&err)
				})
				cb := RelayCallbacks{OnJob: func(context.Context, any, string, string) error {
					if target == "callback" {
						code.Run()
						return errors.New("job failed")
					}
					// The worker writes this panic's line through the logger.
					panic("job boom")
				}}
				relay = NewRelay(m, cb, RelayConfig{
					PollInterval: 5 * time.Millisecond, LeaseDuration: 30 * time.Millisecond,
					BackoffBase: time.Millisecond, BackoffMax: 5 * time.Millisecond,
					MaxAttempts: 2, ShutdownGrace: 20 * time.Millisecond,
				})
				if target == "logger" {
					relay.SetLogger(hostile.NewLogger(code))
				}
				if err := relay.Start(context.Background()); err != nil {
					t.Fatalf("Start: %v", err)
				}
				if mode == hostile.Block {
					<-code.Entered()
					ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
					defer cancel()
					hostile.Within(t, hostile.Deadline, func() {
						if err := relay.Stop(ctx); err == nil {
							t.Error("Stop while user code blocks a relay goroutine = nil, want ctx's error")
						}
					})
					code.Release()
					return
				}
				waitFor(t, 3*time.Second, func() bool {
					rows, _ := m.ListOutboxRows(context.Background(), 10)
					return len(rows) == 1 && rows[0].DLQ
				})
				hostile.Within(t, hostile.Deadline, func() {
					if err := relay.Stop(context.Background()); err != nil {
						t.Errorf("Stop: %v", err)
					}
				})
				if mode == hostile.Reenter {
					if p := reentryErr.Load(); p == nil || *p == nil {
						t.Error("Stop from a relay goroutine returned nil, want an error")
					}
				}
			})
		}
	}
}

// sweepTableCode is the code sweepTableModel's TableName runs.
var sweepTableCode atomic.Pointer[hostile.Code]

type sweepTableModel struct{}

func (sweepTableModel) TableName() string {
	sweepTableCode.Load().Run()
	return "sweep_models"
}

// A TableName that panics, blocks or derives another table: the panic
// reaches the caller and is not cached, a block holds up no other type's
// derivation, re-entry returns, and the name resolves once it behaves.
func TestDeriveTableName_HostileSweep(t *testing.T) {
	typ := reflect.TypeOf(sweepTableModel{})
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			tableNameCache.Delete(typ)
			code := hostile.New(t, mode, func() { _ = deriveTableName(reflect.TypeOf(b7UserProfile{})) })
			sweepTableCode.Store(code)
			t.Cleanup(func() { sweepTableCode.Store(nil) })
			call := func() { _ = deriveTableName(typ) }
			switch mode {
			case hostile.Block:
				go call()
				<-code.Entered()
				hostile.Within(t, hostile.Deadline, func() {
					tableNameCache.Delete(reflect.TypeOf(b7UserProfile{}))
					_ = deriveTableName(reflect.TypeOf(b7UserProfile{}))
				})
			case hostile.Panic:
				if p := hostile.Within(t, hostile.Deadline, call); p != hostile.PanicValue {
					t.Fatalf("panic = %v, want TableName's own to reach the caller", p)
				}
			default:
				hostile.Within(t, hostile.Deadline, call)
			}
			code.Disarm()
			code.Release()
			hostile.Within(t, hostile.Deadline, func() {
				if got := deriveTableName(typ); got != "sweep_models" {
					t.Errorf("table = %q once TableName behaved, want sweep_models", got)
				}
			})
		})
	}
}
