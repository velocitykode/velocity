package ormauth_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/providers/ormauth"
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

// Fillable declares a mass-assignment policy so the remember-token write is
// not rejected by the ORM's deny-by-default gate.
func (Admin) Fillable() []string { return []string{"username", "pass_hash", "recall_token"} }

// Operator covers the *string carrier for the remember-token column.
type Operator struct {
	orm.IDInt[Operator]

	Email    string  `orm:"column:email"`
	Password string  `orm:"column:password"`
	Recall   *string `orm:"column:remember_token"`
}

// Guarded declares an empty denylist, which is the other way out of
// deny-by-default.
func (Operator) Guarded() []string { return nil }

// NoPolicy declares neither Fillable nor Guarded, so every map-based write
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

// Fillable satisfies the mass-assignment gate so the primary-key failure is
// the one the test observes.
func (NoPrimaryKey) Fillable() []string { return []string{"email", "password", "remember_token"} }

// BadTypes carries a remember-token column that cannot hold a string.
type BadTypes struct {
	orm.IDInt[BadTypes]

	Email         string `orm:"column:email"`
	Password      string `orm:"column:password"`
	RememberToken int    `orm:"column:remember_token"`
}

// Fillable satisfies the mass-assignment gate.
func (BadTypes) Fillable() []string { return []string{"email", "password", "remember_token"} }

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

// adminFactory is the registration an application with an Admin model would
// write: every column renamed away from the defaults.
func adminFactory() ormauth.ProviderFactory {
	return ormauth.Factory[Admin](
		ormauth.WithIdentifierColumn("username"),
		ormauth.WithPasswordColumn("pass_hash"),
		ormauth.WithRememberTokenColumn("recall_token"),
	)
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

// TestRegistry_DefaultUserIsPreRegistered pins the backward-compatibility
// guarantee: an application that never registered anything still resolves
// the AUTH_MODEL default.
func TestRegistry_DefaultUserIsPreRegistered(t *testing.T) {
	if _, ok := ormauth.Lookup(ormauth.DefaultModelName); !ok {
		t.Fatalf("%q is not pre-registered", ormauth.DefaultModelName)
	}

	found := false
	for _, name := range ormauth.Registered() {
		if name == ormauth.DefaultModelName {
			found = true
		}
	}
	if !found {
		t.Errorf("Registered() = %v, want it to contain %q", ormauth.Registered(), ormauth.DefaultModelName)
	}
}

// TestRegistry_UnregisteredModelIsAnError is the core of the fix: a model
// name nobody registered must fail loudly, naming the model and listing
// what is available, instead of silently querying the users table.
func TestRegistry_UnregisteredModelIsAnError(t *testing.T) {
	_, err := ormauth.Resolve("Admin")
	if err == nil {
		t.Fatal("resolving an unregistered model succeeded; a mistyped AUTH_MODEL would authenticate against the wrong table")
	}
	if !strings.Contains(err.Error(), `"Admin"`) {
		t.Errorf("error does not name the model: %v", err)
	}
	if !strings.Contains(err.Error(), ormauth.DefaultModelName) {
		t.Errorf("error does not list the registered models: %v", err)
	}
}

// TestRegistry_EmptyNameFallsBackToDefault covers the config path where
// auth.ProviderConfig.Model was never set.
func TestRegistry_EmptyNameFallsBackToDefault(t *testing.T) {
	newManager(t)
	if _, err := ormauth.Resolve(""); err != nil {
		t.Fatalf("empty model name should resolve the default: %v", err)
	}
}

// TestRegistry_RegisterAndUnregister covers the application-side lifecycle.
func TestRegistry_RegisterAndUnregister(t *testing.T) {
	if err := ormauth.Register("Admin", adminFactory()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { ormauth.Unregister("Admin") })

	if _, err := ormauth.Resolve("Admin"); err != nil {
		t.Fatalf("Resolve after Register: %v", err)
	}

	if !ormauth.Unregister("Admin") {
		t.Error("Unregister reported the name was not registered")
	}
	if ormauth.Unregister("Admin") {
		t.Error("Unregister reported a second removal")
	}
	if _, err := ormauth.Resolve("Admin"); err == nil {
		t.Error("Resolve succeeded after Unregister")
	}
}

// TestRegistry_RegisterRejectsBadInput covers the two wiring mistakes.
// Register reports them as errors (library code does not panic); only the
// init()-oriented MustRegister wrapper escalates.
func TestRegistry_RegisterRejectsBadInput(t *testing.T) {
	t.Run("empty name returns an error", func(t *testing.T) {
		if err := ormauth.Register("", ormauth.Factory[Admin]()); err == nil {
			t.Error("Register with an empty name returned nil")
		}
	})

	t.Run("nil factory returns an error", func(t *testing.T) {
		if err := ormauth.Register("Nil", nil); err == nil {
			t.Error("Register with a nil factory returned nil")
		}
		if _, ok := ormauth.Lookup("Nil"); ok {
			t.Error("a rejected registration was still stored")
		}
	})

	t.Run("MustRegister panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("MustRegister with a nil factory did not panic")
			}
		}()
		ormauth.MustRegister("Nil", nil)
	})
}

// TestFactory_CallSiteOptionsWinOverRegistrationDefaults pins the option
// precedence the framework relies on when it threads the auth manager's
// hasher into a factory that already baked in its own.
func TestFactory_CallSiteOptionsWinOverRegistrationDefaults(t *testing.T) {
	factory := ormauth.Factory[Admin](
		ormauth.WithIdentifierColumn("username"),
		ormauth.WithPasswordColumn("pass_hash"),
		ormauth.WithRememberTokenColumn("recall_token"),
		ormauth.WithCredentialsKey("registered-key"),
	)

	provider, err := factory(ormauth.WithCredentialsKey("call-site-key"))
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	opts := provider.(*ormauth.Provider[Admin]).Options()
	if opts.CredentialsKey != "call-site-key" {
		t.Errorf("CredentialsKey = %q, want the call-site value", opts.CredentialsKey)
	}
	if opts.IdentifierColumn != "username" {
		t.Errorf("IdentifierColumn = %q, want the registration default to survive", opts.IdentifierColumn)
	}
}

// TestNew_RejectsUnmappableModels covers every construction failure, each of
// which is a startup error rather than a runtime surprise.
func TestNew_RejectsUnmappableModels(t *testing.T) {
	tests := []struct {
		name     string
		provider interface{ Validate() error }
		wants    string
	}{
		{
			name:     "missing identifier column",
			provider: ormauth.New[Admin](),
			wants:    `has no column "email"`,
		},
		{
			name:     "missing remember token column",
			provider: ormauth.New[Admin](ormauth.WithIdentifierColumn("username")),
			wants:    `has no column "remember_token"`,
		},
		{
			name: "missing password column",
			provider: ormauth.New[Admin](
				ormauth.WithIdentifierColumn("username"),
				ormauth.WithRememberTokenColumn("recall_token"),
			),
			wants: `has no column "password"`,
		},
		{
			name:     "no mass-assignment policy",
			provider: ormauth.New[NoPolicy](),
			wants:    "declares no mass-assignment policy",
		},
		{
			name:     "no primary key",
			provider: ormauth.New[NoPrimaryKey](),
			wants:    "declares no primary key",
		},
		{
			name:     "unusable column type",
			provider: ormauth.New[BadTypes](),
			wants:    "must be string, *string, or sql.NullString",
		},
		{
			name:     "not a struct",
			provider: ormauth.New[int](),
			wants:    "is not a struct model",
		},
		{
			// orm.MetaFor derefs pointers, so a pointer T maps cleanly
			// and would then indirect through a nil pointer in wrap.
			name:     "pointer to a struct",
			provider: ormauth.New[*Admin](),
			wants:    "is not a struct model",
		},
		{
			name:     "interface",
			provider: ormauth.New[auth.Authenticatable](),
			wants:    "is not a struct model",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.provider.Validate()
			if err == nil {
				t.Fatal("expected a construction error")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wants)
			}
		})
	}
}

// TestProvider_UnvalidatedProviderNeverQueries proves a provider that
// failed to map refuses I/O instead of issuing a wrong query.
func TestProvider_UnvalidatedProviderNeverQueries(t *testing.T) {
	newManager(t)
	p := ormauth.New[NoPolicy]()
	ctx := context.Background()

	if _, err := p.FindByIDCtx(ctx, 1); err == nil {
		t.Error("FindByIDCtx on an unmapped provider succeeded")
	}
	if _, err := p.FindByCredentialsCtx(ctx, map[string]interface{}{"email": testEmail}); err == nil {
		t.Error("FindByCredentialsCtx on an unmapped provider succeeded")
	}
	if err := p.UpdateRememberTokenCtx(ctx, &auth.AuthUser{ID: uint(1)}, "t"); err == nil {
		t.Error("UpdateRememberTokenCtx on an unmapped provider succeeded")
	}
	if _, err := p.CompareAndSwapRememberToken(ctx, &auth.AuthUser{ID: uint(1)}, "a", "b"); err == nil {
		t.Error("CompareAndSwapRememberToken on an unmapped provider succeeded")
	}
}

// TestProvider_MappedModel_NullString exercises the non-native path: a
// model that does not implement auth.Authenticatable, with every column
// renamed and a sql.NullString remember token.
func TestProvider_MappedModel_NullString(t *testing.T) {
	m := newManager(t)
	if _, err := m.DB().Exec(adminsSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := m.DB().Exec(
		`INSERT INTO admins (username, pass_hash, recall_token) VALUES (?, ?, ?)`,
		"root", "stored-hash", nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	p := ormauth.New[Admin](
		ormauth.WithIdentifierColumn("username"),
		ormauth.WithPasswordColumn("pass_hash"),
		ormauth.WithRememberTokenColumn("recall_token"),
	)
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	ctx := context.Background()

	user, err := p.FindByCredentialsCtx(ctx, map[string]interface{}{"username": "root"})
	if err != nil {
		t.Fatalf("FindByCredentialsCtx: %v", err)
	}
	if got := user.GetAuthIdentifier(); got != uint(1) {
		t.Errorf("identifier = %#v, want uint(1)", got)
	}
	if got := user.GetAuthPassword(); got != "stored-hash" {
		t.Errorf("password = %q, want stored-hash", got)
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

// TestProvider_MappedModel_PointerString covers the *string carrier.
func TestProvider_MappedModel_PointerString(t *testing.T) {
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

// TestRegistry_AdminModel_EndToEnd is the regression test for the dead
// AUTH_MODEL knob, exercised the way an application actually reaches it:
// register the model under the configured name, resolve it *by that name*,
// authenticate, and prove the statements hit admins rather than users.
//
// Before this change, AUTH_MODEL=Admin produced byte-identical SQL against
// the users table. The SQL assertions below are what make that impossible
// to regress silently.
func TestRegistry_AdminModel_EndToEnd(t *testing.T) {
	m := newManager(t)
	seedAdmin(t, m, "root", testPassword)

	// A users table exists too, seeded with a *different* password. If the
	// provider fell back to the old hardcoded table, the credential lookup
	// would find this row instead and the password check below would fail
	// against the wrong hash.
	seedUser(t, m, testEmail, "not-the-admin-password")

	if err := ormauth.Register("Admin", adminFactory()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { ormauth.Unregister("Admin") })

	var (
		mu       sync.Mutex
		executed []*orm.QueryExecuted
	)
	m.SetEventDispatcher(func(_ context.Context, ev any) error {
		mu.Lock()
		defer mu.Unlock()
		if q, ok := ev.(*orm.QueryExecuted); ok {
			executed = append(executed, q)
		}
		return nil
	})
	statements := func() []string {
		if err := m.FlushQueryEvents(context.Background()); err != nil {
			t.Fatalf("FlushQueryEvents: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, 0, len(executed))
		for _, q := range executed {
			out = append(out, q.SQL)
		}
		return out
	}

	// Resolution goes through the registry by name, exactly as
	// initAuth does with auth.ProviderConfig.Model.
	p, err := ormauth.Resolve("Admin", ormauth.WithHasher(auth.NewBcryptHasher(bcrypt.MinCost)))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ctx := context.Background()

	// Full authentication: lookup by the configured identifier column,
	// then password verification against the configured password column.
	credentials := map[string]interface{}{
		"username": "root",
		"password": testPassword,
	}
	user, err := p.FindByCredentialsCtx(ctx, credentials)
	if err != nil {
		t.Fatalf("FindByCredentialsCtx: %v", err)
	}
	if !p.ValidateCredentials(user, credentials) {
		t.Fatal("the seeded admin password was rejected")
	}
	if p.ValidateCredentials(user, map[string]interface{}{"password": "not-the-admin-password"}) {
		t.Fatal("the users-table password was accepted; the provider read the wrong table")
	}
	if got := user.GetAuthIdentifier(); got != uint(1) {
		t.Errorf("identifier = %#v, want uint(1)", got)
	}

	// Remember-me round trip on the renamed column.
	if err := p.UpdateRememberTokenCtx(ctx, user, "admin-token"); err != nil {
		t.Fatalf("UpdateRememberTokenCtx: %v", err)
	}
	swapped, err := p.(auth.RememberTokenCompareAndSwapper).
		CompareAndSwapRememberToken(ctx, user, "admin-token", "admin-token-2")
	if err != nil {
		t.Fatalf("CompareAndSwapRememberToken: %v", err)
	}
	if !swapped {
		t.Fatal("swap on the registered Admin model reported false")
	}

	// Snapshot the recorded statements before the read-back below: the raw
	// *sql.DB is instrumented too, so a verification query issued here
	// would otherwise land in the set being asserted over.
	sqls := statements()

	var stored string
	if err := m.DB().QueryRow(`SELECT recall_token FROM admins WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "admin-token-2" {
		t.Errorf("persisted token = %q, want admin-token-2", stored)
	}

	// Every statement the provider issued must target admins, and none may
	// touch users. Reading the SQL directly is the only assertion that
	// actually distinguishes "the model was honoured" from "the default
	// table happened to contain a matching row".
	if len(sqls) == 0 {
		t.Fatal("the provider emitted no statements")
	}
	sawSelect, sawUpdate := false, false
	for _, stmt := range sqls {
		if strings.Contains(stmt, "`users`") {
			t.Errorf("statement targeted the users table: %q", stmt)
		}
		if !strings.Contains(stmt, "`admins`") {
			t.Errorf("statement targeted neither admins nor users: %q", stmt)
			continue
		}
		switch {
		case strings.HasPrefix(stmt, "SELECT"):
			sawSelect = true
			if !strings.Contains(stmt, "`username` = ?") {
				t.Errorf("lookup did not use the configured identifier column: %q", stmt)
			}
		case strings.HasPrefix(stmt, "UPDATE"):
			sawUpdate = true
			if !strings.Contains(stmt, "`recall_token`") {
				t.Errorf("write did not use the configured remember-token column: %q", stmt)
			}
		}
	}
	if !sawSelect {
		t.Error("no SELECT against admins was recorded")
	}
	if !sawUpdate {
		t.Error("no UPDATE against admins was recorded")
	}
}
