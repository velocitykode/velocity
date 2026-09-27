package velocity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// A logger that replaces Services.Log after New receives the lines the
// router's default error path and the bridge's no-handler fallback write,
// at the level each writes, and the logger New started with receives none.
func TestAppLogger_WritesToLoggerSwappedAfterNew(t *testing.T) {
	tests := []struct {
		name  string
		setup func(a *App)
		err   error
		level string
		msg   string
	}{
		{
			name:  "router default server error",
			setup: func(a *App) { a.Router.SetErrorHandler(nil) },
			err:   errors.New("db down"),
			level: "error",
			msg:   "unhandled error in HTTP handler",
		},
		{
			name:  "router default deadline",
			setup: func(a *App) { a.Router.SetErrorHandler(nil) },
			err:   context.DeadlineExceeded,
			level: "warn",
			msg:   "unhandled error in HTTP handler",
		},
		{
			name:  "bridge without error handler",
			setup: func(a *App) { a.Services.Errors = nil },
			err:   errors.New("boom"),
			level: "error",
			msg:   "unhandled error in HTTP handler",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, original, _ := newPipelineApp(t)
			tt.setup(a)
			swapped := &levelLogger{}
			a.Services.Log = swapped
			a.Router.Get("/t", func(*router.Context) error { return tt.err })

			a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/t", nil))

			if got := entriesWith(swapped, tt.level, tt.msg); got != 1 {
				t.Errorf("swapped logger %s entries %q = %d, want 1 (%+v)", tt.level, tt.msg, got, swapped.entries)
			}
			if got := entriesWith(original, "", tt.msg); got != 0 {
				t.Errorf("original logger got %d entries %q, want 0", got, tt.msg)
			}
		})
	}
}
