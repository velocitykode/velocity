package orm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sync"
)

// Morph is the value held by a polymorphic relation field. TypeName matches
// a name registered with RegisterMorph; ID is the foreign key value (any
// scalar the database column accepts). Resolved is populated when the field
// is eager-loaded via Query.With(...) and is the loaded related model
// (typically *T for the registered type).
//
// Note: eager-load (Query.With) and the single-row Resolve method differ on
// unknown morph type-names. Eager-load defaults to non-strict mode: rows
// referencing an unregistered type are skipped with a logged warning so a
// list view does not crash when schema gains a new morph type before all
// callers register it. Toggle with SetMorphStrict for tests or callers that
// want fail-fast behavior. Morph.Resolve always returns an error for
// unknown types because the caller has direct access to handle it.
type Morph struct {
	// TypeName is the discriminator value stored in the polymorphic
	// "type" column. It must be registered via RegisterMorph for resolution
	// to succeed.
	TypeName string
	// ID is the foreign key value stored in the polymorphic "id" column.
	ID any
	// Resolved is populated by eager loading or by a successful Resolve call.
	// It points at the loaded related model (e.g. *Post when TypeName="post").
	Resolved any
}

// IsZero reports whether the morph carries no type or id information.
func (m Morph) IsZero() bool {
	return m.TypeName == "" && (m.ID == nil || isZeroKey(normalizeKey(m.ID)))
}

// morphRegistry holds the registered type-name -> reflect.Type mappings.
// A sync.RWMutex guards both read and write access; concurrent goroutines
// that look up types during eager loading and call RegisterMorph at startup
// must not race on the underlying map.
var morphRegistry = struct {
	mu    sync.RWMutex
	types map[string]reflect.Type
}{
	types: make(map[string]reflect.Type),
}

// RegisterMorph associates a polymorphic type-name discriminator with the
// concrete model type used when resolving morph values. Safe for concurrent
// use; typically called at app boot. Re-registering the same name overwrites
// the previous binding so test setups can swap implementations.
func RegisterMorph(typeName string, modelType reflect.Type) {
	if typeName == "" || modelType == nil {
		return
	}
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}
	morphRegistry.mu.Lock()
	defer morphRegistry.mu.Unlock()
	morphRegistry.types[typeName] = modelType
}

// LookupMorph returns the model type registered for typeName, or false if
// no such mapping exists.
func LookupMorph(typeName string) (reflect.Type, bool) {
	morphRegistry.mu.RLock()
	defer morphRegistry.mu.RUnlock()
	t, ok := morphRegistry.types[typeName]
	return t, ok
}

// ResetMorphRegistry clears every registered morph mapping. Intended for
// tests; production code should never need this.
func ResetMorphRegistry() {
	morphRegistry.mu.Lock()
	defer morphRegistry.mu.Unlock()
	morphRegistry.types = make(map[string]reflect.Type)
}

// morphStrictMode controls eager-load behavior when a polymorphic row
// references an unregistered type-name. A sync.RWMutex guards the flag so
// concurrent eager-loads and SetMorphStrict calls do not race.
var morphStrictMode = struct {
	mu     sync.RWMutex
	strict bool
}{}

// SetMorphStrict configures whether unknown morph type-names cause
// eager-load to error (true) or be skipped with a logged warning
// (false, default). Strict is appropriate for tests and for callers
// that want to fail fast on schema drift.
func SetMorphStrict(strict bool) {
	morphStrictMode.mu.Lock()
	defer morphStrictMode.mu.Unlock()
	morphStrictMode.strict = strict
}

// MorphStrict reports whether eager-load is currently in strict mode.
// Default is false: unknown morph types are logged and skipped rather
// than failing the entire batch.
func MorphStrict() bool {
	morphStrictMode.mu.RLock()
	defer morphStrictMode.mu.RUnlock()
	return morphStrictMode.strict
}

// morphWarn holds the io.Writer that non-strict eager-load uses to surface
// unknown-type warnings. Tests inject a buffer via SetMorphWarnWriter so
// they do not race on the os.Stderr global across packages under -race.
var morphWarn = struct {
	mu sync.RWMutex
	w  io.Writer
}{w: os.Stderr}

// SetMorphWarnWriter swaps the writer used for non-strict morph warnings.
// Pass nil to silence warnings entirely. Returns the previous writer so
// tests can restore it via t.Cleanup.
func SetMorphWarnWriter(w io.Writer) (previous io.Writer) {
	morphWarn.mu.Lock()
	defer morphWarn.mu.Unlock()
	previous = morphWarn.w
	morphWarn.w = w
	return previous
}

// morphWarnWriter returns the configured writer for non-strict morph
// warnings, or nil when warnings are silenced.
func morphWarnWriter() io.Writer {
	morphWarn.mu.RLock()
	defer morphWarn.mu.RUnlock()
	return morphWarn.w
}

// Resolve loads the model row identified by m.TypeName and m.ID, sets it on
// m.Resolved, and returns it. Returns a clear error wrapping the unknown
// type-name if the registry has no entry for m.TypeName.
func (m *Morph) Resolve(ctx context.Context) (any, error) {
	if m == nil {
		return nil, errors.New("orm: Morph.Resolve: receiver is nil")
	}
	if m.TypeName == "" {
		return nil, errors.New("orm: Morph.Resolve: empty TypeName")
	}
	if m.ID == nil || isZeroKey(normalizeKey(m.ID)) {
		return nil, errors.New("orm: Morph.Resolve: zero ID")
	}

	relatedType, ok := LookupMorph(m.TypeName)
	if !ok {
		return nil, fmt.Errorf("orm: Morph.Resolve: unknown morph type %q - call orm.RegisterMorph(%q, reflect.TypeOf(YourModel{})) at startup", m.TypeName, m.TypeName)
	}

	mgr := Default()
	if mgr == nil {
		return nil, errors.New("orm: Morph.Resolve: no default manager set")
	}
	driver, err := mgr.liveDriver()
	if err != nil {
		return nil, fmt.Errorf("orm: Morph.Resolve: %w", err)
	}

	tableName := resolveTableNameReflect(relatedType)
	if err := validateIdentifier(tableName); err != nil {
		return nil, fmt.Errorf("orm: Morph.Resolve: invalid table name for %s: %w", relatedType.Name(), err)
	}

	// Honour every global scope registered on relatedType (tenant,
	// archive, locale, state, soft-delete, ...). The IN-of-one shape
	// keeps the helper signature uniform with the batched eager-load
	// path; the grammar's single-element IN compiles to "id IN ($1)"
	// which every supported driver handles identically to "id = $1".
	// A scope that fails validation surfaces here; propagate the error
	// rather than execute SQL with the scope silently dropped.
	sqlStr, sqlArgs, scopeErr := buildScopedInSelect(ctx, driver, relatedType, tableName, "id", []any{m.ID})
	if scopeErr != nil {
		return nil, fmt.Errorf("orm: Morph.Resolve: scope error: %w", scopeErr)
	}
	rows, err := driver.QueryContext(ctx, sqlStr, sqlArgs...)
	if err != nil {
		return nil, fmt.Errorf("orm: Morph.Resolve: query failed: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrRecordNotFound
	}
	ptr := reflect.New(relatedType)
	if err := scanIntoStruct(rows, ptr.Interface()); err != nil {
		return nil, fmt.Errorf("orm: Morph.Resolve: scan failed: %w", err)
	}
	markIsExisting(ptr.Elem())
	m.Resolved = ptr.Interface()
	return m.Resolved, nil
}
