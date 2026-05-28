package console

import "os"

// Default and secret-tier filesystem permission constants used by every
// generator and provider wirer in the console package. Centralising them
// here keeps the values consistent across the framework and ensures the
// security review can audit every disk-touching call site in one place.
//
// The "default" tier is appropriate for generated source files that are
// expected to be committed to a public repository. The "secret" tier is
// reserved for material that must never be readable by other local users
// (e.g. env files, API keys, maintenance markers).
const (
	defaultFileMode os.FileMode = 0644
	defaultDirMode  os.FileMode = 0755
	secretFileMode  os.FileMode = 0600
	secretDirMode   os.FileMode = 0700
)
