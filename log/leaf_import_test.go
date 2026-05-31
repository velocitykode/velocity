package log_test

// Several log root tests construct loggers that resolve the "file", "daily",
// and "stack" drivers (manager, redact, and contract tests). Those drivers live
// in leaf packages that self-register from their init()s, so the standard
// aggregator is blank-imported here to make the full driver set available to
// the log test binary, as an application would in production.
import _ "github.com/velocitykode/velocity/log/standard"
