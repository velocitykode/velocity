package console

import (
	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/router"
)

// RouteList displays all registered routes.
func RouteList(r *router.VelocityRouterV2) error {
	routes := r.AllRoutes()

	if len(routes) == 0 {
		cli.Info("No routes registered.")
		return nil
	}

	headers := []string{"Method", "Path", "Name"}
	var rows [][]string
	for _, route := range routes {
		rows = append(rows, []string{route.Method, route.Path, route.Name})
	}

	cli.Table(headers, rows)
	return nil
}
