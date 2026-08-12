package orm

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrNoRows         = errors.New("velocity/orm: no rows found")
	ErrDriverNotFound = errors.New("velocity/orm: driver not found")
	// ErrNoConnection is returned by execution entry points when no
	// default database connection was ever configured. It signals a
	// boot/wiring mistake: the manager exists but was never connected,
	// or no default manager was set. Matchable with errors.Is.
	ErrNoConnection = errors.New("velocity/orm: no database connection")
	// ErrManagerShutdown is returned by execution entry points when the
	// Manager has been shut down via Shutdown. It signals a
	// teardown-ordering mistake: application code issued a query after
	// (or racing) manager teardown. Distinct from ErrNoConnection so the
	// operator knows to fix lifecycle ordering, not connection config.
	// Matchable with errors.Is.
	ErrManagerShutdown = errors.New("velocity/orm: manager is shut down")
	// ErrNoTxCallbacks is returned by orm.OnCommit / orm.OnRollback
	// when ctx carries no active per-tx callbacks holder. Callers
	// either forgot to wrap ctx with PrepareTxCallbacks before
	// passing it to Manager.Transaction, or are calling outside any
	// active Transaction. The error is wrapped (errors.Is friendly)
	// so callers can branch on it to fall back to inline execution.
	ErrNoTxCallbacks = errors.New("velocity/orm: no active tx callbacks holder; wrap ctx with orm.PrepareTxCallbacks before Transaction")
)

// MassAssignmentError is returned by map-based writes (Create(map),
// FirstOrCreate, UpdateOrCreate) when the target model declares no
// mass-assignment policy. Mass assignment is deny-by-default: a model
// must declare AssignableFields() (allowlist) or ProtectedFields()
// (denylist) before any application column can be written from a map, or
// explicitly opt back into the open behavior via AllowAllColumns.
//
// The message names the model and the rejected keys for developers and
// logs. Do not echo it to HTTP clients: the framework's production error
// renderer already collapses 5xx errors to a generic status text, and
// custom handlers should do the same.
type MassAssignmentError struct {
	// Model is the Go type of the rejected model, e.g. "models.User".
	Model string
	// Keys are the map keys that resolved to application columns and
	// were therefore rejected. Keys that match no column are ignored,
	// and framework-managed embedded columns (id, timestamps,
	// deleted_at) bypass policy and never appear here.
	Keys []string
}

func (e *MassAssignmentError) Error() string {
	return fmt.Sprintf(
		"velocity/orm: mass assignment denied for model %s: keys [%s] rejected because the model declares no mass-assignment policy; declare AssignableFields() (allowlist) or ProtectedFields() (denylist), or restore the previous allow-all behavior by implementing AllowAllColumns() bool { return true }",
		e.Model, strings.Join(e.Keys, ", "))
}
