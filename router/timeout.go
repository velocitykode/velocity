package router

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Timeout returns a middleware that cancels the request context after the
// given duration. If the handler does not finish before the deadline, a
// 503 Service Unavailable response is written. The middleware always waits
// for the handler goroutine to finish to avoid races with context pooling.
func Timeout(duration time.Duration) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			ctx, cancel := context.WithTimeout(c.Request.Context(), duration)
			defer cancel()
			c.Request = c.Request.WithContext(ctx)

			done := make(chan error, 1)
			var once sync.Once

			go func() {
				done <- next(c)
			}()

			select {
			case err := <-done:
				return err
			case <-ctx.Done():
				var writeErr error
				once.Do(func() {
					writeErr = c.String(http.StatusServiceUnavailable, "Service Unavailable")
				})
				// Wait for the handler goroutine to finish so the context
				// can be safely returned to the pool after this returns.
				<-done
				return writeErr
			}
		}
	}
}
