// Package standard aggregates every built-in storage driver so a single
// blank-import wires the full set of factories into the storage registry.
//
//	import _ "github.com/velocitykode/velocity/storage/standard"
//
// Importing this package registers the light root drivers (local, memory)
// plus the heavy leaf drivers (s3) and their transitive dependencies (the
// AWS SDK). Applications that want a smaller dependency footprint should
// blank-import only the specific leaves they need instead.
//
// This aggregator MUST NOT be imported by core, router, or app packages:
// doing so would re-attach the heavy driver dependencies to the framework
// core that the per-leaf split was made to shed.
package standard

import (
	// Light root drivers (local, memory) self-register from storage's init.
	_ "github.com/velocitykode/velocity/storage"
	// Heavy leaf driver: AWS S3.
	_ "github.com/velocitykode/velocity/storage/s3"
)
