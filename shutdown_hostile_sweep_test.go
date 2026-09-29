package velocity

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/internal/hostile"
)

// hostileModule's Shutdown runs its code.
type hostileModule struct{ code *hostile.Code }

func (hostileModule) Init(*app.Services) error  { return nil }
func (hostileModule) Start(*app.Services) error { return nil }
func (m hostileModule) Shutdown(context.Context) error {
	m.code.Run()
	return nil
}

// hostileComponent is a registry component whose Shutdown runs its code.
type hostileComponent struct{ code *hostile.Code }

func (c *hostileComponent) Shutdown(context.Context) error {
	c.code.Run()
	return nil
}

// hostileView is a view engine whose Shutdown runs its code.
type hostileView struct{ code *hostile.Code }

func (hostileView) Back(http.ResponseWriter, *http.Request) {}
func (v hostileView) Shutdown(context.Context) error {
	v.code.Run()
	return nil
}

// App.Shutdown survives a module, registry component or view engine whose
// Shutdown panics or calls App.Shutdown again: no panic escapes, a panic
// becomes the step's error, and the steps around it still run.
func TestAppShutdown_HostileUserCodeSweep(t *testing.T) {
	for _, site := range []string{"module", "component", "view"} {
		for _, mode := range []hostile.Mode{hostile.Panic, hostile.Reenter} {
			t.Run(site+"/"+mode.String(), func(t *testing.T) {
				rec := &shutdownRecorder{}
				var a *App
				code := hostile.New(t, mode, func() {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					_ = a.Shutdown(ctx)
				})
				mods := []Module{rec}
				if site == "module" {
					mods = append(mods, hostileModule{code: code})
				}
				var err error
				a, err = NewTestApp(WithModules(mods...))
				if err != nil {
					t.Fatalf("NewTestApp: %v", err)
				}
				if site == "component" {
					if err := app.Register(a.Services, &hostileComponent{code: code}); err != nil {
						t.Fatal(err)
					}
				}
				probe := &viewShutdownProbe{}
				a.Services.View = probe
				if site == "view" {
					a.Services.View = hostileView{code: code}
				}

				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				var shutdownErr error
				if p := hostile.Within(t, hostile.Deadline, func() { shutdownErr = a.Shutdown(ctx) }); p != nil {
					t.Fatalf("App.Shutdown panicked: %v", p)
				}
				if code.Calls() == 0 {
					t.Fatal("the hostile Shutdown never ran")
				}
				if mode == hostile.Panic && (shutdownErr == nil || !strings.Contains(shutdownErr.Error(), hostile.PanicValue)) {
					t.Errorf("App.Shutdown = %v, want the panic as an error", shutdownErr)
				}
				if rec.shutdowns.Load() == 0 {
					t.Error("the module registered first was not shut down")
				}
				if site != "view" && probe.shutdowns.Load() == 0 {
					t.Error("the view engine, a later step, was not shut down")
				}
			})
		}
	}
}
