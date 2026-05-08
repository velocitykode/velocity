package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// --- Test models for many-to-many ---

type Team struct {
	Model[Team]
	Name    string `orm:"column:name"`
	Members []User `orm:"manyToMany:team_members,team_id,user_id"`
}

func (Team) TableName() string { return "m2m_teams" }

type TeamPtrMembers struct {
	Model[TeamPtrMembers]
	Name    string  `orm:"column:name"`
	Members []*User `orm:"manyToMany:team_members,team_id,user_id"`
}

func (TeamPtrMembers) TableName() string { return "m2m_teams" }

// --- Setup helpers ---

func setupM2MTables(t testing.TB) *Manager {
	t.Helper()
	manager := newTestManager(t)
	db := manager.DB()

	for _, ddl := range []string{
		`CREATE TABLE m2m_teams (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			email TEXT,
			age INTEGER,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE team_members (
			team_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			role TEXT,
			joined_at DATETIME,
			PRIMARY KEY (team_id, user_id)
		)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	return manager
}

func seedM2MData(t testing.TB, manager *Manager) {
	t.Helper()
	db := manager.DB()
	seeds := []string{
		`INSERT INTO m2m_teams (id, name, created_at, updated_at) VALUES
			(1, 'Engineering', '2024-01-01', '2024-01-01'),
			(2, 'Product', '2024-01-01', '2024-01-01'),
			(3, 'Empty', '2024-01-01', '2024-01-01')`,
		`INSERT INTO users (id, name, email, age, created_at, updated_at) VALUES
			(1, 'Alice', 'a@x', 30, '2024-01-01', '2024-01-01'),
			(2, 'Bob', 'b@x', 31, '2024-01-01', '2024-01-01'),
			(3, 'Carol', 'c@x', 32, '2024-01-01', '2024-01-01'),
			(4, 'Dan', 'd@x', 33, '2024-01-01', '2024-01-01')`,
		`INSERT INTO team_members (team_id, user_id, role, joined_at) VALUES
			(1, 1, 'lead', '2024-02-01'),
			(1, 2, 'member', '2024-02-02'),
			(1, 3, 'member', '2024-02-03'),
			(2, 2, 'lead', '2024-03-01'),
			(2, 4, 'member', '2024-03-02')`,
	}
	for _, s := range seeds {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func withM2MDB(t testing.TB) func() {
	t.Helper()
	manager := setupM2MTables(t)
	seedM2MData(t, manager)
	SetDefault(manager)
	// Ensure pivot column probe cache doesn't leak across DB instances.
	clearPivotColumnCache()
	return func() {
		clearPivotColumnCache()
		ResetDefault()
		manager.Shutdown(context.Background())
	}
}

// ============================================================
// Unit tests: tag parsing
// ============================================================

func TestParseManyToManyTag(t *testing.T) {
	tests := []struct {
		name    string
		val     string
		pivot   string
		fk1     string
		fk2     string
		wantErr bool
	}{
		{name: "Basic", val: "team_members,team_id,user_id", pivot: "team_members", fk1: "team_id", fk2: "user_id"},
		{name: "Whitespace", val: " team_members , team_id , user_id ", pivot: "team_members", fk1: "team_id", fk2: "user_id"},
		{name: "TooFew", val: "team_members,team_id", wantErr: true},
		{name: "TooMany", val: "a,b,c,d", wantErr: true},
		{name: "EmptyPart", val: "team_members,,user_id", wantErr: true},
		{name: "UnsafePivot", val: "team; DROP--,a,b", wantErr: true},
		{name: "UnsafeFK", val: "team_members,team_id,user_id;DROP--", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pivot, fk1, fk2, err := parseManyToManyTag(tt.val)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if pivot != tt.pivot || fk1 != tt.fk1 || fk2 != tt.fk2 {
				t.Errorf("got (%q,%q,%q) want (%q,%q,%q)", pivot, fk1, fk2, tt.pivot, tt.fk1, tt.fk2)
			}
		})
	}
}

func TestExtractManyToManyValue(t *testing.T) {
	tests := []struct{ tag, want string }{
		{tag: "manyToMany:tm,tid,uid", want: "tm,tid,uid"},
		{tag: "column:foo;manyToMany:tm,tid,uid", want: "tm,tid,uid"},
		{tag: "relation:hasMany,fk,id", want: ""},
		{tag: "", want: ""},
	}
	for _, tt := range tests {
		if got := extractManyToManyValue(tt.tag); got != tt.want {
			t.Errorf("extractManyToManyValue(%q) = %q, want %q", tt.tag, got, tt.want)
		}
	}
}

func TestResolveManyToManyMeta(t *testing.T) {
	meta, err := resolveManyToManyMeta(reflect.TypeOf(Team{}), "Members")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if meta.pivotTable != "team_members" || meta.localFK != "team_id" || meta.relatedFK != "user_id" {
		t.Errorf("meta wrong: %+v", meta)
	}
	if meta.relatedTable != "users" {
		t.Errorf("relatedTable=%q", meta.relatedTable)
	}
	if meta.isPtr {
		t.Error("expected isPtr=false for []User")
	}
}

func TestResolveManyToManyMeta_PtrSlice(t *testing.T) {
	meta, err := resolveManyToManyMeta(reflect.TypeOf(TeamPtrMembers{}), "Members")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !meta.isPtr {
		t.Error("expected isPtr=true for []*User")
	}
}

func TestResolveManyToManyMeta_NotFound(t *testing.T) {
	_, err := resolveManyToManyMeta(reflect.TypeOf(Team{}), "Nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ============================================================
// Integration: read
// ============================================================

func TestManyToMany_Read(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()

	teams, err := Team{}.With("Members").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(teams) != 3 {
		t.Fatalf("teams=%d, want 3", len(teams))
	}
	expect := map[string]int{"Engineering": 3, "Product": 2, "Empty": 0}
	for _, team := range teams {
		want, ok := expect[team.Name]
		if !ok {
			t.Errorf("unexpected team %q", team.Name)
			continue
		}
		if len(team.Members) != want {
			t.Errorf("team %q: got %d members, want %d", team.Name, len(team.Members), want)
		}
		for _, m := range team.Members {
			if m.ID == 0 || m.Name == "" {
				t.Errorf("team %q: member not loaded fully: %+v", team.Name, m)
			}
		}
	}
}

func TestManyToMany_Read_PtrSlice(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()

	teams, err := TeamPtrMembers{}.With("Members").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	for _, team := range teams {
		for i, m := range team.Members {
			if m == nil {
				t.Errorf("team %q members[%d] is nil", team.Name, i)
			}
		}
	}
}

// ============================================================
// Integration: pivot column flow-through
// ============================================================

func TestManyToMany_PivotColumns(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()

	team, err := Team{}.Find(context.Background(), uint(1))
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	results, err := LoadManyToManyWithPivot[Team, User](team, "Members")
	if err != nil {
		t.Fatalf("LoadManyToManyWithPivot: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 pivot results, got %d", len(results))
	}
	// Verify pivot extras include "role" and "joined_at" but NOT the FKs.
	for _, r := range results {
		if _, ok := r.Pivot["team_id"]; ok {
			t.Errorf("pivot map should not include FK team_id")
		}
		if _, ok := r.Pivot["user_id"]; ok {
			t.Errorf("pivot map should not include FK user_id")
		}
		if _, ok := r.Pivot["role"]; !ok {
			t.Errorf("pivot map should include 'role', got %v", r.Pivot)
		}
		if _, ok := r.Pivot["joined_at"]; !ok {
			t.Errorf("pivot map should include 'joined_at', got %v", r.Pivot)
		}
	}
	// Verify Alice has role=lead.
	for _, r := range results {
		if r.Related.Name == "Alice" {
			if r.Pivot["role"] != "lead" {
				t.Errorf("Alice role = %v, want 'lead'", r.Pivot["role"])
			}
		}
	}
}

func TestManyToMany_PivotColumns_NoPivotData(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()

	team, err := Team{}.Find(context.Background(), uint(3)) // empty team
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	results, err := LoadManyToManyWithPivot[Team, User](team, "Members")
	if err != nil {
		t.Fatalf("LoadManyToManyWithPivot: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestManyToMany_PivotColumns_TypeMismatch(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()
	team, _ := Team{}.Find(context.Background(), uint(1))
	_, err := LoadManyToManyWithPivot[Team, Team](team, "Members")
	if err == nil {
		t.Fatal("expected type mismatch error")
	}
}

// ============================================================
// Integration: attach/detach/sync
// ============================================================

func TestManyToMany_AttachDetachSync(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()

	// Use empty team (id=3) for clean state.
	team, err := Team{}.Find(context.Background(), uint(3))
	if err != nil {
		t.Fatalf("find: %v", err)
	}

	acc, err := M2M(team, "Members")
	if err != nil {
		t.Fatalf("M2M: %v", err)
	}
	ctx := context.Background()

	// Attach 1 and 2.
	if err := acc.Attach(ctx, 1, 2); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	got := pivotMembersFor(t, acc, ctx)
	if !sameIDSet(got, []any{int64(1), int64(2)}) {
		t.Errorf("after Attach(1,2): got %v", got)
	}

	// Re-attaching is a no-op (no duplicates).
	if err := acc.Attach(ctx, 1, 2); err != nil {
		t.Fatalf("Attach idempotent: %v", err)
	}
	got = pivotMembersFor(t, acc, ctx)
	if len(got) != 2 {
		t.Errorf("after duplicate Attach: %d rows, want 2", len(got))
	}

	// Attach adds new while keeping existing.
	if err := acc.Attach(ctx, 3); err != nil {
		t.Fatalf("Attach 3: %v", err)
	}
	got = pivotMembersFor(t, acc, ctx)
	if !sameIDSet(got, []any{int64(1), int64(2), int64(3)}) {
		t.Errorf("after Attach(3): got %v", got)
	}

	// Detach a subset.
	if err := acc.Detach(ctx, 2); err != nil {
		t.Fatalf("Detach 2: %v", err)
	}
	got = pivotMembersFor(t, acc, ctx)
	if !sameIDSet(got, []any{int64(1), int64(3)}) {
		t.Errorf("after Detach(2): got %v", got)
	}

	// Sync to a new exact set: insert 4, delete 1, keep 3.
	if err := acc.Sync(ctx, 3, 4); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	got = pivotMembersFor(t, acc, ctx)
	if !sameIDSet(got, []any{int64(3), int64(4)}) {
		t.Errorf("after Sync(3,4): got %v", got)
	}

	// Detach all (no args).
	if err := acc.Detach(ctx); err != nil {
		t.Fatalf("Detach all: %v", err)
	}
	got = pivotMembersFor(t, acc, ctx)
	if len(got) != 0 {
		t.Errorf("after Detach-all: %d rows, want 0", len(got))
	}
}

func TestManyToMany_Sync_Empty(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()
	team, _ := Team{}.Find(context.Background(), uint(1)) // has 3 members
	acc, err := M2M(team, "Members")
	if err != nil {
		t.Fatalf("M2M: %v", err)
	}
	ctx := context.Background()
	if err := acc.Sync(ctx); err != nil {
		t.Fatalf("Sync empty: %v", err)
	}
	got := pivotMembersFor(t, acc, ctx)
	if len(got) != 0 {
		t.Errorf("after Sync(): %d rows, want 0", len(got))
	}
}

func TestManyToMany_M2M_NoID(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()
	team := &Team{Name: "Unsaved"} // id is zero
	if _, err := M2M(team, "Members"); err == nil {
		t.Fatal("expected error for unsaved parent")
	}
}

// ============================================================
// Concurrent reads + writes
// ============================================================

func TestManyToMany_Concurrent(t *testing.T) {
	manager := setupM2MTables(t)
	manager.DB().SetMaxOpenConns(1) // share one in-memory connection
	seedM2MData(t, manager)
	SetDefault(manager)
	defer ResetDefault()
	defer manager.Shutdown(context.Background())
	clearPivotColumnCache()
	defer clearPivotColumnCache()

	const goroutines = 8
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*2)

	// Reader goroutines.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			teams, err := Team{}.With("Members").Get(context.Background())
			if err != nil {
				errCh <- fmt.Errorf("read: %w", err)
				return
			}
			if len(teams) != 3 {
				errCh <- fmt.Errorf("read: teams=%d", len(teams))
			}
		}()
	}

	// Writer goroutines that operate on the empty team (id=3) to avoid
	// fighting the readers over the seeded fixture data.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(round int) {
			defer wg.Done()
			team, _ := Team{}.Find(context.Background(), uint(3))
			acc, err := M2M(team, "Members")
			if err != nil {
				errCh <- fmt.Errorf("M2M: %w", err)
				return
			}
			ctx := context.Background()
			// Sync repeatedly to a single value; race-detector exercise.
			if err := acc.Sync(ctx, round+1); err != nil {
				errCh <- fmt.Errorf("sync: %w", err)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Errorf("concurrent: %v", e)
	}
}

// --- helpers ---

func pivotMembersFor(t *testing.T, acc *M2MAccessor, ctx context.Context) []any {
	t.Helper()
	out, err := acc.existingRelatedIDs(ctx, acc.driver)
	if err != nil {
		t.Fatalf("existingRelatedIDs: %v", err)
	}
	return out
}

func sameIDSet(got []any, want []any) bool {
	if len(got) != len(want) {
		return false
	}
	g := make(map[any]bool, len(got))
	for _, v := range got {
		g[normalizeKey(v)] = true
	}
	for _, w := range want {
		if !g[normalizeKey(w)] {
			return false
		}
	}
	return true
}

// Confirm that drivers.Driver satisfies the queryRunner interface used by
// the accessor helpers. If this fails to compile the helper is broken.
func TestM2MQueryRunnerCompiles(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.Shutdown(context.Background())
	var _ queryRunner = mgr.DefaultDriver()
	if !strings.Contains("ok", "ok") {
		t.Skip()
	}
}

// ============================================================
// Error path tests
// ============================================================

func TestM2M_NilParent(t *testing.T) {
	if _, err := M2M[Team](nil, "Members"); err == nil {
		t.Fatal("expected error for nil parent")
	}
}

func TestM2M_NoDefaultManager(t *testing.T) {
	ResetDefault()
	team := &Team{}
	team.ID = 1
	if _, err := M2M(team, "Members"); err == nil {
		t.Fatal("expected error when no default manager")
	}
}

func TestLoadManyToManyWithPivot_NilParent(t *testing.T) {
	if _, err := LoadManyToManyWithPivot[Team, User](nil, "Members"); err == nil {
		t.Fatal("expected error for nil parent")
	}
}

func TestLoadManyToManyWithPivot_NoManager(t *testing.T) {
	ResetDefault()
	team := &Team{}
	team.ID = 1
	if _, err := LoadManyToManyWithPivot[Team, User](team, "Members"); err == nil {
		t.Fatal("expected error when no default manager")
	}
}

func TestLoadManyToManyWithPivot_NoID(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()
	team := &Team{} // zero ID
	out, err := LoadManyToManyWithPivot[Team, User](team, "Members")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty result for zero-id parent, got %d", len(out))
	}
}

// BadM2M defines a manyToMany field that is not a slice; resolveManyToManyMeta
// must reject this rather than panic.
type BadM2M struct {
	Model[BadM2M]
	Bogus string `orm:"manyToMany:t,a,b"`
}

func (BadM2M) TableName() string { return "bad_m2m" }

func TestResolveManyToManyMeta_NotSlice(t *testing.T) {
	_, err := resolveManyToManyMeta(reflect.TypeOf(BadM2M{}), "Bogus")
	if err == nil {
		t.Fatal("expected error for non-slice m2m field")
	}
}
