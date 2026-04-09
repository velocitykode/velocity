package console

import (
	"fmt"
	"strings"

	"github.com/velocitykode/velocity/router"
)

// RouteList displays all registered routes.
func RouteList(r *router.VelocityRouterV2) error {
	routes := r.AllRoutes()

	if len(routes) == 0 {
		fmt.Println("No routes registered.")
		return nil
	}

	// Calculate column widths
	methodWidth := len("Method")
	pathWidth := len("Path")
	for _, route := range routes {
		if len(route.Method) > methodWidth {
			methodWidth = len(route.Method)
		}
		if len(route.Path) > pathWidth {
			pathWidth = len(route.Path)
		}
	}
	methodWidth += 3
	pathWidth += 3

	// Header
	fmt.Println()
	fmt.Printf("  %-*s %-*s %s\n", methodWidth, "Method", pathWidth, "Path", "Name")
	fmt.Printf("  %s\n", strings.Repeat("─", methodWidth+pathWidth+20))

	// Routes
	for _, route := range routes {
		fmt.Printf("  %-*s %-*s %s\n", methodWidth, route.Method, pathWidth, route.Path, route.Name)
	}

	fmt.Printf("\n  Showing %d routes\n\n", len(routes))
	return nil
}
