// Package standard aggregates every built-in ORM driver so a single
// blank-import wires the full set of factories into the ORM registry.
//
//	import _ "github.com/velocitykode/velocity/orm/standard"
//
// Importing this package registers the light root default (pure-Go modernc
// SQLite) plus the heavy leaf drivers (postgres via lib/pq, mysql via
// go-sql-driver/mysql, and the cgo mattn SQLite backend) and their transitive
// dependencies. Because orm/sqlite is blank-imported here, building with cgo
// enabled swaps the SQLite default to the mattn backend; under CGO_ENABLED=0
// orm/sqlite is an empty no-op and the pure-Go modernc default stays in
// effect. Applications that want a smaller dependency footprint should
// blank-import only the specific leaves they need instead.
//
// This aggregator MUST NOT be imported by core, router, or app packages:
// doing so would re-attach the heavy driver dependencies to the framework
// core that the per-leaf split was made to shed.
package standard

import (
	// Light root default (modernc SQLite) self-registers from orm's init.
	_ "github.com/velocitykode/velocity/orm"
	// Heavy leaf drivers.
	_ "github.com/velocitykode/velocity/orm/mysql"
	_ "github.com/velocitykode/velocity/orm/postgres"
	_ "github.com/velocitykode/velocity/orm/sqlite"
)
