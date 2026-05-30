// Package standard aggregates every built-in queue driver so a single
// blank-import wires the full set of factories into the queue registry.
//
//	import _ "github.com/velocitykode/velocity/queue/standard"
//
// Importing this package registers the light root drivers (memory, database)
// plus the heavy leaf drivers (redis) and their transitive dependencies (the
// go-redis client). Applications that want a smaller dependency footprint
// should blank-import only the specific leaves they need instead.
//
// This aggregator MUST NOT be imported by core, router, or app packages:
// doing so would re-attach the heavy driver dependencies to the framework
// core that the per-leaf split was made to shed.
package standard

import (
	// Light root drivers (memory, database) self-register from queue's init.
	_ "github.com/velocitykode/velocity/queue"
	// Heavy leaf driver: Redis.
	_ "github.com/velocitykode/velocity/queue/redis"
)
