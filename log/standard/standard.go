// Package standard aggregates every built-in log driver so a single
// blank-import wires the full set of factories into the log registry.
//
//	import _ "github.com/velocitykode/velocity/log/standard"
//
// Importing this package registers the light root drivers (console, null)
// plus the leaf drivers (file, daily, stack) and their transitive
// dependencies (the async goroutine helper behind the file logger).
// Applications that want a smaller dependency footprint should blank-import
// only the specific leaves they need instead.
//
// This aggregator MUST NOT be imported by core, router, or app packages:
// doing so would re-attach the leaf driver dependencies to the framework
// core that the per-leaf split was made to shed. The console default keeps
// zero-config apps working without it.
package standard

import (
	// Light root drivers (console, null) self-register from log's init.
	_ "github.com/velocitykode/velocity/log"
	// Leaf drivers: file/daily.
	_ "github.com/velocitykode/velocity/log/file"
	// Leaf driver: stack.
	_ "github.com/velocitykode/velocity/log/stack"
)
