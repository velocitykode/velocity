package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A handler error, or a handler panic value, whose Error, String or Format
// method panics, a nested panic included, is answered with the plain 500
// and logged once with the value as errchain.Unreadable: a panic out of
// formatting it for the log line does not escape the default path.
func TestBoundary_UnformattableErrorAndPanic(t *testing.T) {
	for name, v := range hostile.Unformattables() {
		for _, how := range []string{"return", "panic"} {
			t.Run(name+"/"+how, func(t *testing.T) {
				out := fallbacklogtest.Capture(t)
				r := NewV2()
				r.Get("/x", func(*Context) error {
					if how == "panic" {
						panic(v)
					}
					if err, ok := v.(error); ok {
						return err
					}
					panic(v)
				})
				w := httptest.NewRecorder()
				if p := hostile.Within(t, hostile.Deadline, func() {
					r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
				}); p != nil {
					t.Fatalf("the boundary panicked: %v", p)
				}
				if w.Code != http.StatusInternalServerError {
					t.Errorf("status = %d, want 500", w.Code)
				}
				log := out.String()
				if strings.Count(log, UnhandledErrorMessage) != 1 || !strings.Contains(log, errchain.Unreadable) {
					t.Errorf("want one unhandled-error line with the value as Unreadable:\n%s", log)
				}
				// A returned error is not a recovered panic: a panic out of
				// formatting it must not turn the line into a panic report.
				if _, isErr := v.(error); isErr && how == "return" && strings.Contains(log, "stack=") {
					t.Errorf("a returned error was logged as a recovered panic:\n%s", log)
				}
			})
		}
	}
}
