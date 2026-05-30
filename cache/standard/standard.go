// Package standard aggregates every built-in cache store so a single
// blank-import wires the full set of factories into the cache registry.
//
//	import _ "github.com/velocitykode/velocity/cache/standard"
//
// Importing this package registers the light root stores (memory, file) plus
// the heavy leaf stores (redis) and their transitive dependencies (the
// go-redis client). Applications that want a smaller dependency footprint
// should blank-import only the specific leaves they need instead.
//
// This aggregator MUST NOT be imported by core, router, or app packages:
// doing so would re-attach the heavy driver dependencies to the framework
// core that the per-leaf split was made to shed.
package standard

import (
	// Light root stores (memory, file) self-register from cache's init.
	_ "github.com/velocitykode/velocity/cache"
	// Heavy leaf store: Redis.
	_ "github.com/velocitykode/velocity/cache/redis"
)
