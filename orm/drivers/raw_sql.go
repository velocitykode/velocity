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
