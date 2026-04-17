package router

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/velocitykode/velocity/internal/panicerr"
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

			// Recover from panics inside the handler so the select below
			// always observes a terminal value and the context pool entry
			// is never leaked. The converted error flows through the
			// normal error path.
			go func() {
				defer func() {
					if r := recover(); r != nil {
						done <- fmt.Errorf("velocity/router: timeout handler panic: %w", panicerr.FromRecovered(r))
					}
				}()
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
