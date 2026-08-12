package ormauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/orm"
)

// Store is an [auth.UserStore] backed by the ORM's generic query
// builder for model type T. Every read and write goes through
// orm.Model[T], so the table name, the placeholder dialect, soft-delete
// scoping, and identifier quoting are all owned by the ORM grammar
// rather than reimplemented here.
//
// A Store is immutable after construction and safe for concurrent
// use; all per-call state lives on the stack.
type Store[T any] struct {
	opts Options

	// meta is the ORM's canonical reflection view of T.
	meta *orm.ModelMeta

	// pk is the primary-key column. It backs GetAuthIdentifier and the
	// WHERE clause of every write, and is deliberately not configurable:
	// the model's own ORM tags already declare it.
	pk orm.ColumnDef

	// native records that *T implements auth.Authenticatable itself, in
	// which case lookups hand the model straight to the caller and the
	// password/remember-token column mapping is bypassed.
	native bool

	// passwordPath / rememberPath are reflect index paths used only on
	// the non-native path.
	passwordPath []int
	rememberPath []int

	// err holds a construction failure. New reports it through Validate
	// (so the documented constructor signature stays allocation-simple)
	// and every I/O method returns it rather than issuing a query.
	err error
}

// compile-time interface conformance.
var (
	_ auth.UserStore                      = (*Store[User])(nil)
	_ auth.RememberTokenCompareAndSwapper = (*Store[User])(nil)
)

// New builds a Store for model type T.
//
// Construction never panics and never returns an error inline; a model
// that cannot be mapped records the failure, which [Store.Validate]
// reports and every query method returns. Callers wiring from
// configuration should go through [Factory] / [Resolve], which surface
// that failure at startup.
func New[T any](opts ...Option) *Store[T] {
	p := &Store[T]{opts: resolveOptions(opts)}
	p.resolve()
	return p
}

// Validate reports whether the user store mapped cleanly onto T.
func (p *Store[T]) Validate() error { return p.err }

// Options returns the resolved configuration. Useful for diagnostics.
func (p *Store[T]) Options() Options { return p.opts }

// resolve maps the configured columns onto T once, at construction.
func (p *Store[T]) resolve() {
	var zero T

	// Resolve T through *T rather than reflect.TypeOf(zero): the latter is
	// nil for a pointer or interface T, which would both lose the name from
	// every diagnostic below and hide the rejection this guard exists for.
	modelName := reflect.TypeOf(&zero).Elem()

	// T must be the struct itself, never a pointer to it. orm.MetaFor
	// derefs any nesting of pointers, so T = *Admin would map cleanly here
	// and then panic downstream: the zero value of *Admin is nil, so the
	// FieldByIndex walk in wrap would indirect through a nil pointer. Both
	// query paths are equally broken for a pointer T (orm.Model[*Admin]
	// scans into **Admin), so this is a rejection, not a supported shape.
	if modelName.Kind() != reflect.Struct {
		p.err = fmt.Errorf("velocity/ormauth: %v is not a struct model; instantiate with the model type itself, not a pointer or interface (ormauth.New[Admin], not ormauth.New[*Admin])", modelName)
		return
	}

	p.meta = orm.MetaFor(modelName)
	if p.meta == nil {
		p.err = fmt.Errorf("velocity/ormauth: %v has no ORM metadata", modelName)
		return
	}

	pk, ok := p.meta.PrimaryKeyColumn()
	if !ok {
		p.err = fmt.Errorf("velocity/ormauth: model %v declares no primary key; auth needs one for GetAuthIdentifier", modelName)
		return
	}
	p.pk = pk

	if _, ok := any(&zero).(auth.Authenticatable); ok {
		p.native = true
	}

	// The identifier column is only ever used in a WHERE clause, so it
	// needs to exist but its Go type is irrelevant.
	if _, ok := p.meta.ColumnByName(p.opts.IdentifierColumn); !ok {
		p.err = fmt.Errorf("velocity/ormauth: model %v has no column %q (configure it with ormauth.WithIdentifierColumn); columns: %v",
			modelName, p.opts.IdentifierColumn, columnNames(p.meta))
		return
	}

	// The remember-token column is written on the login path and on
	// rotation, so it is required even for a model that implements
	// auth.Authenticatable itself: the interface exposes the token but
	// not where to persist it.
	remember, ok := p.meta.ColumnByName(p.opts.RememberTokenColumn)
	if !ok {
		p.err = fmt.Errorf("velocity/ormauth: model %v has no column %q (configure it with ormauth.WithRememberTokenColumn); columns: %v",
			modelName, p.opts.RememberTokenColumn, columnNames(p.meta))
		return
	}
	p.rememberPath = remember.IndexPath

	// The remember token is persisted through the ORM's map-based
	// Update, which is the mass-assignment-policed path. A model that
	// declares no policy at all rejects every map key, so remember-me
	// would fail on first use at runtime; refuse at startup instead.
	if implicitDeny(&zero) {
		p.err = fmt.Errorf("velocity/ormauth: model %v declares no mass-assignment policy, so writing %q would be rejected; declare AssignableFields() including %q (or ProtectedFields()/AllowAllColumns)",
			modelName, p.opts.RememberTokenColumn, p.opts.RememberTokenColumn)
		return
	}

	if p.native {
		return
	}

	if err := stringFieldKind(p.meta, remember); err != nil {
		p.err = fmt.Errorf("velocity/ormauth: model %v column %q: %w", modelName, p.opts.RememberTokenColumn, err)
		return
	}

	password, ok := p.meta.ColumnByName(p.opts.PasswordColumn)
	if !ok {
		p.err = fmt.Errorf("velocity/ormauth: model %v has no column %q (configure it with ormauth.WithPasswordColumn); columns: %v",
			modelName, p.opts.PasswordColumn, columnNames(p.meta))
		return
	}
	if err := stringFieldKind(p.meta, password); err != nil {
		p.err = fmt.Errorf("velocity/ormauth: model %v column %q: %w", modelName, p.opts.PasswordColumn, err)
		return
	}
	p.passwordPath = password.IndexPath
}

// FindByIDCtx retrieves a user by primary key.
func (p *Store[T]) FindByIDCtx(ctx context.Context, id interface{}) (auth.Authenticatable, error) {
	if p.err != nil {
		return nil, p.err
	}
	if id == nil {
		return nil, auth.ErrUserNotFound
	}
	return p.first(ctx, p.pk.Column, id)
}

// FindByID retrieves a user by primary key.
//
// Deprecated: use FindByIDCtx with a request-scoped context.Context.
func (p *Store[T]) FindByID(id interface{}) (auth.Authenticatable, error) {
	return p.FindByIDCtx(context.Background(), id)
}

// FindByCredentialsCtx retrieves a user by the configured identifier
// column, read from the credentials map under the configured key.
func (p *Store[T]) FindByCredentialsCtx(ctx context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	if p.err != nil {
		return nil, p.err
	}
	value, ok := credentials[p.opts.CredentialsKey].(string)
	if !ok {
		return nil, fmt.Errorf("velocity/ormauth: credential %q is required", p.opts.CredentialsKey)
	}
	return p.first(ctx, p.opts.IdentifierColumn, value)
}

// FindByCredentials retrieves a user by credentials.
//
// Deprecated: use FindByCredentialsCtx with a request-scoped context.Context.
func (p *Store[T]) FindByCredentials(credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentialsCtx(context.Background(), credentials)
}

// first runs the single-row lookup shared by both find paths.
func (p *Store[T]) first(ctx context.Context, column string, value any) (auth.Authenticatable, error) {
	var model T
	if err := (orm.Model[T]{}).Where(column+" = ?", value).First(ctx, &model); err != nil {
		// orm.ErrRecordNotFound is sql.ErrNoRows; match the sentinel so
		// a driver surfacing the bare stdlib error is covered too.
		if errors.Is(err, sql.ErrNoRows) {
			return nil, auth.ErrUserNotFound
		}
		return nil, err
	}
	return p.wrap(&model)
}

// ValidateCredentials compares a candidate password against the stored
// hash. Pure CPU work; no query is issued.
func (p *Store[T]) ValidateCredentials(user auth.Authenticatable, credentials map[string]interface{}) bool {
	if user == nil {
		return false
	}
	password, ok := credentials["password"].(string)
	if !ok {
		return false
	}
	return p.opts.Hasher.Verify(password, user.GetAuthPassword())
}

// UpdateRememberTokenCtx persists a freshly minted remember token. Used
// on the login path, where no prior token is being consumed; rotation of
// an existing token goes through CompareAndSwapRememberToken.
func (p *Store[T]) UpdateRememberTokenCtx(ctx context.Context, user auth.Authenticatable, token string) error {
	if p.err != nil {
		return p.err
	}
	if user == nil {
		return auth.ErrUserNotFound
	}
	// Mutate first, matching the previous user store: the in-memory user
	// carries the token the caller is about to write to the cookie even
	// if persistence fails.
	user.SetRememberToken(token)

	_, err := (orm.Model[T]{}).
		Where(p.pk.Column+" = ?", user.GetAuthIdentifier()).
		Update(ctx, map[string]any{p.opts.RememberTokenColumn: token})
	return err
}

// UpdateRememberToken persists a remember token.
//
// Deprecated: use UpdateRememberTokenCtx with a request-scoped context.Context.
func (p *Store[T]) UpdateRememberToken(user auth.Authenticatable, token string) error {
	return p.UpdateRememberTokenCtx(context.Background(), user, token)
}

// CompareAndSwapRememberToken implements [auth.RememberTokenCompareAndSwapper].
// The stored token is replaced only when the row still holds oldToken, in
// a single conditional UPDATE, so two concurrent recalls of the same
// cookie cannot both mint a valid credential. Returns false with a nil
// error when no row matched; the in-memory user is mutated only on a
// successful swap.
func (p *Store[T]) CompareAndSwapRememberToken(ctx context.Context, user auth.Authenticatable, oldToken, newToken string) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	if user == nil {
		return false, auth.ErrUserNotFound
	}

	affected, err := (orm.Model[T]{}).
		Where(p.pk.Column+" = ?", user.GetAuthIdentifier()).
		Where(p.opts.RememberTokenColumn+" = ?", oldToken).
		Update(ctx, map[string]any{p.opts.RememberTokenColumn: newToken})
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil
	}

	user.SetRememberToken(newToken)
	return true, nil
}

// wrap adapts a loaded model to auth.Authenticatable.
func (p *Store[T]) wrap(model *T) (auth.Authenticatable, error) {
	if p.native {
		return any(model).(auth.Authenticatable), nil
	}

	value := reflect.ValueOf(model).Elem()

	password, err := readString(value.FieldByIndex(p.passwordPath))
	if err != nil {
		return nil, fmt.Errorf("velocity/ormauth: reading %q: %w", p.opts.PasswordColumn, err)
	}
	remember, err := readString(value.FieldByIndex(p.rememberPath))
	if err != nil {
		return nil, fmt.Errorf("velocity/ormauth: reading %q: %w", p.opts.RememberTokenColumn, err)
	}

	return &mappedUser[T]{
		model:    model,
		id:       auth.NormalizeID(value.FieldByIndex(p.pk.IndexPath).Interface()),
		password: password,
		remember: remember,
		writeRemember: func(token string) {
			// Best effort: keep the underlying record consistent with
			// the adapter so a caller reaching through Model() does not
			// observe a stale token. A field that cannot be set (an
			// unexported shadow) simply leaves the model untouched.
			writeString(value.FieldByIndex(p.rememberPath), token)
		},
	}, nil
}

// mappedUser adapts a model that does not implement auth.Authenticatable
// itself, projecting the configured columns onto the interface.
type mappedUser[T any] struct {
	model         *T
	id            interface{}
	password      string
	remember      string
	writeRemember func(string)
}

// Model returns the underlying record, so application code holding an
// auth.Authenticatable can recover its own model type:
//
//	if m, ok := user.(interface{ Model() *models.Admin }); ok { ... }
func (u *mappedUser[T]) Model() *T { return u.model }

// GetAuthIdentifier returns the primary key.
func (u *mappedUser[T]) GetAuthIdentifier() interface{} { return u.id }

// GetAuthPassword returns the stored password hash.
func (u *mappedUser[T]) GetAuthPassword() string { return u.password }

// GetRememberToken returns the remember-me token.
func (u *mappedUser[T]) GetRememberToken() string { return u.remember }

// SetRememberToken updates the token on the adapter and the record.
func (u *mappedUser[T]) SetRememberToken(token string) {
	u.remember = token
	if u.writeRemember != nil {
		u.writeRemember(token)
	}
}

// String renders the adapter for logs without leaking the hash.
func (u *mappedUser[T]) String() string {
	var zero T
	return fmt.Sprintf("ormauth.User<%T %v>", zero, u.id)
}
