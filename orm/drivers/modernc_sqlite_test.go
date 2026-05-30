package drivers

// The SQLite connector tests in this package (Connect, perms, context
// cancellation) open the pure-Go modernc database/sql driver via
// NewSQLiteDriver. modernc self-registers under the name "sqlite" from this
// blank import; it is a test-only dependency so `go list -deps ./orm/drivers`
// stays free of any SQLite backend.
import _ "modernc.org/sqlite"
