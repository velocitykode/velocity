package router

// init initializes the router package
func init() {
	// Create global router instance
	_ = Get()
	// TODO: implement debug logging when ROUTE_DEBUG=true
}
