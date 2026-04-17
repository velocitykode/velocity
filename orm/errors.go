package orm

import "errors"

var (
	ErrNoRows         = errors.New("velocity/orm: no rows found")
	ErrDriverNotFound = errors.New("velocity/orm: driver not found")
)
