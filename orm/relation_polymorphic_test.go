package orm

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/orm/drivers"
)

// --- Test models for polymorphic ---

type MComment struct {
	Model[MComment]
	Body string `orm:"column:body"`
}

func (MComment) TableName() string { return "morph_comments" }

type MArticle struct {
	Model[MArticle]
	Title string `orm:"column:title"`
}

func (MArticle) TableName() string { return "morph_articles" }

type MPhoto struct {
	Model[MPhoto]
	URL string `orm:"column:url"`
}

func (MPhoto) TableName() string { return "morph_photos" }

type MorphAudit struct {
	Model[MorphAudit]
	Action   string `orm:"column:action"`
	Resource Morph  `orm:"polymorphic:resource_type,resource_id"`
}

func (MorphAudit) TableName() string { return "morph_audit_logs" }

// --- Setup ---

func setupPolymorphicTables(t testing.TB) *Manager {
	t.Helper()
	manager := newTestManager(t)
	db := manager.DB()
	for _, ddl := range []string{
		`CREATE TABLE morph_comments (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT, created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE morph_articles (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT, created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE morph_photos (id INTEGER PRIMARY KEY AUTOINCREMENT, url TEXT, created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE morph_audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT,
			resource_type TEXT,
			resource_id INTEGER,
			created_at DATETIME, updated_at DATETIME
		)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	return manager
}

func seedPolymorphicData(t testing.TB, manager *Manager) {
	t.Helper()
	db := manager.DB()
	seeds := []string{
		`INSERT INTO morph_comments (id, body, created_at, updated_at) VALUES (1, 'first', '2024-01-01', '2024-01-01'), (2, 'second', '2024-01-01', '2024-01-01')`,
		`INSERT INTO morph_articles (id, title, created_at, updated_at) VALUES (1, 'Article A', '2024-01-01', '2024-01-01'), (2, 'Article B', '2024-01-01', '2024-01-01')`,
		`INSERT INTO morph_photos (id, url, created_at, updated_at) VALUES (1, 'https://x/y.jpg', '2024-01-01', '2024-01-01')`,
		`INSERT INTO morph_audit_logs (id, action, resource_type, resource_id, created_at, updated_at) VALUES
			(1, 'create', 'comment', 1, '2024-01-01', '2024-01-01'),
			(2, 'update', 'comment', 2, '2024-01-01', '2024-01-01'),
			(3, 'create', 'article', 1, '2024-01-01', '2024-01-01'),
			(4, 'create', 'article', 2, '2024-01-01', '2024-01-01'),
			(5, 'create', 'photo', 1, '2024-01-01', '2024-01-01')`,
	}
	for _, s := range seeds {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func withPolymorphicDB(t testing.TB) func() {
	t.Helper()
	manager := setupPolymorphicTables(t)
	seedPolymorphicData(t, manager)
	SetDefault(manager)
	ResetMorphRegistry()
	RegisterMorph("comment", reflect.TypeOf(MComment{}))
	RegisterMorph("article", reflect.TypeOf(MArticle{}))
	RegisterMorph("photo", reflect.TypeOf(MPhoto{}))
	return func() {
		ResetMorphRegistry()
		ResetDefault()
		manager.Shutdown(context.Background())
	}
}

// ============================================================
// Unit tests: registry + tag parsing
// ============================================================

func TestRegisterMorph_BasicRoundtrip(t *testing.T) {
	ResetMorphRegistry()
	defer ResetMorphRegistry()

	RegisterMorph("comment", reflect.TypeOf(MComment{}))
	if got, ok := LookupMorph("comment"); !ok || got != reflect.TypeOf(MComment{}) {
		t.Errorf("LookupMorph: got=%v ok=%v", got, ok)
	}
	if _, ok := LookupMorph("missing"); ok {
		t.Error("missing entry should return ok=false")
	}
}

func TestRegisterMorph_PointerNormalized(t *testing.T) {
	ResetMorphRegistry()
	defer ResetMorphRegistry()
	RegisterMorph("comment", reflect.TypeOf(&MComment{}))
	if got, _ := LookupMorph("comment"); got != reflect.TypeOf(MComment{}) {
		t.Errorf("expected non-pointer registration, got %v", got)
	}
}

func TestRegisterMorph_ZeroInputs(t *testing.T) {
	ResetMorphRegistry()
	defer ResetMorphRegistry()
	RegisterMorph("", reflect.TypeOf(MComment{}))
	RegisterMorph("x", nil)
	if _, ok := LookupMorph(""); ok {
		t.Error("empty name should not register")
	}
	if _, ok := LookupMorph("x"); ok {
		t.Error("nil type should not register")
	}
}

func TestParsePolymorphicTag(t *testing.T) {
	tests := []struct {
		name    string
		val     string
		typeCol string
		idCol   string
		wantErr bool
	}{
		{name: "Basic", val: "resource_type,resource_id", typeCol: "resource_type", idCol: "resource_id"},
		{name: "Whitespace", val: " rt , rid ", typeCol: "rt", idCol: "rid"},
		{name: "TooFew", val: "rt", wantErr: true},
		{name: "TooMany", val: "a,b,c", wantErr: true},
		{name: "Empty", val: ",rid", wantErr: true},
		{name: "Unsafe", val: "rt; DROP--,rid", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc, ic, err := parsePolymorphicTag(tt.val)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if tc != tt.typeCol || ic != tt.idCol {
				t.Errorf("got (%q,%q) want (%q,%q)", tc, ic, tt.typeCol, tt.idCol)
			}
		})
	}
}

func TestExtractPolymorphicValue(t *testing.T) {
	tests := []struct{ tag, want string }{
		{tag: "polymorphic:rt,rid", want: "rt,rid"},
		{tag: "column:foo;polymorphic:rt,rid", want: "rt,rid"},
		{tag: "manyToMany:t,a,b", want: ""},
	}
	for _, tt := range tests {
		if got := extractPolymorphicValue(tt.tag); got != tt.want {
			t.Errorf("extractPolymorphicValue(%q) = %q, want %q", tt.tag, got, tt.want)
		}
	}
}

func TestResolvePolymorphicMeta(t *testing.T) {
	meta, err := resolvePolymorphicMeta(reflect.TypeOf(MorphAudit{}), "Resource")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if meta.typeColumn != "resource_type" || meta.idColumn != "resource_id" {
		t.Errorf("meta wrong: %+v", meta)
	}
}

func TestResolvePolymorphicMeta_NotFound(t *testing.T) {
	_, err := resolvePolymorphicMeta(reflect.TypeOf(MorphAudit{}), "Nope")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ============================================================
// Integration: Resolve
// ============================================================

func TestPolymorphic_Resolve(t *testing.T) {
	cleanup := withPolymorphicDB(t)
	defer cleanup()

	tests := []struct {
		typeName string
		id       int
		check    func(t *testing.T, res any)
	}{
		{typeName: "comment", id: 1, check: func(t *testing.T, res any) {
			c, ok := res.(*MComment)
			if !ok {
				t.Fatalf("expected *MComment, got %T", res)
			}
			if c.Body != "first" {
				t.Errorf("body = %q", c.Body)
			}
		}},
		{typeName: "article", id: 2, check: func(t *testing.T, res any) {
			a, ok := res.(*MArticle)
			if !ok {
				t.Fatalf("expected *MArticle, got %T", res)
			}
			if a.Title != "Article B" {
				t.Errorf("title = %q", a.Title)
			}
		}},
		{typeName: "photo", id: 1, check: func(t *testing.T, res any) {
			p, ok := res.(*MPhoto)
			if !ok {
				t.Fatalf("expected *MPhoto, got %T", res)
			}
			if !strings.HasPrefix(p.URL, "https://") {
				t.Errorf("url = %q", p.URL)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.typeName, func(t *testing.T) {
			m := &Morph{TypeName: tt.typeName, ID: tt.id}
			res, err := m.Resolve(context.Background())
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			tt.check(t, res)
			if m.Resolved == nil {
				t.Error("Resolved should be set on the morph")
			}
		})
	}
}

func TestPolymorphic_Resolve_ZeroInputs(t *testing.T) {
	cleanup := withPolymorphicDB(t)
	defer cleanup()
	if _, err := (&Morph{}).Resolve(context.Background()); err == nil {
		t.Error("expected error for empty TypeName")
	}
	if _, err := (&Morph{TypeName: "comment"}).Resolve(context.Background()); err == nil {
		t.Error("expected error for zero ID")
	}
}

func TestPolymorphic_UnknownTypeName_ClearError(t *testing.T) {
	cleanup := withPolymorphicDB(t)
	defer cleanup()

	m := &Morph{TypeName: "unicorn", ID: 1}
	_, err := m.Resolve(context.Background())
	if err == nil {
		t.Fatal("expected error for unknown type name")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unicorn") {
		t.Errorf("error should mention type name, got: %v", err)
	}
	if !strings.Contains(msg, "RegisterMorph") {
		t.Errorf("error should hint at RegisterMorph, got: %v", err)
	}
}

func TestPolymorphic_Resolve_NotFound(t *testing.T) {
	cleanup := withPolymorphicDB(t)
	defer cleanup()
	m := &Morph{TypeName: "comment", ID: 9999}
	_, err := m.Resolve(context.Background())
	if err != ErrRecordNotFound {
		t.Errorf("expected ErrRecordNotFound, got %v", err)
	}
}

// ============================================================
// Integration: eager-load batches by type
// ============================================================

func TestPolymorphic_EagerLoad_BatchesByType(t *testing.T) {
	cleanup := withPolymorphicDB(t)
	defer cleanup()

	// 5 rows, 3 distinct types -> exactly 3 IN queries against related
	// tables. We count queries by issuing them ourselves via a sql.DB
	// before/after counter that taps into Manager.Stats's open-conn metric;
	// since that's flaky we instead instrument by replacing the driver with
	// a counting wrapper.
	mgr := Default()
	original := mgr.DefaultDriver()
	wrapped := newCountingDriver(original, []string{"morph_comments", "morph_articles", "morph_photos"})
	mgr.mu.Lock()
	mgr.defaultDriver = wrapped
	mgr.mu.Unlock()
	defer func() {
		mgr.mu.Lock()
		mgr.defaultDriver = original
		mgr.mu.Unlock()
	}()

	logs, err := MorphAudit{}.With("Resource").Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(logs) != 5 {
		t.Fatalf("logs=%d, want 5", len(logs))
	}
	if got := wrapped.matched.Load(); got != 3 {
		t.Errorf("expected exactly 3 batched queries (one per type), got %d", got)
	}
	// Spot-check resolved values.
	for _, l := range logs {
		if l.Resource.Resolved == nil {
			t.Errorf("log %d resource not resolved", l.ID)
			continue
		}
		switch l.Resource.TypeName {
		case "comment":
			if _, ok := l.Resource.Resolved.(*MComment); !ok {
				t.Errorf("log %d: resolved is %T", l.ID, l.Resource.Resolved)
			}
		case "article":
			if _, ok := l.Resource.Resolved.(*MArticle); !ok {
				t.Errorf("log %d: resolved is %T", l.ID, l.Resource.Resolved)
			}
		case "photo":
			if _, ok := l.Resource.Resolved.(*MPhoto); !ok {
				t.Errorf("log %d: resolved is %T", l.ID, l.Resource.Resolved)
			}
		}
	}
}

func TestPolymorphic_EagerLoad_UnknownType(t *testing.T) {
	manager := setupPolymorphicTables(t)
	defer manager.Shutdown(context.Background())
	db := manager.DB()
	_, _ = db.Exec(`INSERT INTO morph_audit_logs (id, action, resource_type, resource_id, created_at, updated_at)
		VALUES (1, 'create', 'unknown_type', 1, '2024-01-01', '2024-01-01')`)
	SetDefault(manager)
	defer ResetDefault()
	ResetMorphRegistry()
	defer ResetMorphRegistry()
	// Strict mode keeps the legacy hard-fail behavior the original test
	// asserted; the default is now non-strict (logs + skips).
	SetMorphStrict(true)
	t.Cleanup(func() { SetMorphStrict(false) })
	// Only register one type to ensure 'unknown_type' is missing.
	RegisterMorph("comment", reflect.TypeOf(MComment{}))

	_, err := MorphAudit{}.With("Resource").Get(context.Background())
	if err == nil {
		t.Fatal("expected error for unknown morph type")
	}
	if !strings.Contains(err.Error(), "unknown_type") {
		t.Errorf("error should mention type name: %v", err)
	}
}

// TestPolymorphic_UnknownTypeName_NonStrict_LogsAndSkips verifies the new
// default behavior: rows referencing an unregistered morph type are skipped
// (Resolved stays nil) instead of failing the whole eager-load batch.
func TestPolymorphic_UnknownTypeName_NonStrict_LogsAndSkips(t *testing.T) {
	manager := setupPolymorphicTables(t)
	defer manager.Shutdown(context.Background())
	db := manager.DB()
	// Mix one known and two unknown rows so we can verify that known rows
	// resolve while unknown rows are silently skipped.
	if _, err := db.Exec(`INSERT INTO morph_comments (id, body, created_at, updated_at) VALUES (1, 'ok', '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed comments: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO morph_audit_logs (id, action, resource_type, resource_id, created_at, updated_at) VALUES
		(1, 'create', 'comment', 1, '2024-01-01', '2024-01-01'),
		(2, 'create', 'unknown_type', 99, '2024-01-01', '2024-01-01'),
		(3, 'update', 'unknown_type', 100, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	SetDefault(manager)
	defer ResetDefault()
	ResetMorphRegistry()
	defer ResetMorphRegistry()
	RegisterMorph("comment", reflect.TypeOf(MComment{}))

	// Inject an in-memory writer for the morph warning so the assertion is
	// race-free (the previous version reassigned os.Stderr globally, which
	// races across packages under -race) and so test output stays clean.
	var captured strings.Builder
	prev := SetMorphWarnWriter(&captured)
	t.Cleanup(func() { SetMorphWarnWriter(prev) })

	// Default (non-strict) mode.
	if MorphStrict() {
		t.Fatal("default MorphStrict() should be false")
	}

	logs, getErr := MorphAudit{}.With("Resource").Get(context.Background())

	if getErr != nil {
		t.Fatalf("Get returned error in non-strict mode: %v", getErr)
	}
	if len(logs) != 3 {
		t.Fatalf("logs=%d, want 3", len(logs))
	}

	var resolved, unresolved int
	for _, l := range logs {
		switch l.Resource.TypeName {
		case "comment":
			if l.Resource.Resolved == nil {
				t.Errorf("known type 'comment' should be resolved")
			} else {
				resolved++
			}
		case "unknown_type":
			if l.Resource.Resolved != nil {
				t.Errorf("unknown type should leave Resolved=nil")
			} else {
				unresolved++
			}
		}
	}
	if resolved != 1 {
		t.Errorf("expected 1 resolved row, got %d", resolved)
	}
	if unresolved != 2 {
		t.Errorf("expected 2 unresolved rows, got %d", unresolved)
	}
	if !strings.Contains(captured.String(), "unknown_type") {
		t.Errorf("expected morph warn writer to mention skipped type, got: %q", captured.String())
	}
}

// TestPolymorphic_UnknownTypeName_Strict_Errors verifies that toggling
// strict mode restores the original hard-fail behavior so callers that
// want fail-fast schema drift detection can opt back in.
func TestPolymorphic_UnknownTypeName_Strict_Errors(t *testing.T) {
	manager := setupPolymorphicTables(t)
	defer manager.Shutdown(context.Background())
	db := manager.DB()
	if _, err := db.Exec(`INSERT INTO morph_audit_logs (id, action, resource_type, resource_id, created_at, updated_at)
		VALUES (1, 'create', 'unknown_type', 1, '2024-01-01', '2024-01-01')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	SetDefault(manager)
	defer ResetDefault()
	ResetMorphRegistry()
	defer ResetMorphRegistry()
	RegisterMorph("comment", reflect.TypeOf(MComment{}))

	SetMorphStrict(true)
	t.Cleanup(func() { SetMorphStrict(false) })

	if _, err := (MorphAudit{}.With("Resource")).Get(context.Background()); err == nil {
		t.Fatal("expected error in strict mode")
	} else if !strings.Contains(err.Error(), "unknown_type") {
		t.Errorf("error should mention the unknown type name: %v", err)
	}
}

// ============================================================
// Concurrency
// ============================================================

// BadMorph defines a polymorphic field of the wrong Go type.
type BadMorph struct {
	Model[BadMorph]
	Resource string `orm:"polymorphic:rt,rid"`
}

func (BadMorph) TableName() string { return "bad_morphs" }

func TestResolvePolymorphicMeta_WrongType(t *testing.T) {
	_, err := resolvePolymorphicMeta(reflect.TypeOf(BadMorph{}), "Resource")
	if err == nil {
		t.Fatal("expected error for non-Morph field")
	}
}

func TestMorph_IsZero(t *testing.T) {
	if !(Morph{}).IsZero() {
		t.Error("zero Morph should be zero")
	}
	if (Morph{TypeName: "x"}).IsZero() {
		t.Error("non-empty TypeName means non-zero")
	}
	if (Morph{ID: 1}).IsZero() {
		t.Error("non-zero ID means non-zero")
	}
}

func TestMorph_Resolve_NoManager(t *testing.T) {
	ResetDefault()
	ResetMorphRegistry()
	RegisterMorph("comment", reflect.TypeOf(MComment{}))
	defer ResetMorphRegistry()
	m := &Morph{TypeName: "comment", ID: 1}
	if _, err := m.Resolve(context.Background()); err == nil {
		t.Fatal("expected error when no default manager")
	}
}

func TestMorph_Resolve_NilReceiver(t *testing.T) {
	var m *Morph
	if _, err := m.Resolve(context.Background()); err == nil {
		t.Fatal("expected error for nil receiver")
	}
}

func TestPolymorphic_EagerLoad_NoMatchingRows(t *testing.T) {
	cleanup := withPolymorphicDB(t)
	defer cleanup()
	logs, err := MorphAudit{}.With("Resource").Where("id < 0").Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 logs, got %d", len(logs))
	}
}

func TestPolymorphic_EagerLoad_NoTypeName(t *testing.T) {
	manager := setupPolymorphicTables(t)
	defer manager.Shutdown(context.Background())
	db := manager.DB()
	_, _ = db.Exec(`INSERT INTO morph_audit_logs (id, action, resource_type, resource_id, created_at, updated_at)
		VALUES (1, 'create', '', 0, '2024-01-01', '2024-01-01')`)
	SetDefault(manager)
	defer ResetDefault()
	ResetMorphRegistry()
	defer ResetMorphRegistry()
	logs, err := MorphAudit{}.With("Resource").Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Resource.Resolved != nil {
		t.Error("zero morph should not resolve to anything")
	}
}

func TestPolymorphic_StructToMapWritesColumns(t *testing.T) {
	rec := &MorphAudit{Action: "test"}
	rec.Resource = Morph{TypeName: "comment", ID: 7}
	m := structToMap(rec)
	if m["resource_type"] != "comment" {
		t.Errorf("resource_type = %v, want 'comment'", m["resource_type"])
	}
	if m["resource_id"] == nil {
		t.Errorf("resource_id missing from structToMap output: %v", m)
	}
}

func TestPolymorphic_Concurrent(t *testing.T) {
	ResetMorphRegistry()
	defer ResetMorphRegistry()

	const goroutines = 64
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("type_%d", i%4)
			RegisterMorph(name, reflect.TypeOf(MComment{}))
			_, _ = LookupMorph(name)
		}(i)
	}
	wg.Wait()
	for i := 0; i < 4; i++ {
		if _, ok := LookupMorph(fmt.Sprintf("type_%d", i)); !ok {
			t.Errorf("type_%d missing after concurrent register", i)
		}
	}
}

// --- helpers: query-counting driver wrapper ---

// countingDriver wraps a real driver, forwarding every method to the inner
// implementation while counting QueryContext calls whose SQL references one
// of the supplied table names. Used to assert batched eager-load behaviour.
type countingDriver struct {
	inner   drivers.Driver
	tables  []string
	matched atomic.Int32
}

func newCountingDriver(inner drivers.Driver, tables []string) *countingDriver {
	return &countingDriver{inner: inner, tables: tables}
}

func (d *countingDriver) Connect(cfg drivers.ConnectionConfig) error {
	return d.inner.Connect(cfg)
}
func (d *countingDriver) Close() error { return d.inner.Close() }
func (d *countingDriver) Ping() error  { return d.inner.Ping() }
func (d *countingDriver) DB() *sql.DB  { return d.inner.DB() }

func (d *countingDriver) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	for _, tbl := range d.tables {
		if strings.Contains(q, tbl) {
			d.matched.Add(1)
			break
		}
	}
	return d.inner.QueryContext(ctx, q, args...)
}
func (d *countingDriver) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return d.inner.QueryRowContext(ctx, q, args...)
}
func (d *countingDriver) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return d.inner.ExecContext(ctx, q, args...)
}
func (d *countingDriver) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return d.inner.BeginTx(ctx, opts)
}
func (d *countingDriver) CreateTable(name string, def func(*drivers.Table)) error {
	return d.inner.CreateTable(name, def)
}
func (d *countingDriver) DropTable(name string) error   { return d.inner.DropTable(name) }
func (d *countingDriver) HasTable(name string) bool     { return d.inner.HasTable(name) }
func (d *countingDriver) HasColumn(t, c string) bool    { return d.inner.HasColumn(t, c) }
func (d *countingDriver) Grammar() drivers.QueryGrammar { return d.inner.Grammar() }
func (d *countingDriver) DriverName() string            { return d.inner.DriverName() }
func (d *countingDriver) OperatorRegistry() map[string]drivers.OperatorSpec {
	return d.inner.OperatorRegistry()
}
