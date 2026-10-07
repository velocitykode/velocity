package router

import "net/http"

// benchNoopListener is package-level so registering it allocates no closure.
var benchNoopListener = func(int, http.ResponseWriter) {}

// benchRegisterListeners registers n pre-commit listeners on c.
func benchRegisterListeners(c *Context, n int) {
	for i := 0; i < n; i++ {
		c.BeforeCommit(benchNoopListener)
	}
}
