package orm

import (
	"database/sql"

	"golang.org/x/crypto/bcrypt"
)

// QueryExecutor interface for *sql.DB and *sql.Tx
type QueryExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func getDefaultPort(driver string) string {
	switch driver {
	case "mysql":
		return "3306"
	case "postgres":
		return "5432"
	case "sqlite":
		return ""
	default:
		return ""
	}
}

// Hash hashes a password using bcrypt
func Hash(password string) string {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		panic("failed to hash password: " + err.Error())
	}
	return string(hashed)
}
