package console

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/router"
)

// RouteListJSON writes all registered routes to w as a JSON array of
// {method, path, handler, middleware, name} objects - a stable contract for
// machine consumers, unlike the human table.
func RouteListJSON(r *router.VelocityRouterV2, w io.Writer) error {
	routes := r.AllRoutes()
	if routes == nil {
		routes = []router.RouteInfo{}
	}
	out, err := json.MarshalIndent(routes, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal routes: %w", err)
	}
	_, err = fmt.Fprintln(w, string(out))
	return err
}

// RouteList displays all registered routes.
func RouteList(r *router.VelocityRouterV2) error {
	routes := r.AllRoutes()

	if len(routes) == 0 {
		prism.Info("No routes registered.")
		return nil
	}

	headers := []string{"Method", "Path", "Name"}
	var rows [][]string
	for _, route := range routes {
		rows = append(rows, []string{route.Method, route.Path, route.Name})
	}

	prism.Table(headers, rows)
	return nil
}
