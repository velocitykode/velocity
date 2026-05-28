// Package orm
//
// Transactional outbox pattern (spec: HIGH-VALUE-FEATURES.md #3).
//
// TransactionWithOutbox wraps a normal SQL transaction and exposes a Pending
// handle whose Enqueue/Dispatch methods write rows into the outbox table
// using the same *sql.Tx. On commit, the rows persist; on rollback, they are
// discarded with the rest of the transaction. A separate Relay process drains
// the table to the queue / events dispatcher with lease-based claim, retry,
// and DLQ semantics.
//
// Payload encoding: encoding/gob, base64-wrapped into a TEXT column. gob is
// stdlib, handles arbitrary Go values (including time.Time and unexported
// fields via Encoder/Decoder), and base64 keeps the payload portable across
// SQLite/MySQL/Postgres TEXT columns without driver-specific BLOB handling.
// Decode requires the concrete type to be registered via RegisterPayloadType.
//
// SQL: identifiers are constants validated against ddlIdentifierRegex at
// startup; operators are not user-supplied; all values bind via parameters.
// Driver-aware quoting (backticks for MySQL/SQLite, double-quotes for
// Postgres) and placeholder ($N for Postgres, ? otherwise) are derived from
// Manager.DriverName.

package orm

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
)

// OutboxTableName is the canonical outbox table name. Callers must apply the
// migration produced by OutboxMigrationSQL (or use the helper registered in
// orm/migrate) before invoking TransactionWithOutbox.
const OutboxTableName = "velocity_outbox"

// OutboxKindJob marks a row as a queue job.
const OutboxKindJob = "job"

// OutboxKindEvent marks a row as an event dispatch.
const OutboxKindEvent = "event"

// outboxIdentifierRegex restricts the table/column names embedded in SQL to
// the same shape the migrate package validates DDL identifiers with.
var outboxIdentifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// validateOutboxIdentifier ensures a constant identifier is safe to embed in
// SQL. Called from package init so any future tampering fails loudly.
func validateOutboxIdentifier(name string) {
	if !outboxIdentifierRegex.MatchString(name) {
		panic("velocity/orm: invalid outbox identifier: " + name)
	}
}

func init() {
	validateOutboxIdentifier(OutboxTableName)
}

// Pending is the handle passed into the TransactionWithOutbox callback. It
// records jobs and events in the outbox table inside the same SQL transaction
// as the user's writes; the relay drains them after commit.
type Pending interface {
	// Enqueue records a queue job. The job is delivered to the queue driver
	// by the relay. Returns the row id assigned by the database.
	Enqueue(job any, opts ...PendingOption) (int64, error)
	// Dispatch records an event for the events dispatcher. Same delivery
	// guarantees as Enqueue. Returns the row id assigned by the database.
	Dispatch(event any, opts ...PendingOption) (int64, error)
}

// PendingOption configures a single outbox row.
type PendingOption func(*pendingMeta)

// WithIdempotencyKey overrides the auto-generated idempotency key. The key
// must be unique across the table; duplicate keys are rejected by the unique
// index and surface as the underlying SQL error.
func WithIdempotencyKey(key string) PendingOption {
	return func(m *pendingMeta) { m.IdempotencyKey = key }
}

// WithPartitionKey sets a partition key. The relay processes rows with the
// same key strictly in id order and never leases two rows from the same key
// concurrently, providing per-partition FIFO ordering.
func WithPartitionKey(key string) PendingOption {
	return func(m *pendingMeta) { m.PartitionKey = key }
}

// WithMaxAttempts sets the maximum delivery attempts before the row moves to
// the DLQ. Defaults to 5 when unset.
func WithMaxAttempts(n int) PendingOption {
	return func(m *pendingMeta) { m.MaxAttempts = n }
}

// WithAvailableAt schedules the first delivery attempt. Defaults to now.
func WithAvailableAt(at time.Time) PendingOption {
	return func(m *pendingMeta) { m.AvailableAt = at }
}

type pendingMeta struct {
	IdempotencyKey string
	PartitionKey   string
	MaxAttempts    int
	AvailableAt    time.Time
}

// pending implements Pending by inserting rows into the outbox table on the
// supplied transaction. It is local to a single TransactionWithOutbox call
// and is not safe for concurrent use across goroutines (the underlying
// *sql.Tx is single-threaded by design).
type pending struct {
	tx     *sql.Tx
	driver string
}

// Enqueue inserts a job row.
func (p *pending) Enqueue(job any, opts ...PendingOption) (int64, error) {
	return p.insert(OutboxKindJob, job, opts)
}

// Dispatch inserts an event row.
func (p *pending) Dispatch(event any, opts ...PendingOption) (int64, error) {
	return p.insert(OutboxKindEvent, event, opts)
}

func (p *pending) insert(kind string, payload any, opts []PendingOption) (int64, error) {
	if payload == nil {
		return 0, errors.New("velocity/orm: outbox payload cannot be nil")
	}
	meta := pendingMeta{
		MaxAttempts: 5,
		AvailableAt: time.Now().UTC(),
	}
	for _, opt := range opts {
		opt(&meta)
	}
	if meta.IdempotencyKey == "" {
		key, err := generateIdempotencyKey()
		if err != nil {
			return 0, err
		}
		meta.IdempotencyKey = key
	}
	if meta.MaxAttempts <= 0 {
		meta.MaxAttempts = 5
	}
	encoded, ptype, err := encodePayload(payload)
	if err != nil {
		return 0, err
	}

	var partition any
	if meta.PartitionKey != "" {
		partition = meta.PartitionKey
	} else {
		partition = nil
	}

	q := outboxInsertSQL(p.driver)
	res, err := p.tx.Exec(q,
		partition,
		kind,
		meta.IdempotencyKey,
		encoded,
		ptype,
		0,
		meta.MaxAttempts,
		meta.AvailableAt.UTC(),
		false,
		time.Now().UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("velocity/orm: outbox insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("velocity/orm: outbox last insert id: %w", err)
	}
	return id, nil
}

// generateIdempotencyKey returns a 128-bit cryptographically random hex key.
func generateIdempotencyKey() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("velocity/orm: idempotency key: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// outboxInsertSQL returns the driver-aware INSERT statement for outbox rows.
// Identifiers are constants validated at init; values bind as parameters.
func outboxInsertSQL(driver string) string {
	cols := "(partition_key, kind, idempotency_key, payload, payload_type, attempts, max_attempts, available_at, dlq, created_at)"
	switch driver {
	case "postgres":
		return "INSERT INTO " + quoteIdent(OutboxTableName, driver) + " " + cols +
			" VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id"
	default: // mysql, sqlite
		return "INSERT INTO " + quoteIdent(OutboxTableName, driver) + " " + cols +
			" VALUES (?,?,?,?,?,?,?,?,?,?)"
	}
}

// quoteIdent quotes an identifier for the active driver. Mirrors the helper
// in orm/migrate so this file does not import that package.
func quoteIdent(name, driver string) string {
	switch driver {
	case "postgres":
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	default:
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
}

// TransactionWithOutbox runs fn inside a database transaction. The Pending
// handle records outbox rows on the same *sql.Tx so they commit atomically
// with the user's writes, or roll back together. Any panic inside fn is
// converted to an error and the transaction is rolled back; the panic is
// not re-raised, so callers may handle the failure as a normal error per
// the framework's "no panics in library code" rule.
func (m *Manager) TransactionWithOutbox(ctx context.Context, fn func(tx *sql.Tx, outbox Pending) error) (retErr error) {
	m.mu.RLock()
	driver := m.defaultDriver
	logger := m.logger
	m.mu.RUnlock()

	if driver == nil {
		return errors.New("velocity/orm: no database connection")
	}

	tx, err := driver.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	driverName := driver.DriverName()
	p := &pending{tx: tx, driver: driverName}

	defer func() {
		if r := recover(); r != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				if logger != nil {
					logger.Error("velocity/orm: rollback failed after panic in outbox tx", "error", rbErr, "panic", fmt.Sprint(r))
				}
				m.dispatchEvent(ctx, &TxRecover{
					Cause:       "panic",
					PanicValue:  fmt.Sprint(r),
					RollbackErr: rbErr.Error(),
				})
			}
			// Honour the docstring: convert the panic into an error and
			// return it to the caller instead of re-raising. This keeps
			// outbox library code in line with CLAUDE.md rule #10
			// (never panic in library code).
			retErr = fmt.Errorf("velocity/orm: panic in outbox tx: %v", r)
		}
	}()

	if err := fn(tx, pendingFor(p, driverName)); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			if logger != nil {
				logger.Error("velocity/orm: rollback failed in outbox tx", "error", rbErr, "original_error", err)
			}
			m.dispatchEvent(ctx, &TxRecover{
				Cause:       "error",
				OriginalErr: err.Error(),
				RollbackErr: rbErr.Error(),
			})
		}
		return err
	}

	return tx.Commit()
}

// pendingFor returns a Pending implementation suited to the active driver.
// Postgres needs RETURNING, others use LastInsertId.
func pendingFor(p *pending, driver string) Pending {
	if driver == "postgres" {
		return &pendingPostgres{p}
	}
	return p
}

// pendingPostgres adapts pending for Postgres' RETURNING semantics.
type pendingPostgres struct{ inner *pending }

func (pp *pendingPostgres) Enqueue(job any, opts ...PendingOption) (int64, error) {
	return pp.insert(OutboxKindJob, job, opts)
}
func (pp *pendingPostgres) Dispatch(event any, opts ...PendingOption) (int64, error) {
	return pp.insert(OutboxKindEvent, event, opts)
}

func (pp *pendingPostgres) insert(kind string, payload any, opts []PendingOption) (int64, error) {
	if payload == nil {
		return 0, errors.New("velocity/orm: outbox payload cannot be nil")
	}
	meta := pendingMeta{MaxAttempts: 5, AvailableAt: time.Now().UTC()}
	for _, opt := range opts {
		opt(&meta)
	}
	if meta.IdempotencyKey == "" {
		key, err := generateIdempotencyKey()
		if err != nil {
			return 0, err
		}
		meta.IdempotencyKey = key
	}
	if meta.MaxAttempts <= 0 {
		meta.MaxAttempts = 5
	}
	encoded, ptype, err := encodePayload(payload)
	if err != nil {
		return 0, err
	}
	var partition any
	if meta.PartitionKey != "" {
		partition = meta.PartitionKey
	}
	q := outboxInsertSQL("postgres")
	var id int64
	if err := pp.inner.tx.QueryRow(q,
		partition, kind, meta.IdempotencyKey, encoded, ptype,
		0, meta.MaxAttempts, meta.AvailableAt.UTC(), false, time.Now().UTC(),
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("velocity/orm: outbox insert: %w", err)
	}
	return id, nil
}

// ----- Payload codec ---------------------------------------------------------

// payloadRegistry tracks which types have been registered so RegisterPayloadType
// is safely idempotent (gob.Register panics on duplicate name registrations
// with different types). The map itself is also useful for diagnostics.
var payloadRegistry = struct {
	sync.RWMutex
	m map[string]reflect.Type
}{m: map[string]reflect.Type{}}

// RegisterPayloadType registers a payload type with encoding/gob so the relay
// can decode rows produced by Pending.Enqueue / Pending.Dispatch. Callers must
// register every concrete type they enqueue before starting the relay; missing
// types surface as decode errors at delivery time. The call is idempotent for
// the same type and safe for concurrent use.
func RegisterPayloadType(v any) {
	if v == nil {
		return
	}
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	name := t.PkgPath() + "." + t.Name()
	payloadRegistry.Lock()
	if _, dup := payloadRegistry.m[name]; dup {
		payloadRegistry.Unlock()
		return
	}
	payloadRegistry.m[name] = t
	payloadRegistry.Unlock()
	gob.Register(v)
}

func payloadTypeName(v any) string {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.PkgPath() + "." + t.Name()
}

// encodePayload returns base64(gob(payload)) and the type name. base64 keeps
// the bytes safe inside a TEXT column on every supported driver.
func encodePayload(v any) (string, string, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(&v); err != nil {
		return "", "", fmt.Errorf("velocity/orm: outbox encode: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), payloadTypeName(v), nil
}

// decodePayload reverses encodePayload. gob carries the concrete type with
// the bytes, so the typeName parameter is informational (preserved for
// observability) and the returned value is whatever was originally encoded
// (value or pointer, depending on how the caller invoked Enqueue/Dispatch).
func decodePayload(encoded, _ string) (any, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("velocity/orm: outbox decode base64: %w", err)
	}
	var out any
	dec := gob.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("velocity/orm: outbox decode gob: %w", err)
	}
	return out, nil
}

// ----- Migration helper ------------------------------------------------------

// OutboxMigrationSQL returns the CREATE TABLE + index DDL for the outbox
// table for the given driver. Callers may run these statements directly or
// wrap them in a migrate.Migration.
func OutboxMigrationSQL(driver string) []string {
	t := quoteIdent(OutboxTableName, driver)
	idxLease := quoteIdent("idx_"+OutboxTableName+"_lease", driver)
	idxIdem := quoteIdent("idx_"+OutboxTableName+"_idem", driver)
	idxPartition := quoteIdent("idx_"+OutboxTableName+"_partition", driver)

	switch driver {
	case "postgres":
		return []string{
			"CREATE TABLE IF NOT EXISTS " + t + ` (
				id BIGSERIAL PRIMARY KEY,
				partition_key TEXT NULL,
				kind TEXT NOT NULL,
				idempotency_key TEXT NOT NULL,
				payload TEXT NOT NULL,
				payload_type TEXT NOT NULL,
				attempts INTEGER NOT NULL DEFAULT 0,
				max_attempts INTEGER NOT NULL DEFAULT 5,
				available_at TIMESTAMP NOT NULL DEFAULT NOW(),
				leased_until TIMESTAMP NULL,
				leased_by TEXT NULL,
				dlq BOOLEAN NOT NULL DEFAULT false,
				last_error TEXT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT NOW()
			)`,
			"CREATE UNIQUE INDEX IF NOT EXISTS " + idxIdem + " ON " + t + " (idempotency_key)",
			"CREATE INDEX IF NOT EXISTS " + idxLease + " ON " + t + " (dlq, leased_until, available_at)",
			"CREATE INDEX IF NOT EXISTS " + idxPartition + " ON " + t + " (partition_key, id)",
		}
	case "mysql":
		return []string{
			"CREATE TABLE IF NOT EXISTS " + t + ` (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				partition_key VARCHAR(255) NULL,
				kind VARCHAR(16) NOT NULL,
				idempotency_key VARCHAR(128) NOT NULL,
				payload LONGTEXT NOT NULL,
				payload_type VARCHAR(255) NOT NULL,
				attempts INT NOT NULL DEFAULT 0,
				max_attempts INT NOT NULL DEFAULT 5,
				available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				leased_until TIMESTAMP NULL,
				leased_by VARCHAR(128) NULL,
				dlq TINYINT(1) NOT NULL DEFAULT 0,
				last_error TEXT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				UNIQUE KEY ` + quoteIdent("uk_"+OutboxTableName+"_idem", driver) + ` (idempotency_key)
			) ENGINE=InnoDB`,
			"CREATE INDEX " + idxLease + " ON " + t + " (dlq, leased_until, available_at)",
			"CREATE INDEX " + idxPartition + " ON " + t + " (partition_key, id)",
		}
	default: // sqlite
		return []string{
			"CREATE TABLE IF NOT EXISTS " + t + ` (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				partition_key TEXT NULL,
				kind TEXT NOT NULL,
				idempotency_key TEXT NOT NULL,
				payload TEXT NOT NULL,
				payload_type TEXT NOT NULL,
				attempts INTEGER NOT NULL DEFAULT 0,
				max_attempts INTEGER NOT NULL DEFAULT 5,
				available_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				leased_until DATETIME NULL,
				leased_by TEXT NULL,
				dlq INTEGER NOT NULL DEFAULT 0,
				last_error TEXT NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`,
			"CREATE UNIQUE INDEX IF NOT EXISTS " + idxIdem + " ON " + t + " (idempotency_key)",
			"CREATE INDEX IF NOT EXISTS " + idxLease + " ON " + t + " (dlq, leased_until, available_at)",
			"CREATE INDEX IF NOT EXISTS " + idxPartition + " ON " + t + " (partition_key, id)",
		}
	}
}

// EnsureOutboxTable runs the migration DDL against the manager's default
// connection. Idempotent (uses IF NOT EXISTS). Useful for tests and for
// app-level wiring that does not use the migrate package.
func (m *Manager) EnsureOutboxTable(ctx context.Context) error {
	driver := m.DefaultDriver()
	if driver == nil {
		return errors.New("velocity/orm: no database connection")
	}
	for _, stmt := range OutboxMigrationSQL(driver.DriverName()) {
		if _, err := driver.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("velocity/orm: ensure outbox table: %w", err)
		}
	}
	return nil
}

// ----- Relay -----------------------------------------------------------------

// RelayConfig configures a Relay.
type RelayConfig struct {
	// PollInterval is the time between scan ticks. Defaults to 1s.
	PollInterval time.Duration
	// LeaseDuration is how long a claimed row remains owned by this relay
	// before another relay may steal it. Defaults to 30s.
	LeaseDuration time.Duration
	// BatchSize is the maximum rows claimed per tick. Defaults to 32.
	BatchSize int
	// WorkerCount bounds in-flight dispatches. Defaults to 4.
	WorkerCount int
	// MaxAttempts is the default per-row max_attempts when the row's value
	// is zero. Defaults to 5.
	MaxAttempts int
	// BackoffBase / BackoffMax are passed to the exponential backoff used
	// to schedule retries (matches queue.ExponentialBackoff). Defaults:
	// 1s base, 5m max.
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// RelayID identifies this relay process in the leased_by column. When
	// empty a random hex string is used.
	RelayID string
	// ShutdownGrace is the grace window granted to in-flight workers after
	// Stop is signalled before their database writes (recordSuccess /
	// recordFailure) and dispatch callback contexts are cancelled. This
	// guarantees Stop cannot block indefinitely under DB pressure even when
	// Stop's own ctx is context.Background. Defaults to 5s.
	ShutdownGrace time.Duration
}

// RelayCallbacks is the dispatch surface the relay calls into when it wins a
// row. Both fields are optional; rows whose kind matches an unset callback
// are treated as transient failures and scheduled for retry.
type RelayCallbacks struct {
	OnJob   func(ctx context.Context, payload any, payloadType, idempotencyKey string) error
	OnEvent func(ctx context.Context, payload any, payloadType, idempotencyKey string) error
}

// RelayLogger receives relay diagnostics. The interface matches the orm
// eventLogger so an *log.Logger satisfies it.
type RelayLogger interface {
	Warn(msg string, kvs ...any)
	Error(msg string, kvs ...any)
}

// Relay drains the outbox table and dispatches rows via the configured
// callbacks. Multiple relays may run against the same table concurrently;
// row claim uses lease + atomic CAS so only one relay handles a row at a
// time.
type Relay struct {
	mgr       *Manager
	cfg       RelayConfig
	callbacks RelayCallbacks
	logger    RelayLogger

	mu       sync.Mutex
	running  bool
	cancelFn context.CancelFunc
	// shutdownCtx is the relay's lifetime context. It mirrors loopCtx for
	// cancellation but is the ctx threaded into worker DB writes
	// (recordSuccess / recordFailure) and into dispatch callbacks. Stop
	// keeps it alive for cfg.ShutdownGrace before cancelling so in-flight
	// workers can finish their writebacks instead of hanging forever.
	shutdownCtx      context.Context
	shutdownCancelFn context.CancelFunc
	doneCh           chan struct{}
	inFlight         sync.WaitGroup
	activePart       sync.Map // partition_key (string) -> struct{} for ordering claim
}

// NewRelay constructs a Relay. The relay does not start until Start is called.
func NewRelay(mgr *Manager, callbacks RelayCallbacks, cfg RelayConfig) *Relay {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = 30 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = 4
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 5 * time.Minute
	}
	if cfg.RelayID == "" {
		key, err := generateIdempotencyKey()
		if err != nil {
			// generateIdempotencyKey only fails if crypto/rand fails;
			// fall back to a constant so the relay remains usable.
			key = "relay-unknown"
		}
		cfg.RelayID = "relay-" + key
	}
	if cfg.ShutdownGrace <= 0 {
		cfg.ShutdownGrace = 5 * time.Second
	}
	return &Relay{
		mgr:       mgr,
		cfg:       cfg,
		callbacks: callbacks,
	}
}

// SetLogger installs an optional logger.
func (r *Relay) SetLogger(l RelayLogger) {
	r.mu.Lock()
	r.logger = l
	r.mu.Unlock()
}

// ID returns the relay's RelayID, useful for tests and observability.
func (r *Relay) ID() string { return r.cfg.RelayID }

// Start launches the relay loop. Returns an error if the relay is already
// running or the database is not configured.
func (r *Relay) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errors.New("velocity/orm: relay already running")
	}
	if r.mgr == nil || r.mgr.DefaultDriver() == nil {
		r.mu.Unlock()
		return errors.New("velocity/orm: relay needs a connected manager")
	}
	r.running = true
	loopCtx, cancel := context.WithCancel(ctx)
	r.cancelFn = cancel
	// shutdownCtx is detached from the loop ctx so we can cancel it on a
	// grace timer in Stop, independent of when the polling loop exits.
	// Workers' DB writebacks and dispatch callbacks derive from this ctx,
	// guaranteeing they cannot hang past Stop + ShutdownGrace.
	r.shutdownCtx, r.shutdownCancelFn = context.WithCancel(context.Background())
	r.doneCh = make(chan struct{})
	r.mu.Unlock()

	// Not async.Go: must close(r.doneCh) on panic so Stop's wait on doneCh
	// never blocks shutdown waiting on a goroutine that already died, and
	// the panic is logged with the relay's own structured logger.
	go func() { //safe-goroutine: close(r.doneCh) on panic + relay-scoped logger, see comment above
		defer close(r.doneCh)
		defer func() {
			if rec := recover(); rec != nil {
				if r.logger != nil {
					r.logger.Error("velocity/orm: relay loop panic", "panic", fmt.Sprint(rec))
				}
			}
		}()
		r.loop(loopCtx)
	}()
	return nil
}

// Stop signals the relay to stop and waits for in-flight dispatches to finish
// (bounded by ctx). After cfg.ShutdownGrace elapses (or ctx is cancelled),
// the relay-scoped shutdownCtx is also cancelled, which interrupts any
// dispatch callbacks and recordSuccess / recordFailure DB writes still in
// flight so Stop cannot hang indefinitely.
func (r *Relay) Stop(ctx context.Context) error {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return nil
	}
	cancel := r.cancelFn
	shutdownCancel := r.shutdownCancelFn
	done := r.doneCh
	grace := r.cfg.ShutdownGrace
	r.running = false
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// Always cancel the shutdown ctx eventually so workers cannot pin the
	// relay open beyond grace, even if the caller hands us a Background ctx.
	graceTimer := time.NewTimer(grace)
	defer graceTimer.Stop()
	cancelOnce := sync.Once{}
	cancelShutdown := func() {
		cancelOnce.Do(func() {
			if shutdownCancel != nil {
				shutdownCancel()
			}
		})
	}
	defer cancelShutdown()

	// Wait for the loop goroutine to exit.
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			cancelShutdown()
			return ctx.Err()
		case <-graceTimer.C:
			cancelShutdown()
		}
	}
	// Wait for any in-flight dispatch goroutines.
	// Not async.Go: trivial WaitGroup waiter, no user code runs here.
	wait := make(chan struct{})
	go func() { //safe-goroutine: trivial WaitGroup waiter, no user code runs here
		r.inFlight.Wait()
		close(wait)
	}()
	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		cancelShutdown()
		// Drain remaining workers with cancellation now signalled.
		<-wait
		return ctx.Err()
	case <-graceTimer.C:
		cancelShutdown()
		<-wait
		return nil
	}
}

// loop is the polling driver.
func (r *Relay) loop(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	sem := make(chan struct{}, r.cfg.WorkerCount)

	// Tick once immediately so callers don't have to wait a full interval
	// for the first scan.
	r.tick(ctx, sem)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx, sem)
		}
	}
}

// tick claims a batch and dispatches each row with bounded concurrency.
func (r *Relay) tick(ctx context.Context, sem chan struct{}) {
	rows, err := r.claimBatch(ctx)
	if err != nil {
		if r.logger != nil {
			r.logger.Warn("velocity/orm: relay claim batch failed", "error", err)
		}
		return
	}
	for i, row := range rows {
		if ctx.Err() != nil {
			// Early shutdown: claimBatch reserved partition keys for every
			// row in the batch, but we no longer spawn a goroutine for the
			// remaining rows so their deferred Delete never fires. Release
			// the partition reservations now or future ticks would skip
			// them forever (silent stall on shutdown + restart).
			r.releasePartitions(rows[i:])
			return
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			r.releasePartitions(rows[i:])
			return
		}
		r.inFlight.Add(1)
		row := row
		// Not async.Go: must release the semaphore, decrement inFlight,
		// and record per-row failure / clear partition reservation on
		// panic, none of which generic recovery can do.
		go func() { //safe-goroutine: per-row resource release on panic, see comment above
			defer r.inFlight.Done()
			defer func() { <-sem }()
			defer func() {
				if rec := recover(); rec != nil {
					if r.logger != nil {
						r.logger.Error("velocity/orm: relay worker panic", "panic", fmt.Sprint(rec), "row_id", row.ID)
					}
					_ = r.recordFailure(r.writebackCtx(), row, fmt.Errorf("panic: %v", rec))
				}
				if row.PartitionKey != "" {
					r.activePart.Delete(row.PartitionKey)
				}
			}()
			// Hand the worker the relay-scoped shutdown ctx. This survives
			// the polling-loop ctx cancellation so we don't yank the rug
			// out from under workers mid-callback, but Stop will cancel
			// it after cfg.ShutdownGrace to bound the wait.
			r.dispatch(r.shutdownCtx, row)
		}()
	}
}

// releasePartitions clears the activePart reservations for rows that were
// claimed but for which no goroutine will run (early shutdown path). Without
// this, the partition keys would remain pinned in the map until the relay
// is GC'd, blocking subsequent ticks from claiming the same partition.
func (r *Relay) releasePartitions(rows []outboxRow) {
	for _, row := range rows {
		if row.PartitionKey != "" {
			r.activePart.Delete(row.PartitionKey)
		}
	}
}

// outboxRow is the in-memory shape of a claimed row.
type outboxRow struct {
	ID             int64
	PartitionKey   string
	Kind           string
	IdempotencyKey string
	Payload        string
	PayloadType    string
	Attempts       int
	MaxAttempts    int
}

// claimBatch atomically leases up to BatchSize ready rows for this relay.
// Strategy:
//   - SELECT a candidate id list (LIMIT BatchSize, ordered by partition then id),
//     skipping rows whose partition is already in flight on this relay.
//   - For each candidate, run a conditional UPDATE that sets leased_until/by
//     only when the row is still unleased (or the lease expired). The update
//     returns RowsAffected=1 when this relay won; 0 when another relay raced
//     us. Wins are added to the result.
//
// This portable strategy works on SQLite (no SKIP LOCKED), MySQL, and Postgres
// without taking long-held locks. The conditional UPDATE is the atomic step.
func (r *Relay) claimBatch(ctx context.Context) ([]outboxRow, error) {
	driver := r.mgr.DefaultDriver()
	if driver == nil {
		return nil, errors.New("velocity/orm: no database connection")
	}
	driverName := driver.DriverName()
	now := time.Now().UTC()

	// 1. Read candidate ids.
	candidates, partitions, err := r.scanCandidates(ctx, driverName, now)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	leaseUntil := now.Add(r.cfg.LeaseDuration)
	out := make([]outboxRow, 0, len(candidates))

	for i, id := range candidates {
		part := partitions[i]
		// Per-partition serialisation: never lease two rows from the
		// same partition concurrently from this relay.
		if part != "" {
			if _, busy := r.activePart.LoadOrStore(part, struct{}{}); busy {
				continue
			}
		}
		row, ok, err := r.tryClaim(ctx, driverName, id, leaseUntil, now)
		if err != nil {
			if part != "" {
				r.activePart.Delete(part)
			}
			if r.logger != nil {
				r.logger.Warn("velocity/orm: relay claim row failed", "error", err, "row_id", id)
			}
			continue
		}
		if !ok {
			if part != "" {
				r.activePart.Delete(part)
			}
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

// scanCandidates returns ready row ids ordered by (partition_key NULLS FIRST, id).
func (r *Relay) scanCandidates(ctx context.Context, driver string, now time.Time) ([]int64, []string, error) {
	t := quoteIdent(OutboxTableName, driver)

	var q string
	switch driver {
	case "postgres":
		q = "SELECT id, COALESCE(partition_key, '') FROM " + t +
			" WHERE dlq = false AND available_at <= $1 AND (leased_until IS NULL OR leased_until <= $2)" +
			" ORDER BY partition_key NULLS FIRST, id LIMIT $3"
	default:
		// SQLite/MySQL: NULL sorts first by default in MySQL/SQLite for ASC.
		q = "SELECT id, COALESCE(partition_key, '') FROM " + t +
			" WHERE dlq = 0 AND available_at <= ? AND (leased_until IS NULL OR leased_until <= ?)" +
			" ORDER BY partition_key, id LIMIT ?"
	}

	rows, err := r.mgr.DefaultDriver().QueryContext(ctx, q, now, now, r.cfg.BatchSize)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []int64
	var parts []string
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		parts = append(parts, p)
	}
	return ids, parts, rows.Err()
}

// tryClaim runs the conditional UPDATE that owns a single row. Returns
// (row, true, nil) on win.
func (r *Relay) tryClaim(ctx context.Context, driver string, id int64, leaseUntil, now time.Time) (outboxRow, bool, error) {
	t := quoteIdent(OutboxTableName, driver)

	var upd string
	switch driver {
	case "postgres":
		upd = "UPDATE " + t + " SET leased_until=$1, leased_by=$2 WHERE id=$3 AND dlq=false AND (leased_until IS NULL OR leased_until <= $4)"
	default:
		upd = "UPDATE " + t + " SET leased_until=?, leased_by=? WHERE id=? AND dlq=0 AND (leased_until IS NULL OR leased_until <= ?)"
	}

	res, err := r.mgr.DefaultDriver().ExecContext(ctx, upd, leaseUntil, r.cfg.RelayID, id, now)
	if err != nil {
		return outboxRow{}, false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return outboxRow{}, false, err
	}
	if affected == 0 {
		return outboxRow{}, false, nil
	}

	// Fetch the row contents.
	var sel string
	switch driver {
	case "postgres":
		sel = "SELECT id, COALESCE(partition_key,''), kind, idempotency_key, payload, payload_type, attempts, max_attempts FROM " + t + " WHERE id=$1"
	default:
		sel = "SELECT id, COALESCE(partition_key,''), kind, idempotency_key, payload, payload_type, attempts, max_attempts FROM " + t + " WHERE id=?"
	}
	row := r.mgr.DefaultDriver().QueryRowContext(ctx, sel, id)
	var or outboxRow
	if err := row.Scan(&or.ID, &or.PartitionKey, &or.Kind, &or.IdempotencyKey, &or.Payload, &or.PayloadType, &or.Attempts, &or.MaxAttempts); err != nil {
		return outboxRow{}, false, err
	}
	return or, true, nil
}

// dispatch decodes the payload and calls the matching callback. ctx is the
// relay-scoped shutdown ctx: it stays live until Stop's grace window
// elapses, so the user's callback work AND the writeback (recordSuccess /
// recordFailure) cancel together when the relay is winding down.
func (r *Relay) dispatch(ctx context.Context, row outboxRow) {
	payload, err := decodePayload(row.Payload, row.PayloadType)
	if err != nil {
		_ = r.recordFailure(ctx, row, err)
		return
	}

	var cb func(context.Context, any, string, string) error
	switch row.Kind {
	case OutboxKindJob:
		cb = r.callbacks.OnJob
	case OutboxKindEvent:
		cb = r.callbacks.OnEvent
	}
	if cb == nil {
		_ = r.recordFailure(ctx, row, fmt.Errorf("no callback for kind %q", row.Kind))
		return
	}

	if err := cb(ctx, payload, row.PayloadType, row.IdempotencyKey); err != nil {
		_ = r.recordFailure(ctx, row, err)
		return
	}
	if err := r.recordSuccess(ctx, row); err != nil && r.logger != nil {
		r.logger.Warn("velocity/orm: relay record success failed", "error", err, "row_id", row.ID)
	}
}

// writebackCtx returns the relay-scoped ctx workers should use for DB
// writebacks (recordSuccess / recordFailure) and callback dispatch. It
// follows the relay's shutdown lifecycle: live until Stop's grace window
// elapses, then cancelled. Falls back to a fresh background ctx if the
// relay was never started (defensive: tests call recordSuccess directly).
func (r *Relay) writebackCtx() context.Context {
	r.mu.Lock()
	c := r.shutdownCtx
	r.mu.Unlock()
	if c == nil {
		return context.Background()
	}
	return c
}

// recordSuccess deletes the row. Uses the relay-scoped shutdown ctx so an
// in-flight DELETE can be cancelled when Stop's grace window elapses,
// preventing relay shutdown from hanging on DB pressure.
func (r *Relay) recordSuccess(ctx context.Context, row outboxRow) error {
	driver := r.mgr.DefaultDriver()
	if driver == nil {
		return errors.New("velocity/orm: no database connection")
	}
	driverName := driver.DriverName()
	t := quoteIdent(OutboxTableName, driverName)
	var q string
	if driverName == "postgres" {
		q = "DELETE FROM " + t + " WHERE id=$1"
	} else {
		q = "DELETE FROM " + t + " WHERE id=?"
	}
	_, err := driver.ExecContext(ctx, q, row.ID)
	return err
}

// recordFailure increments attempts, schedules a retry, and (if attempts >=
// max) flips the row into the DLQ. Uses the relay-scoped shutdown ctx so
// an in-flight UPDATE is cancelled when Stop's grace window elapses.
func (r *Relay) recordFailure(ctx context.Context, row outboxRow, cause error) error {
	driver := r.mgr.DefaultDriver()
	if driver == nil {
		return errors.New("velocity/orm: no database connection")
	}
	driverName := driver.DriverName()
	t := quoteIdent(OutboxTableName, driverName)

	attempts := row.Attempts + 1
	maxAttempts := row.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = r.cfg.MaxAttempts
	}
	dead := attempts >= maxAttempts
	delay := backoffFor(attempts, r.cfg.BackoffBase, r.cfg.BackoffMax)
	nextAt := time.Now().UTC().Add(delay)
	errMsg := truncate(cause.Error(), 1024)

	var q string
	if driverName == "postgres" {
		q = "UPDATE " + t + " SET attempts=$1, available_at=$2, leased_until=NULL, leased_by=NULL, dlq=$3, last_error=$4 WHERE id=$5"
	} else {
		q = "UPDATE " + t + " SET attempts=?, available_at=?, leased_until=NULL, leased_by=NULL, dlq=?, last_error=? WHERE id=?"
	}
	dlqVal := boolForDriver(dead, driverName)
	_, err := driver.ExecContext(ctx, q, attempts, nextAt, dlqVal, errMsg, row.ID)
	return err
}

// Replay flips a DLQ row back into the active set, resetting attempts to
// zero. Returns ErrOutboxRowNotFound when the id does not exist.
func (r *Relay) Replay(ctx context.Context, id int64) error {
	driver := r.mgr.DefaultDriver()
	if driver == nil {
		return errors.New("velocity/orm: no database connection")
	}
	driverName := driver.DriverName()
	t := quoteIdent(OutboxTableName, driverName)
	var q string
	if driverName == "postgres" {
		q = "UPDATE " + t + " SET dlq=false, attempts=0, available_at=$1, leased_until=NULL, leased_by=NULL WHERE id=$2"
	} else {
		q = "UPDATE " + t + " SET dlq=0, attempts=0, available_at=?, leased_until=NULL, leased_by=NULL WHERE id=?"
	}
	res, err := driver.ExecContext(ctx, q, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrOutboxRowNotFound
	}
	return nil
}

// ErrOutboxRowNotFound is returned by Replay when the row id does not exist.
var ErrOutboxRowNotFound = errors.New("velocity/orm: outbox row not found")

// backoffFor computes an exponential delay capped at max.
func backoffFor(attempt int, base, max time.Duration) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d > max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}

// boolForDriver renders Go bools to the literal accepted by Exec on each
// driver. Postgres takes booleans natively; MySQL/SQLite store integers.
func boolForDriver(b bool, driver string) any {
	if driver == "postgres" {
		return b
	}
	if b {
		return 1
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ----- Test/inspection helpers ----------------------------------------------

// OutboxRowSnapshot is a read-only view of a row, returned by inspection
// helpers used in tests and admin tooling.
type OutboxRowSnapshot struct {
	ID             int64
	PartitionKey   string
	Kind           string
	IdempotencyKey string
	PayloadType    string
	Attempts       int
	MaxAttempts    int
	DLQ            bool
	LastError      string
	LeasedBy       string
}

// ListOutboxRows returns up to limit rows for inspection. Order is by id ASC.
func (m *Manager) ListOutboxRows(ctx context.Context, limit int) ([]OutboxRowSnapshot, error) {
	driver := m.DefaultDriver()
	if driver == nil {
		return nil, errors.New("velocity/orm: no database connection")
	}
	if limit <= 0 {
		limit = 100
	}
	driverName := driver.DriverName()
	t := quoteIdent(OutboxTableName, driverName)
	var q string
	if driverName == "postgres" {
		q = "SELECT id, COALESCE(partition_key,''), kind, idempotency_key, payload_type, attempts, max_attempts, dlq, COALESCE(last_error,''), COALESCE(leased_by,'') FROM " + t + " ORDER BY id ASC LIMIT $1"
	} else {
		q = "SELECT id, COALESCE(partition_key,''), kind, idempotency_key, payload_type, attempts, max_attempts, dlq, COALESCE(last_error,''), COALESCE(leased_by,'') FROM " + t + " ORDER BY id ASC LIMIT ?"
	}
	rows, err := driver.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxRowSnapshot
	for rows.Next() {
		var s OutboxRowSnapshot
		var dlqInt sql.NullBool
		var dlqRawInt sql.NullInt64
		// Postgres scans bool natively; MySQL/SQLite store integer.
		if driverName == "postgres" {
			if err := rows.Scan(&s.ID, &s.PartitionKey, &s.Kind, &s.IdempotencyKey, &s.PayloadType, &s.Attempts, &s.MaxAttempts, &dlqInt, &s.LastError, &s.LeasedBy); err != nil {
				return nil, err
			}
			s.DLQ = dlqInt.Valid && dlqInt.Bool
		} else {
			if err := rows.Scan(&s.ID, &s.PartitionKey, &s.Kind, &s.IdempotencyKey, &s.PayloadType, &s.Attempts, &s.MaxAttempts, &dlqRawInt, &s.LastError, &s.LeasedBy); err != nil {
				return nil, err
			}
			s.DLQ = dlqRawInt.Valid && dlqRawInt.Int64 != 0
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CountOutboxRows returns total rows in the outbox table. Useful for tests.
func (m *Manager) CountOutboxRows(ctx context.Context) (int, error) {
	driver := m.DefaultDriver()
	if driver == nil {
		return 0, errors.New("velocity/orm: no database connection")
	}
	t := quoteIdent(OutboxTableName, driver.DriverName())
	var n int
	if err := driver.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
