package velocity

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/velocitykode/velocity/router"
)

// PreventRequestsDuringMaintenance returns middleware that checks for the
// .vel/down marker file (created by the "down" console command) and returns
// a 503 Service Unavailable response when the application is in maintenance mode.
func PreventRequestsDuringMaintenance() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			if isDownForMaintenance() {
				return c.JSON(http.StatusServiceUnavailable, map[string]string{
					"message": "Service Unavailable",
				})
			}
			return next(c)
		}
	}
}

// isDownForMaintenance checks whether the .vel/down marker file exists.
func isDownForMaintenance() bool {
	_, err := os.Stat(filepath.Join(".", ".vel", "down"))
	return err == nil
}
