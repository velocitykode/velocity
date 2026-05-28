package contract

import (
	"os"
	"strings"
)

// Environment-classification helpers. Live in contract (leaf package,
// stdlib-only) so every framework subsystem can route through one
// vocabulary regardless of where it sits in the import graph. The root
// app package re-exports these via app.IsProductionEnv etc; subsystems
// that cannot import app because of cycles (exceptions, auth, csrf,
// scheduler, grpc, ...) call them directly.
//
// Vocabulary is final for the 1.0 surface:
//
//	"production", "prod", "staging" -> IsProductionEnv == true
//	"development", "dev", "test", "testing", "local", ""
//	                                -> IsProductionEnv == false
//	"test", "testing"               -> IsTestingEnv == true
//	"development", "dev", "test",
//	"testing", "local"              -> IsDevOrTestEnv == true
//
// Anything outside the recognised set is treated as production
// (fail-secure): a typo in APP_ENV should never silently disable a
// production gate.

// EnvVar is the canonical environment-variable name for the application
// environment selector. Every direct read of the env var in framework
// code must go through GetEnv (or app.Env, which delegates here) so a
// rename can land in exactly one place.
const EnvVar = "APP_ENV"

// GetEnv returns the normalised value of the APP_ENV environment
// variable: lowercased, surrounding whitespace trimmed. An empty result
// distinguishes "unset" from "explicitly set to production".
//
// This is the single canonical reader. Callers MUST NOT call
// os.Getenv("APP_ENV") directly; route through this helper (or its
// app.Env() re-export) so vocabulary, casing, and trim semantics stay
// consistent across the framework.
func GetEnv() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv(EnvVar)))
}

// NonProdEnvNames returns the canonical list of APP_ENV values that
// IsDevOrTestEnv recognises as non-production. Exported so error
// messages, validator hints, and documentation can quote the
// vocabulary from a single source instead of drifting copies. Order
// matches the declaration in IsDevOrTestEnv; do not depend on it
// beyond display.
func NonProdEnvNames() []string {
	return []string{"development", "dev", "test", "testing", "local"}
}

// IsProductionEnv reports whether env names a production-class
// environment. Inputs are normalised (lowercase + trim) before
// comparison.
func IsProductionEnv(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "development", "dev", "test", "testing", "local", "":
		return false
	default:
		// production, prod, staging, or anything unknown.
		return true
	}
}

// IsTestingEnv reports whether env names a test environment.
func IsTestingEnv(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "test", "testing":
		return true
	default:
		return false
	}
}

// IsDevelopmentEnv reports whether env specifically names a development
// environment. Distinct from IsDevOrTestEnv because some callers want
// to gate dev-only warnings without including test profiles.
func IsDevelopmentEnv(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "development", "dev":
		return true
	default:
		return false
	}
}

// IsDevOrTestEnv reports whether env names a development or test
// profile. Used by config validators that relax Secure=false /
// HttpOnly=false / unsigned-payload defaults only when the operator has
// explicitly opted into a non-production environment.
func IsDevOrTestEnv(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "development", "dev", "test", "testing", "local":
		return true
	default:
		return false
	}
}
