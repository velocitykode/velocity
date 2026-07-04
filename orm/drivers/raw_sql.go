package drivers

// RawSQL marks a value in an Update or Insert map as raw SQL rather than a
// bound parameter. Values of this type are emitted verbatim into the
// generated statement; any other value (including plain string values that
// happen to look like SQL) is bound as a parameter.
//
// Use this type for trusted, server-constructed SQL fragments such as
// function calls (NOW(), CURRENT_TIMESTAMP) or column arithmetic. Never
// construct a RawSQL value from user-controlled input — doing so is a
// SQL-injection vector by design.
//
// Driver-portability note: function names differ per dialect
// (MySQL/Postgres use NOW(); SQLite uses CURRENT_TIMESTAMP). RawSQL is a
// dumb pass-through: the caller is responsible for picking a fragment
// valid for the target driver, or for using the higher-level ORM helpers
// that resolve the driver-appropriate sentinel for common cases.
type RawSQL string

// NOW and CurrentTimestamp are the well-known current-timestamp sentinels
// (re-exported as orm.NOW / orm.CurrentTimestamp). Grammars recognize
// exactly these two typed values in their RawSQL emission branch and pin
// the emitted SQL to a UTC wall clock (see each grammar's rawSQLExpr), so
// the stored value in a naive timestamp column is independent of the
// database session timezone. Contract: DB clock, UTC wall clock. Any other
// RawSQL value is emitted verbatim.
const (
	NOW              RawSQL = "NOW()"
	CurrentTimestamp RawSQL = "CURRENT_TIMESTAMP"
)

// pgRawSQLExpr returns the SQL fragment to emit for a RawSQL value on
// PostgreSQL. The current-timestamp sentinels become a naive timestamp
// carrying the UTC wall clock: correct for the framework-default naive
// timestamp columns regardless of session TimeZone. (Into a timestamptz
// column under a hand-set non-UTC session it would be misread; documented
// on the sentinels.)
func pgRawSQLExpr(raw RawSQL) string {
	switch raw {
	case NOW, CurrentTimestamp:
		return "(NOW() AT TIME ZONE 'UTC')"
	}
	return string(raw)
}

// mysqlRawSQLExpr returns the SQL fragment to emit for a RawSQL value on
// MySQL. The current-timestamp sentinels become UTC_TIMESTAMP(), a DATETIME
// carrying the UTC wall clock independent of session time_zone.
func mysqlRawSQLExpr(raw RawSQL) string {
	switch raw {
	case NOW, CurrentTimestamp:
		return "UTC_TIMESTAMP()"
	}
	return string(raw)
}

// sqliteRawSQLExpr returns the SQL fragment to emit for a RawSQL value on
// SQLite. CURRENT_TIMESTAMP is already UTC there; NOW additionally maps to
// it because SQLite has no NOW() function (previously the sentinel emitted
// invalid SQL on this driver).
func sqliteRawSQLExpr(raw RawSQL) string {
	switch raw {
	case NOW, CurrentTimestamp:
		return "CURRENT_TIMESTAMP"
	}
	return string(raw)
}

// RawSQLExprFor returns the SQL fragment to emit for a RawSQL value on the
// named driver, applying the same UTC pinning of the current-timestamp
// sentinels the grammars use. Callers that build SQL outside a grammar
// method (e.g. the ORM's map-based INSERT builder) use this so sentinels
// behave identically in UPDATE and INSERT maps. Unknown driver names emit
// the value verbatim.
func RawSQLExprFor(driverName string, raw RawSQL) string {
	switch driverName {
	case "postgres":
		return pgRawSQLExpr(raw)
	case "mysql":
		return mysqlRawSQLExpr(raw)
	case "sqlite", "sqlite3":
		return sqliteRawSQLExpr(raw)
	}
	return string(raw)
}
