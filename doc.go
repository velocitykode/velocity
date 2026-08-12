// Package velocity is a batteries-included web framework for Go. It provides
// a dependency-injection container (*App), a radix-tree router, a generic ORM
// (Model[T]), driver-based cache/queue/log/mail/storage subsystems, an auth
// manager with schemes and access policies, an Inertia.js-compatible view layer, and a
// declarative bootstrap API for wiring it all together.
//
// # Canonical import path
//
// Application code should import only "github.com/velocitykode/velocity". The
// declarative bootstrap types (Routing, MiddlewareStack, ModuleRegistry,
// Commands, Services, Module, and the optional module interfaces) are
// re-exported from this package as type aliases; their implementation homes
// (chain/ and app/) are framework-internal concerns driven by import-cycle
// constraints, not public API. Sibling packages (prism, generated
// scaffolding, examples, templates) all use the velocity.X names.
//
// Framework-internal code and third-party modules that need to
// embed or implement these types may import chain/ or app/ directly —
// velocity.X and chain.X resolve to the same Go type, so either path works.
//
// # Getting started
//
// The minimum viable app:
//
//	func main() {
//	    v, err := velocity.New()
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//	    v.Routes(func(r *velocity.Routing) {
//	        r.Web(func(g velocity.Router) {
//	            g.Get("/", homeHandler)
//	        })
//	    }).Serve()
//	}
//
// See the velocity-template and velocity-template-api repositories for
// full-stack and API-only starters with conventional directory layouts.
package velocity
