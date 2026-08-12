package ormauth_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/stores/ormauth"
	"github.com/velocitykode/velocity/orm"
)

// Admin is a model that does NOT implement auth.Authenticatable and whose
// columns share none of the default names, so it exercises the reflective
// column mapping end to end. Its remember token is a sql.NullString.
type Admin struct {
	orm.IDInt[Admin]

	Username    string         `orm:"column:username"`
	PassHash    string         `orm:"column:pass_hash"`
	RecallToken sql.NullString `orm:"column:recall_token"`
}

// AssignableFields declares a mass-assignment policy so the remember-token
// write is not rejected by the ORM's deny-by-default rule.
func (Admin) AssignableFields() []string { return []string{"username", "pass_hash", "recall_token"} }

// Operator covers the *string carrier for the remember-token column.
type Operator struct {
	orm.IDInt[Operator]

	Email    string  `orm:"column:email"`
	Password string  `orm:"column:password"`
	Recall   *string `orm:"column:remember_token"`
}

// ProtectedFields declares an empty denylist, which is the other way out of
// deny-by-default.
func (Operator) ProtectedFields() []string { return nil }

// NoPolicy declares neither AssignableFields nor ProtectedFields, so every map-based write
// against it is rejected by the ORM.
type NoPolicy struct {
	orm.IDInt[NoPolicy]

	Email         string `orm:"column:email"`
	Password      string `orm:"column:password"`
	RememberToken string `orm:"column:remember_token"`
}

// NoPrimaryKey has no primary key, so it cannot back GetAuthIdentifier.
type NoPrimaryKey struct {
	Email         string `orm:"column:email"`
	Password      string `orm:"column:password"`
	RememberToken string `orm:"column:remember_token"`
}

// AssignableFields satisfies the mass-assignment policy so the primary-key
// failure is the one the test observes.
func (NoPrimaryKey) AssignableFields() []string {
	return []string{"email", "password", "remember_token"}
}

// BadTypes carries a remember-token column that cannot hold a string.
type BadTypes struct {
	orm.IDInt[BadTypes]

	Email         string `orm:"column:email"`
	Password      string `orm:"column:password"`
	RememberToken int    `orm:"column:remember_token"`
}

// AssignableFields satisfies the mass-assignment policy.
func (BadTypes) AssignableFields() []string { return []string{"email", "password", "remember_token"} }

const adminsSchema = `CREATE TABLE admins (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT NOT NULL UNIQUE,
	pass_hash TEXT NOT NULL,
	recall_token TEXT
)`

const operatorsSchema = `CREATE TABLE operators (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	email TEXT NOT NULL UNIQUE,
	password TEXT NOT NULL,
	remember_token TEXT
)`

// newAdminStore is the construction an application with an Admin model
// would write: every column renamed away from the defaults.
func newAdminStore(t *testing.T) *ormauth.Store[Admin] {
	t.Helper()

	p := ormauth.New[Admin](
		ormauth.WithIdentifierColumn("username"),
		ormauth.WithPasswordColumn("pass_hash"),
		ormauth.WithRememberTokenColumn("recall_token"),
		ormauth.WithHasher(auth.NewBcryptHasher(bcrypt.MinCost)),
	)
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return p
}

// seedAdmin creates the admins table and inserts one row.
func seedAdmin(t *testing.T, m *orm.Manager, username, password string) {
	t.Helper()

	if _, err := m.DB().Exec(adminsSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if _, err := m.DB().Exec(
		`INSERT INTO admins (username, pass_hash) VALUES (?, ?)`, username, string(hash),
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestNew_RejectsUnmappableModels covers every construction failure, each of
// which surfaces at Validate rather than as a runtime surprise.
func TestNew_RejectsUnmappableModels(t *testing.T) {
	tests := []struct {
		name      string
		userStore interface{ Validate() error }
		wants     string
	}{
		{
			name:      "missing identifier column",
			userStore: ormauth.New[Admin](),
			wants:     `has no column "email"`,
		},
		{
			name:      "missing remember token column",
			userStore: ormauth.New[Admin](ormauth.WithIdentifierColumn("username")),
			wants:     `has no column "remember_token"`,
		},
		{
			name: "missing password column",
			userStore: ormauth.New[Admin](
				ormauth.WithIdentifierColumn("username"),
				ormauth.WithRememberTokenColumn("recall_token"),
			),
			wants: `has no column "password"`,
		},
		{
			name:      "no mass-assignment policy",
			userStore: ormauth.New[NoPolicy](),
			wants:     "declares no mass-assignment policy",
		},
		{
			name:      "no primary key",
			userStore: ormauth.New[NoPrimaryKey](),
			wants:     "declares no primary key",
		},
		{
			name:      "unusable column type",
			userStore: ormauth.New[BadTypes](),
			wants:     "must be string, *string, or sql.NullString",
		},
		{
			name:      "not a struct",
			userStore: ormauth.New[int](),
			wants:     "is not a struct model",
		},
		{
			// orm.MetaFor derefs pointers, so a pointer T maps cleanly
			// and would then indirect through a nil pointer in wrap.
			name:      "pointer to a struct",
			userStore: ormauth.New[*Admin](),
			wants:     "is not a struct model",
		},
		{
			name:      "interface",
			userStore: ormauth.New[auth.Authenticatable](),
			wants:     "is not a struct model",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.userStore.Validate()
			if err == nil {
				t.Fatal("expected a construction error")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wants)
			}
		})
	}
}

// TestStore_UnvalidatedStoreNeverQueries proves a user store that
// failed to map refuses I/O instead of issuing a wrong query.
func TestStore_UnvalidatedStoreNeverQueries(t *testing.T) {
	newManager(t)
	p := ormauth.New[NoPolicy]()
	ctx := context.Background()

	if _, err := p.FindByIDCtx(ctx, 1); err == nil {
		t.Error("FindByIDCtx on an unmapped user store succeeded")
	}
	if _, err := p.FindByCredentialsCtx(ctx, map[string]interface{}{"email": testEmail}); err == nil {
		t.Error("FindByCredentialsCtx on an unmapped user store succeeded")
	}
	if err := p.UpdateRememberTokenCtx(ctx, &auth.AuthUser{ID: uint(1)}, "t"); err == nil {
		t.Error("UpdateRememberTokenCtx on an unmapped user store succeeded")
	}
	if _, err := p.CompareAndSwapRememberToken(ctx, &auth.AuthUser{ID: uint(1)}, "a", "b"); err == nil {
		t.Error("CompareAndSwapRememberToken on an unmapped user store succeeded")
	}
}

// TestStore_MappedModel_NullString exercises the non-native path: a
// model that does not implement auth.Authenticatable, with every column
// renamed and a sql.NullString remember token.
func TestStore_MappedModel_NullString(t *testing.T) {
	m := newManager(t)
	seedAdmin(t, m, "root", testPassword)

	p := newAdminStore(t)
	ctx := context.Background()

	user, err := p.FindByCredentialsCtx(ctx, map[string]interface{}{"username": "root"})
	if err != nil {
		t.Fatalf("FindByCredentialsCtx: %v", err)
	}
	if got := user.GetAuthIdentifier(); got != uint(1) {
		t.Errorf("identifier = %#v, want uint(1)", got)
	}
	if !p.ValidateCredentials(user, map[string]interface{}{"password": testPassword}) {
		t.Error("seeded admin password rejected")
	}
	if got := user.GetRememberToken(); got != "" {
		t.Errorf("NULL remember token = %q, want empty", got)
	}

	// The adapter exposes the underlying record.
	holder, ok := user.(interface{ Model() *Admin })
	if !ok {
		t.Fatal("mapped user does not expose Model()")
	}
	if holder.Model().Username != "root" {
		t.Errorf("Model().Username = %q, want root", holder.Model().Username)
	}

	if err := p.UpdateRememberTokenCtx(ctx, user, "recalled"); err != nil {
		t.Fatalf("UpdateRememberTokenCtx: %v", err)
	}
	if got := user.GetRememberToken(); got != "recalled" {
		t.Errorf("in-memory token = %q, want recalled", got)
	}
	if got := holder.Model().RecallToken; !got.Valid || got.String != "recalled" {
		t.Errorf("underlying model token = %#v, want a valid \"recalled\"", got)
	}

	var stored string
	if err := m.DB().QueryRow(`SELECT recall_token FROM admins WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "recalled" {
		t.Errorf("persisted token = %q, want recalled", stored)
	}

	swapped, err := p.CompareAndSwapRememberToken(ctx, user, "recalled", "rotated")
	if err != nil {
		t.Fatalf("CompareAndSwapRememberToken: %v", err)
	}
	if !swapped {
		t.Error("swap on the mapped model reported false")
	}
}

// TestStore_MappedModel_PointerString covers the *string carrier.
func TestStore_MappedModel_PointerString(t *testing.T) {
	m := newManager(t)
	if _, err := m.DB().Exec(operatorsSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := m.DB().Exec(
		`INSERT INTO operators (email, password, remember_token) VALUES (?, ?, ?)`,
		testEmail, "stored-hash", "existing",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	p := ormauth.New[Operator]()
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	user, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{"email": testEmail})
	if err != nil {
		t.Fatalf("FindByCredentialsCtx: %v", err)
	}
	if got := user.GetRememberToken(); got != "existing" {
		t.Errorf("remember token = %q, want existing", got)
	}

	user.SetRememberToken("replaced")
	holder := user.(interface{ Model() *Operator })
	if holder.Model().Recall == nil || *holder.Model().Recall != "replaced" {
		t.Errorf("underlying model token = %v, want replaced", holder.Model().Recall)
	}
}

// TestStore_SwapsTable is the headline for model swappability: pointing
// the user store at a different model type moves every statement onto that
// model's table, with that model's column names. No configuration string is
// involved - the type parameter is the switch.
func TestStore_SwapsTable(t *testing.T) {
	m := newManager(t)
	seedAdmin(t, m, "root", testPassword)

	// A users table seeded too. If the user store still carried the
	// framework's hardcoded table, these statements would land there.
	seedUser(t, m, testEmail, testPassword)

	var (
		mu         sync.Mutex
		statements []string
	)
	m.SetEventDispatcher(func(_ context.Context, ev any) error {
		mu.Lock()
		defer mu.Unlock()
		if q, ok := ev.(*orm.QueryExecuted); ok {
			statements = append(statements, q.SQL)
		}
		return nil
	})

	ctx := context.Background()

	// Drain the seeding statements: query events are buffered and delivered
	// to whichever dispatcher is installed at flush time, so only an
	// explicit drain keeps them out of the capture below.
	if err := m.FlushQueryEvents(ctx); err != nil {
		t.Fatalf("drain seed events: %v", err)
	}
	mu.Lock()
	statements = nil
	mu.Unlock()

	p := newAdminStore(t)

	user, err := p.FindByCredentialsCtx(ctx, map[string]interface{}{"username": "root"})
	if err != nil {
		t.Fatalf("FindByCredentialsCtx: %v", err)
	}
	if err := p.UpdateRememberTokenCtx(ctx, user, "token-1"); err != nil {
		t.Fatalf("UpdateRememberTokenCtx: %v", err)
	}
	if err := m.FlushQueryEvents(ctx); err != nil {
		t.Fatalf("FlushQueryEvents: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	sawSelect, sawUpdate := false, false
	for _, stmt := range statements {
		if strings.Contains(stmt, "`users`") {
			t.Errorf("user store statement touched the users table: %q", stmt)
		}
		if !strings.Contains(stmt, "`admins`") {
			continue
		}
		switch {
		case strings.HasPrefix(stmt, "SELECT"):
			sawSelect = true
			if !strings.Contains(stmt, "`username` = ?") {
				t.Errorf("lookup ignored the configured identifier column: %q", stmt)
			}
		case strings.HasPrefix(stmt, "UPDATE"):
			sawUpdate = true
			if !strings.Contains(stmt, "`recall_token`") {
				t.Errorf("write ignored the configured remember-token column: %q", stmt)
			}
		}
	}
	if !sawSelect {
		t.Fatal("no SELECT against admins was recorded; the model swap did not take effect")
	}
	if !sawUpdate {
		t.Fatal("no UPDATE against admins was recorded")
	}
}
