// Package chain holds the declarative bootstrap types wired into *velocity.App
// by the Modules, Middleware, Routes, and Commands chain methods. The types
// live here — rather than in the root velocity package — because the root
// router depends on app.Services, and putting these types alongside it would
// create an import cycle with the callback signatures consumer code writes.
//
// # Not the canonical import path
//
// Application code should not import chain/ directly. The same types are
// re-exported from the root velocity package as type aliases, so consumers
// write *velocity.Routing, *velocity.MiddlewareStack, velocity.RouteModule,
// and so on — no chain import required. The velocity.X names and the
// chain.X names resolve to the same Go type; framework-internal code uses
// chain.X, application code uses velocity.X.
//
// Import chain/ directly only when writing framework internals or a
// third-party module that needs to embed or reference these
// types outside the velocity package.
package chain
