package router

import (
	"os"
)

// init initializes the router package
func init() {
	// Create global router instance
	_ = Get()

	// Check for debug mode
	if os.Getenv("ROUTE_DEBUG") == "true" {
		// Could add debug logging here
	}
}
