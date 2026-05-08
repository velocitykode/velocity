package orm

import "errors"

var (
	ErrNoRows         = errors.New("velocity/orm: no rows found")
	ErrDriverNotFound = errors.New("velocity/orm: driver not found")
	// ErrNoTxCallbacks is returned by orm.OnCommit / orm.OnRollback
	// when ctx carries no active per-tx callbacks holder. Callers
	// either forgot to wrap ctx with PrepareTxCallbacks before
	// passing it to Manager.Transaction, or are calling outside any
	// active Transaction. The error is wrapped (errors.Is friendly)
	// so callers can branch on it to fall back to inline execution.
	ErrNoTxCallbacks = errors.New("velocity/orm: no active tx callbacks holder; wrap ctx with orm.PrepareTxCallbacks before Transaction")
)
