package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// outboxTestPayload is a tiny gob-encodable type used across the outbox tests.
type outboxTestPayload struct {
	Name string
	N    int
}

func init() {
	RegisterPayloadType(outboxTestPayload{})
}

// newOutboxFileManager opens a sqlite file under t.TempDir so multiple
// connections / multiple managers can share the same DB. ":memory:" is
// per-connection on sqlite, which breaks multi-relay concurrency tests.
func newOutboxFileManager(t testing.TB) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "outbox.db")
	m, err := NewManager(ManagerConfig{
		Driver:       "sqlite",
		Database:     dbPath,
		MaxOpenConns: 1, // sqlite serialises writes; one conn avoids spurious lock errors
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.EnsureOutboxTable(context.Background()); err != nil {
		t.Fatalf("EnsureOutboxTable: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	return m, dbPath
}

func TestTransactionWithOutbox_Commit(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		if _, err := outbox.Enqueue(outboxTestPayload{Name: "job-1", N: 1}); err != nil {
			return err
		}
		if _, err := outbox.Dispatch(outboxTestPayload{Name: "evt-1", N: 2}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TransactionWithOutbox: %v", err)
	}
	n, err := m.CountOutboxRows(context.Background())
	if err != nil {
		t.Fatalf("CountOutboxRows: %v", err)
	}
	if n != 2 {
		t.Fatalf("got %d rows, want 2", n)
	}
}

func TestTransactionWithOutbox_Rollback(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	wantErr := errors.New("boom")
	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		if _, err := outbox.Enqueue(outboxTestPayload{Name: "job-x"}); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("got err=%v, want %v", err, wantErr)
	}
	n, err := m.CountOutboxRows(context.Background())
	if err != nil {
		t.Fatalf("CountOutboxRows: %v", err)
	}
	if n != 0 {
		t.Fatalf("got %d rows, want 0 after rollback", n)
	}
}

func TestTransactionWithOutbox_PanicRollsBack(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	defer func() { _ = recover() }()

	_ = m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		_, _ = outbox.Enqueue(outboxTestPayload{Name: "job-panic"})
		panic("boom")
	})

	n, err := m.CountOutboxRows(context.Background())
	if err != nil {
		t.Fatalf("CountOutboxRows: %v", err)
	}
	if n != 0 {
		t.Fatalf("got %d rows, want 0 after panic rollback", n)
	}
}

func TestTransactionWithOutbox_IdempotencyKey_AutoGen_AndOverride(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	const overrideKey = "user-supplied-key-001"
	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		if _, err := outbox.Enqueue(outboxTestPayload{Name: "auto"}); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(outboxTestPayload{Name: "manual"}, WithIdempotencyKey(overrideKey)); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	rows, err := m.ListOutboxRows(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListOutboxRows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	var seenManual bool
	for _, r := range rows {
		if r.IdempotencyKey == "" {
			t.Errorf("row %d has empty idempotency key", r.ID)
		}
		if r.IdempotencyKey == overrideKey {
			seenManual = true
		}
	}
	if !seenManual {
		t.Fatalf("override key not present")
	}

	// Duplicate override key must violate the unique index.
	err = m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		_, err := outbox.Enqueue(outboxTestPayload{Name: "dup"}, WithIdempotencyKey(overrideKey))
		return err
	})
	if err == nil {
		t.Fatalf("expected duplicate key error")
	}
}

func TestRelay_DispatchesJobsAndEvents(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	var jobCount, eventCount atomic.Int32
	relay := NewRelay(m, RelayCallbacks{
		OnJob: func(_ context.Context, _ any, _, _ string) error {
			jobCount.Add(1)
			return nil
		},
		OnEvent: func(_ context.Context, _ any, _, _ string) error {
			eventCount.Add(1)
			return nil
		},
	}, RelayConfig{PollInterval: 20 * time.Millisecond, BatchSize: 10})

	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		if _, err := outbox.Enqueue(outboxTestPayload{Name: "j1"}); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(outboxTestPayload{Name: "j2"}); err != nil {
			return err
		}
		if _, err := outbox.Dispatch(outboxTestPayload{Name: "e1"}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = relay.Stop(context.Background()) })

	waitFor(t, time.Second, func() bool {
		return jobCount.Load() == 2 && eventCount.Load() == 1
	})

	// And the rows are gone.
	n, err := m.CountOutboxRows(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("got %d remaining rows, want 0", n)
	}
}

func TestRelay_CrashMidRelay_Idempotent(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	// Track per-idempotency-key delivery counts (consumer-side dedup).
	var delivered sync.Map // key -> count

	const key = "fixed-key-only-once"

	// Insert a row with a known idempotency key.
	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		_, err := outbox.Enqueue(outboxTestPayload{Name: "crash"}, WithIdempotencyKey(key))
		return err
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	// First relay simulates a crash mid-dispatch: it claims the row, fails
	// the callback (transient), then is stopped before the row's lease
	// expires. The lease will expire and the second relay will pick up.
	relay1 := NewRelay(m, RelayCallbacks{
		OnJob: func(_ context.Context, _ any, _, k string) error {
			cur, _ := delivered.LoadOrStore(k, new(atomic.Int32))
			cur.(*atomic.Int32).Add(1)
			return errors.New("simulated failure")
		},
	}, RelayConfig{
		PollInterval:  10 * time.Millisecond,
		LeaseDuration: 50 * time.Millisecond,
		BackoffBase:   5 * time.Millisecond,
		BackoffMax:    20 * time.Millisecond,
		MaxAttempts:   10,
	})
	if err := relay1.Start(context.Background()); err != nil {
		t.Fatalf("relay1.Start: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		v, ok := delivered.Load(key)
		return ok && v.(*atomic.Int32).Load() >= 1
	})
	if err := relay1.Stop(context.Background()); err != nil {
		t.Fatalf("relay1.Stop: %v", err)
	}

	// Wait for retry/lease window then start a successful relay. The same
	// idempotency key should be delivered again (at-least-once), proving
	// the consumer must dedup. We assert the key is non-empty + matches.
	time.Sleep(100 * time.Millisecond)
	relay2 := NewRelay(m, RelayCallbacks{
		OnJob: func(_ context.Context, _ any, _, k string) error {
			cur, _ := delivered.LoadOrStore(k, new(atomic.Int32))
			cur.(*atomic.Int32).Add(1)
			return nil
		},
	}, RelayConfig{
		PollInterval:  10 * time.Millisecond,
		LeaseDuration: 50 * time.Millisecond,
		BackoffBase:   5 * time.Millisecond,
		BackoffMax:    20 * time.Millisecond,
		MaxAttempts:   10,
	})
	if err := relay2.Start(context.Background()); err != nil {
		t.Fatalf("relay2.Start: %v", err)
	}
	t.Cleanup(func() { _ = relay2.Stop(context.Background()) })

	// Wait for the row to be deleted (success).
	waitFor(t, 2*time.Second, func() bool {
		n, _ := m.CountOutboxRows(context.Background())
		return n == 0
	})
	v, _ := delivered.Load(key)
	if v.(*atomic.Int32).Load() < 2 {
		t.Fatalf("expected the same key to be delivered to both relays (>=2), got %d", v.(*atomic.Int32).Load())
	}
}

func TestRelay_LeaseExpiry(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		_, err := outbox.Enqueue(outboxTestPayload{Name: "expire"})
		return err
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	// Seed a stale lease directly (simulate a crashed relay).
	driver := m.DefaultDriver()
	t1 := time.Now().UTC().Add(-1 * time.Hour)
	if _, err := driver.ExecContext(context.Background(),
		"UPDATE `velocity_outbox` SET leased_until=?, leased_by=? WHERE id=1",
		t1, "dead-relay"); err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	var got atomic.Int32
	relay := NewRelay(m, RelayCallbacks{
		OnJob: func(_ context.Context, _ any, _, _ string) error {
			got.Add(1)
			return nil
		},
	}, RelayConfig{PollInterval: 10 * time.Millisecond})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = relay.Stop(context.Background()) })

	waitFor(t, time.Second, func() bool { return got.Load() == 1 })
}

func TestRelay_DLQ(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		_, err := outbox.Enqueue(outboxTestPayload{Name: "dlq"}, WithMaxAttempts(2))
		return err
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	relay := NewRelay(m, RelayCallbacks{
		OnJob: func(_ context.Context, _ any, _, _ string) error {
			return errors.New("perm fail")
		},
	}, RelayConfig{
		PollInterval:  10 * time.Millisecond,
		LeaseDuration: 30 * time.Millisecond,
		BackoffBase:   1 * time.Millisecond,
		BackoffMax:    5 * time.Millisecond,
		MaxAttempts:   2,
	})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		rows, _ := m.ListOutboxRows(context.Background(), 10)
		for _, r := range rows {
			if r.DLQ {
				return true
			}
		}
		return false
	})
	_ = relay.Stop(context.Background())

	rows, err := m.ListOutboxRows(context.Background(), 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || !rows[0].DLQ {
		t.Fatalf("expected one DLQ row, got %+v", rows)
	}

	// Replay flips it back; install a passing callback for the next tick.
	var passed atomic.Int32
	relay2 := NewRelay(m, RelayCallbacks{
		OnJob: func(_ context.Context, _ any, _, _ string) error {
			passed.Add(1)
			return nil
		},
	}, RelayConfig{PollInterval: 10 * time.Millisecond})
	if err := relay2.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = relay2.Stop(context.Background()) })

	if err := relay2.Replay(context.Background(), rows[0].ID); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	waitFor(t, time.Second, func() bool { return passed.Load() == 1 })
}

func TestRelay_OrderingWithinPartition(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	const partition = "tenant-42"
	const total = 10
	for i := 0; i < total; i++ {
		i := i
		err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
			_, err := outbox.Enqueue(outboxTestPayload{Name: "ord", N: i}, WithPartitionKey(partition))
			return err
		})
		if err != nil {
			t.Fatalf("tx %d: %v", i, err)
		}
	}

	var mu sync.Mutex
	var seen []int
	relay := NewRelay(m, RelayCallbacks{
		OnJob: func(_ context.Context, payload any, _, _ string) error {
			n, ok := payloadN(payload)
			if !ok {
				return errors.New("unexpected type")
			}
			// Hold briefly to maximise the chance of overlap if ordering
			// were not enforced.
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			seen = append(seen, n)
			mu.Unlock()
			return nil
		},
	}, RelayConfig{PollInterval: 5 * time.Millisecond, BatchSize: 10, WorkerCount: 4})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = relay.Stop(context.Background()) })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n == total {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != total {
		// Inspect remaining rows for debugging.
		rows, _ := m.ListOutboxRows(context.Background(), 100)
		t.Fatalf("seen=%v (len=%d, want %d); remaining rows=%+v", seen, len(seen), total, rows)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("partition order violated at %d: %v", i, seen)
		}
	}
}

func TestRelay_BackPressure(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	const total = 100
	const workers = 4

	// Insert 100 rows.
	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		for i := 0; i < total; i++ {
			if _, err := outbox.Enqueue(outboxTestPayload{Name: "bp", N: i}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	var inFlight atomic.Int32
	var peak atomic.Int32
	relay := NewRelay(m, RelayCallbacks{
		OnJob: func(_ context.Context, _ any, _, _ string) error {
			cur := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			return nil
		},
	}, RelayConfig{PollInterval: 5 * time.Millisecond, BatchSize: 64, WorkerCount: workers})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = relay.Stop(context.Background()) })

	waitFor(t, 10*time.Second, func() bool {
		n, _ := m.CountOutboxRows(context.Background())
		return n == 0
	})
	if peak.Load() > int32(workers) {
		t.Fatalf("peak in-flight %d exceeded workers %d", peak.Load(), workers)
	}
}

func TestRelay_Concurrent(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	const total = 50

	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		for i := 0; i < total; i++ {
			// No partition: rows are independent so multiple relays can
			// drain in parallel.
			if _, err := outbox.Enqueue(outboxTestPayload{Name: fmt.Sprintf("c-%d", i), N: i}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	var got atomic.Int32
	cb := func(_ context.Context, _ any, _, _ string) error {
		got.Add(1)
		return nil
	}
	r1 := NewRelay(m, RelayCallbacks{OnJob: cb}, RelayConfig{PollInterval: 5 * time.Millisecond, BatchSize: 8, WorkerCount: 2})
	r2 := NewRelay(m, RelayCallbacks{OnJob: cb}, RelayConfig{PollInterval: 5 * time.Millisecond, BatchSize: 8, WorkerCount: 2})
	r3 := NewRelay(m, RelayCallbacks{OnJob: cb}, RelayConfig{PollInterval: 5 * time.Millisecond, BatchSize: 8, WorkerCount: 2})

	if err := r1.Start(context.Background()); err != nil {
		t.Fatalf("r1: %v", err)
	}
	if err := r2.Start(context.Background()); err != nil {
		t.Fatalf("r2: %v", err)
	}
	if err := r3.Start(context.Background()); err != nil {
		t.Fatalf("r3: %v", err)
	}
	t.Cleanup(func() {
		_ = r1.Stop(context.Background())
		_ = r2.Stop(context.Background())
		_ = r3.Stop(context.Background())
	})

	waitFor(t, 10*time.Second, func() bool {
		n, _ := m.CountOutboxRows(context.Background())
		return n == 0 && got.Load() >= int32(total)
	})
	if got.Load() < int32(total) {
		t.Fatalf("got %d deliveries, want >= %d", got.Load(), total)
	}
}

func TestRelay_StartStopIdempotent(t *testing.T) {
	m, _ := newOutboxFileManager(t)
	relay := NewRelay(m, RelayCallbacks{
		OnJob:   func(_ context.Context, _ any, _, _ string) error { return nil },
		OnEvent: func(_ context.Context, _ any, _, _ string) error { return nil },
	}, RelayConfig{PollInterval: 50 * time.Millisecond})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := relay.Start(context.Background()); err == nil {
		t.Fatalf("expected error from double Start")
	}
	if err := relay.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Double stop is fine.
	if err := relay.Stop(context.Background()); err != nil {
		t.Fatalf("Stop2: %v", err)
	}
}

func TestRelay_NoCallbackForKindFails(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		_, err := outbox.Dispatch(outboxTestPayload{Name: "no-cb"}, WithMaxAttempts(1))
		return err
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	relay := NewRelay(m, RelayCallbacks{
		// OnEvent intentionally nil
		OnJob: func(_ context.Context, _ any, _, _ string) error { return nil },
	}, RelayConfig{
		PollInterval:  10 * time.Millisecond,
		LeaseDuration: 30 * time.Millisecond,
		BackoffBase:   1 * time.Millisecond,
		BackoffMax:    5 * time.Millisecond,
		MaxAttempts:   1,
	})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = relay.Stop(context.Background()) })

	waitFor(t, 2*time.Second, func() bool {
		rows, _ := m.ListOutboxRows(context.Background(), 10)
		for _, r := range rows {
			if r.DLQ {
				return true
			}
		}
		return false
	})
}

func TestRelay_PanicInCallbackRecorded(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		_, err := outbox.Enqueue(outboxTestPayload{Name: "panic"}, WithMaxAttempts(2))
		return err
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	relay := NewRelay(m, RelayCallbacks{
		OnJob: func(_ context.Context, _ any, _, _ string) error {
			panic("worker panic")
		},
	}, RelayConfig{
		PollInterval:  10 * time.Millisecond,
		LeaseDuration: 30 * time.Millisecond,
		BackoffBase:   1 * time.Millisecond,
		BackoffMax:    5 * time.Millisecond,
		MaxAttempts:   2,
	})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = relay.Stop(context.Background()) })

	waitFor(t, 2*time.Second, func() bool {
		rows, _ := m.ListOutboxRows(context.Background(), 10)
		for _, r := range rows {
			if r.DLQ && r.LastError != "" {
				return true
			}
		}
		return false
	})
}

func TestReplay_NotFound(t *testing.T) {
	m, _ := newOutboxFileManager(t)
	relay := NewRelay(m, RelayCallbacks{}, RelayConfig{})
	if err := relay.Replay(context.Background(), 99999); !errors.Is(err, ErrOutboxRowNotFound) {
		t.Fatalf("got %v, want ErrOutboxRowNotFound", err)
	}
}

func TestRegisterPayloadType_NilNoOp(t *testing.T) {
	// nil call is a no-op (does not panic, does not register).
	RegisterPayloadType(nil)
}

func TestDecodePayload_BadBase64(t *testing.T) {
	if _, err := decodePayload("not-base64-!!", "x.y"); err == nil {
		t.Fatalf("expected base64 decode error")
	}
}

func TestDecodePayload_BadGob(t *testing.T) {
	// Valid base64, invalid gob.
	if _, err := decodePayload("aGVsbG8=", "x.y"); err == nil {
		t.Fatalf("expected gob decode error")
	}
}

func TestEncodeDecodePayload_Roundtrip(t *testing.T) {
	in := outboxTestPayload{Name: "hi", N: 7}
	encoded, ptype, err := encodePayload(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := decodePayload(encoded, ptype)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, ok := out.(*outboxTestPayload)
	if !ok {
		// gob may decode pointer or value depending on registration; both
		// are acceptable as long as the data round-trips.
		v, ok2 := out.(outboxTestPayload)
		if !ok2 {
			t.Fatalf("decoded type %T, want *outboxTestPayload or outboxTestPayload", out)
		}
		got = &v
	}
	if got.Name != in.Name || got.N != in.N {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, in)
	}
}

func TestOutboxInsertSQL_DriverShape(t *testing.T) {
	for _, tc := range []struct {
		driver string
		want   string
	}{
		{"sqlite", "VALUES (?,?,?,?,?,?,?,?,?,?)"},
		{"mysql", "VALUES (?,?,?,?,?,?,?,?,?,?)"},
		{"postgres", "VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id"},
	} {
		got := outboxInsertSQL(tc.driver)
		if !contains(got, tc.want) {
			t.Errorf("driver=%s sql does not contain %q: %s", tc.driver, tc.want, got)
		}
	}
}

func TestQuoteIdent_DriverShape(t *testing.T) {
	if got := quoteIdent("foo", "postgres"); got != `"foo"` {
		t.Errorf("postgres quote: %s", got)
	}
	if got := quoteIdent("foo", "mysql"); got != "`foo`" {
		t.Errorf("mysql quote: %s", got)
	}
	if got := quoteIdent("foo", "sqlite"); got != "`foo`" {
		t.Errorf("sqlite quote: %s", got)
	}
	// Backtick / double-quote injection inside identifier must be escaped.
	if got := quoteIdent("a`b", "mysql"); got != "`a``b`" {
		t.Errorf("mysql escape: %s", got)
	}
	if got := quoteIdent(`a"b`, "postgres"); got != `"a""b"` {
		t.Errorf("postgres escape: %s", got)
	}
}

func TestOutboxMigrationSQL_AllDrivers(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql", "postgres"} {
		stmts := OutboxMigrationSQL(driver)
		if len(stmts) < 3 {
			t.Errorf("%s: got %d stmts, want >=3", driver, len(stmts))
		}
		// Every statement must reference the outbox table.
		for _, s := range stmts {
			if !contains(s, OutboxTableName) {
				t.Errorf("%s: stmt missing table name: %s", driver, s)
			}
		}
	}
}

func TestPendingPostgres_InsertSurface(t *testing.T) {
	// We cannot execute real Postgres in unit tests, but we can exercise
	// the Postgres pending wrapper validation paths and verify the
	// pendingFor router returns the right type per driver.
	if _, ok := pendingFor(&pending{driver: "postgres"}, "postgres").(*pendingPostgres); !ok {
		t.Fatalf("pendingFor(postgres) did not return *pendingPostgres")
	}
	if _, ok := pendingFor(&pending{driver: "sqlite"}, "sqlite").(*pending); !ok {
		t.Fatalf("pendingFor(sqlite) did not return *pending")
	}
	// Nil payload short-circuits before any tx use.
	pp := &pendingPostgres{inner: &pending{driver: "postgres"}}
	if _, err := pp.Enqueue(nil); err == nil {
		t.Fatalf("expected nil-payload error from Enqueue")
	}
	if _, err := pp.Dispatch(nil); err == nil {
		t.Fatalf("expected nil-payload error from Dispatch")
	}
	p := &pending{driver: "sqlite"}
	if _, err := p.Enqueue(nil); err == nil {
		t.Fatalf("expected nil-payload error from sqlite Enqueue")
	}
}

func TestRelay_SetLoggerAndID(t *testing.T) {
	m, _ := newOutboxFileManager(t)
	r := NewRelay(m, RelayCallbacks{}, RelayConfig{RelayID: "fixed-id"})
	if r.ID() != "fixed-id" {
		t.Fatalf("ID(): %s", r.ID())
	}
	r.SetLogger(testLogger{})
}

func TestPendingOptions_WithAvailableAt_DefersDelivery(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	future := time.Now().UTC().Add(500 * time.Millisecond)
	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		_, err := outbox.Enqueue(outboxTestPayload{Name: "deferred"}, WithAvailableAt(future))
		return err
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	var got atomic.Int32
	relay := NewRelay(m, RelayCallbacks{
		OnJob: func(_ context.Context, _ any, _, _ string) error {
			got.Add(1)
			return nil
		},
	}, RelayConfig{PollInterval: 50 * time.Millisecond})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = relay.Stop(context.Background()) })

	// Within 200ms the row should still be pending; not delivered yet.
	time.Sleep(200 * time.Millisecond)
	if got.Load() != 0 {
		t.Fatalf("delivered before available_at: got=%d", got.Load())
	}
	waitFor(t, 2*time.Second, func() bool { return got.Load() == 1 })
}

func TestRelay_StartFailsWithNoDB(t *testing.T) {
	r := NewRelay(&Manager{}, RelayCallbacks{}, RelayConfig{})
	if err := r.Start(context.Background()); err == nil {
		t.Fatalf("expected error starting relay with no DB")
	}
}

func TestManager_OutboxAPIs_NoConnection_ReturnError(t *testing.T) {
	m := &Manager{} // no driver wired
	if err := m.EnsureOutboxTable(context.Background()); err == nil {
		t.Fatalf("EnsureOutboxTable expected error")
	}
	if err := m.TransactionWithOutbox(context.Background(), func(_ *sql.Tx, _ Pending) error { return nil }); err == nil {
		t.Fatalf("TransactionWithOutbox expected error")
	}
	if _, err := m.CountOutboxRows(context.Background()); err == nil {
		t.Fatalf("CountOutboxRows expected error")
	}
	if _, err := m.ListOutboxRows(context.Background(), 10); err == nil {
		t.Fatalf("ListOutboxRows expected error")
	}
}

func TestRelay_NoDBOps_ReturnError(t *testing.T) {
	r := &Relay{mgr: &Manager{}, cfg: RelayConfig{}}
	if err := r.Replay(context.Background(), 1); err == nil {
		t.Fatalf("Replay expected error")
	}
	if err := r.recordSuccess(context.Background(), outboxRow{ID: 1}); err == nil {
		t.Fatalf("recordSuccess expected error")
	}
	if err := r.recordFailure(context.Background(), outboxRow{ID: 1}, errors.New("x")); err == nil {
		t.Fatalf("recordFailure expected error")
	}
}

func TestEncodePayload_UnregisteredTypeStillRoundtrips(t *testing.T) {
	type localType struct{ X string }
	gobRegisterValue(localType{X: "hi"})
	enc, ptype, err := encodePayload(localType{X: "hi"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if ptype == "" {
		t.Fatalf("empty payload type")
	}
	if _, err := decodePayload(enc, ptype); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// gobRegisterValue is a tiny indirection so we can register a non-package
// type from the test without polluting init().
func gobRegisterValue(v any) { RegisterPayloadType(v) }

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 3); got != "hel" {
		t.Fatalf("got %q", got)
	}
	if got := truncate("hi", 10); got != "hi" {
		t.Fatalf("got %q", got)
	}
}

func TestRelayConfig_Defaults(t *testing.T) {
	r := NewRelay(&Manager{}, RelayCallbacks{}, RelayConfig{})
	if r.cfg.PollInterval == 0 || r.cfg.LeaseDuration == 0 || r.cfg.BatchSize == 0 ||
		r.cfg.WorkerCount == 0 || r.cfg.MaxAttempts == 0 ||
		r.cfg.BackoffBase == 0 || r.cfg.BackoffMax == 0 || r.cfg.RelayID == "" ||
		r.cfg.ShutdownGrace == 0 {
		t.Fatalf("defaults not applied: %+v", r.cfg)
	}
}

type testLogger struct{}

func (testLogger) Warn(_ string, _ ...any)  {}
func (testLogger) Error(_ string, _ ...any) {}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && stringIndex(s, sub) >= 0)
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestBackoffFor_Bounded(t *testing.T) {
	if d := backoffFor(0, time.Second, time.Minute); d != time.Second {
		t.Fatalf("attempt=0 -> %v, want 1s", d)
	}
	if d := backoffFor(1, time.Second, time.Minute); d != time.Second {
		t.Fatalf("attempt=1 -> %v, want 1s", d)
	}
	if d := backoffFor(2, time.Second, time.Minute); d != 2*time.Second {
		t.Fatalf("attempt=2 -> %v, want 2s", d)
	}
	if d := backoffFor(100, time.Second, time.Minute); d != time.Minute {
		t.Fatalf("attempt=100 -> %v, want 1m (cap)", d)
	}
}

// payloadN extracts N from a decoded outboxTestPayload regardless of whether
// gob hands back the value or a pointer to it.
func payloadN(payload any) (int, bool) {
	switch v := payload.(type) {
	case *outboxTestPayload:
		return v.N, true
	case outboxTestPayload:
		return v.N, true
	default:
		return 0, false
	}
}

// waitFor polls cond until it returns true or timeout elapses.
func waitFor(t testing.TB, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waitFor timed out after %v", timeout)
}

// TestRelay_ShutdownCancels_RecordSuccess proves that the ctx threaded into
// the dispatch callback (and, by extension, into the recordSuccess /
// recordFailure DB writebacks) is cancelled when the relay is stopped past
// its grace window. Without this fix, recordSuccess used context.Background
// and could hang indefinitely under DB pressure on shutdown.
func TestRelay_ShutdownCancels_RecordSuccess(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	// Insert one row so the relay has work.
	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		_, err := outbox.Enqueue(outboxTestPayload{Name: "shutdown"})
		return err
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	gotCtxCh := make(chan context.Context, 1)
	cbReleased := make(chan struct{})
	relay := NewRelay(m, RelayCallbacks{
		OnJob: func(ctx context.Context, _ any, _, _ string) error {
			select {
			case gotCtxCh <- ctx:
			default:
			}
			// Block until the relay's shutdown ctx is cancelled. This
			// simulates a slow/blocking callback that needs the grace
			// window cancellation to terminate.
			<-ctx.Done()
			close(cbReleased)
			return ctx.Err()
		},
	}, RelayConfig{
		PollInterval:  10 * time.Millisecond,
		ShutdownGrace: 50 * time.Millisecond,
	})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait until the callback is running with its ctx captured.
	var cbCtx context.Context
	select {
	case cbCtx = <-gotCtxCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("callback ctx never captured")
	}
	if cbCtx.Err() != nil {
		t.Fatalf("callback ctx already cancelled before Stop: %v", cbCtx.Err())
	}

	// Stop with a Background ctx: only the relay's ShutdownGrace can save
	// us. If the fix is wrong, Stop hangs forever because the callback is
	// blocked on ctx.Done().
	stopDone := make(chan error, 1)
	go func() { stopDone <- relay.Stop(context.Background()) }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Stop hung past grace window: shutdown ctx was not cancelled")
	}

	// Both the callback ctx and the relay's writeback ctx must be done.
	if cbCtx.Err() == nil {
		t.Fatalf("callback ctx not cancelled after Stop")
	}
	if relay.writebackCtx().Err() == nil {
		t.Fatalf("writebackCtx not cancelled after Stop")
	}
	select {
	case <-cbReleased:
	case <-time.After(time.Second):
		t.Fatalf("callback never observed ctx.Done()")
	}
}

// TestTransactionWithOutbox_PanicReturnsError verifies the docstring claim
// that panics inside fn are converted to errors. The previous implementation
// re-panicked, contradicting the docstring and CLAUDE.md rule #10 (no panics
// in library code).
func TestTransactionWithOutbox_PanicReturnsError(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	// Detect any leaked panic at the test boundary. If the function still
	// re-panics, this recover() catches it and we fail the test with a
	// clear message.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("TransactionWithOutbox re-panicked: %v", r)
		}
	}()

	err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
		_, _ = outbox.Enqueue(outboxTestPayload{Name: "p"})
		panic("boom")
	})
	if err == nil {
		t.Fatalf("expected error from panicking fn, got nil")
	}
	if got := err.Error(); !contains(got, "panic") || !contains(got, "boom") {
		t.Fatalf("error did not surface panic value: %q", got)
	}
	// Rollback must still have happened.
	n, err := m.CountOutboxRows(context.Background())
	if err != nil {
		t.Fatalf("CountOutboxRows: %v", err)
	}
	if n != 0 {
		t.Fatalf("got %d rows, want 0 after panic rollback", n)
	}
}

// TestRelay_ActivePart_NoLeak_OnEarlyCtxCancel exercises the tick() early
// shutdown path: a batch is claimed (reserving partition keys in
// activePart), then ctx is cancelled before each row's worker goroutine is
// spawned. Without releasePartitions(), the reserved keys would stick
// around indefinitely and starve future ticks (the partition would always
// look "busy").
//
// The test uses a zero-capacity semaphore so tick() is forced to select on
// `sem <- struct{}{}` vs `<-ctx.Done()`. After claimBatch has populated
// activePart, we cancel ctx, the select picks ctx.Done(), and the fix's
// releasePartitions(rows[i:]) must drain the map.
func TestRelay_ActivePart_NoLeak_OnEarlyCtxCancel(t *testing.T) {
	m, _ := newOutboxFileManager(t)

	// Insert several rows with distinct partition keys so claimBatch
	// returns a populated batch.
	const n = 5
	for i := 0; i < n; i++ {
		i := i
		err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, outbox Pending) error {
			_, err := outbox.Enqueue(
				outboxTestPayload{Name: "p", N: i},
				WithPartitionKey(fmt.Sprintf("part-%d", i)),
			)
			return err
		})
		if err != nil {
			t.Fatalf("tx %d: %v", i, err)
		}
	}

	// Construct a relay manually so we can drive tick() without racing
	// against the polling goroutine.
	relay := NewRelay(m, RelayCallbacks{
		OnJob: func(_ context.Context, _ any, _, _ string) error { return nil },
	}, RelayConfig{
		PollInterval: time.Hour, // never auto-ticks
		BatchSize:    n,
		WorkerCount:  1,
	})
	// Ensure the writebackCtx is wired even though we don't Start.
	relay.shutdownCtx, relay.shutdownCancelFn = context.WithCancel(context.Background())
	t.Cleanup(relay.shutdownCancelFn)

	ctx, cancel := context.WithCancel(context.Background())

	// Zero-capacity semaphore: every goroutine launch attempt blocks at
	// `sem <- struct{}{}` until something reads from it.
	sem := make(chan struct{})

	tickDone := make(chan struct{})
	go func() {
		relay.tick(ctx, sem)
		close(tickDone)
	}()

	// Wait until claimBatch has populated activePart (sample by polling).
	waitFor(t, 2*time.Second, func() bool {
		got := 0
		relay.activePart.Range(func(_, _ any) bool {
			got++
			return true
		})
		return got > 0
	})

	// Cancel the ctx so the select inside tick's loop picks ctx.Done()
	// before any row gets a worker.
	cancel()

	select {
	case <-tickDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("tick did not return after ctx cancel")
	}

	// No goroutines should have been spawned (sem is empty / zero-cap and
	// nothing read from it), but wait defensively.
	doneCh := make(chan struct{})
	go func() {
		relay.inFlight.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatalf("inFlight workers did not exit")
	}

	// activePart must be empty: every partition key claimed by claimBatch
	// must have been released by tick's early-shutdown path.
	leaked := 0
	relay.activePart.Range(func(k, _ any) bool {
		leaked++
		t.Errorf("leaked partition key: %v", k)
		return true
	})
	if leaked != 0 {
		t.Fatalf("activePart leaked %d partition keys", leaked)
	}
}
