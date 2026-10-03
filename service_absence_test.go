package velocity_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/velocitykode/velocity"
	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth/stores/ormauth"
	"github.com/velocitykode/velocity/contract"
	cryptodrivers "github.com/velocitykode/velocity/crypto/drivers"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
	"github.com/velocitykode/velocity/velocitytest"
	"github.com/velocitykode/velocity/view"
)

// notOwned lists the Services fields that are not services: they carry
// configuration, not an instance a lookup can find missing.
var notOwned = map[string]bool{"RedirectAllowlist": true, "CookiePolicy": true, "FlashBag": true}

// absenceRow is one owned Services field: the name its missing-service
// error carries, a typed nil of a concrete type for the fields the test app
// leaves empty, and the lookups that must report it.
type absenceRow struct {
	field    string
	service  string
	typedNil any
	// lookups maps a lookup's name to a call that returns the error it
	// reports; the first one ("accessor") runs inside a handler whose
	// return reaches the error pipeline.
	lookups map[string]func(c *router.Context, s *app.Services) error
}

func accessor[T any](get func(*router.Context) (T, error)) map[string]func(*router.Context, *app.Services) error {
	return map[string]func(*router.Context, *app.Services) error{
		"accessor": func(c *router.Context, _ *app.Services) error { _, err := get(c); return err },
	}
}

func absenceRows() []absenceRow {
	db := accessor((*router.Context).DB)
	db["Validate with a DB rule"] = func(c *router.Context, _ *app.Services) error {
		return c.Validate(validation.Rules{"email": {validation.Unique("users", "email")}})
	}
	authLookups := accessor((*router.Context).Auth)
	authLookups["Authorize"] = func(c *router.Context, _ *app.Services) error { return c.Authorize("edit") }
	authLookups["Can"] = func(c *router.Context, _ *app.Services) error {
		if c.Can("edit") || !c.Cannot("edit") {
			return errors.New("granted an ability with auth missing")
		}
		_, err := c.Auth()
		return err
	}
	authLookups["SetAuthModel"] = func(_ *router.Context, s *app.Services) error { return velocity.SetAuthModel[ormauth.User](s) }
	viewLookups := accessor((*router.Context).View)
	viewLookups["view.Render"] = func(c *router.Context, _ *app.Services) error { return view.Render(c, "Page") }
	viewLookups["view.Redirect"] = func(c *router.Context, _ *app.Services) error { return view.Redirect(c, "/x") }
	viewLookups["view.Back"] = func(c *router.Context, _ *app.Services) error { return view.Back(c) }
	viewLookups["view.For"] = func(c *router.Context, _ *app.Services) error { _, err := view.For(c); return err }
	return []absenceRow{
		{field: "Log", service: "log"},
		{field: "Errors", service: "errors", lookups: accessor((*router.Context).Errors)},
		{field: "Crypto", service: "crypto", typedNil: (*cryptodrivers.AESDriver)(nil), lookups: accessor((*router.Context).Crypto)},
		{field: "DB", service: "database", typedNil: (*orm.Manager)(nil), lookups: db},
		{field: "Auth", service: "auth", lookups: authLookups},
		{field: "CSRF", service: "csrf", lookups: accessor((*router.Context).CSRF)},
		{field: "View", service: "view", typedNil: (*view.Engine)(nil), lookups: viewLookups},
		{field: "Cache", service: "cache", lookups: accessor((*router.Context).Cache)},
		{field: "Events", service: "events", lookups: accessor((*router.Context).Events)},
		{field: "Queue", service: "queue", lookups: accessor((*router.Context).Queue)},
		{field: "Storage", service: "storage", lookups: accessor((*router.Context).Storage)},
		{field: "Scheduler", service: "scheduler", lookups: accessor((*router.Context).Scheduler)},
		{field: "Mail", service: "mail", lookups: accessor((*router.Context).Mail)},
		{field: "Notification", service: "notification", lookups: accessor((*router.Context).Notification)},
	}
}

// TestOwnedServices_EveryFieldHasAnAbsenceRow is the enumerator: every
// exported Services field is either listed as not a service or has a row
// below, so a field added to Services without its missing-service lookups
// fails here.
func TestOwnedServices_EveryFieldHasAnAbsenceRow(t *testing.T) {
	rows := map[string]bool{}
	for _, r := range absenceRows() {
		rows[r.field] = true
	}
	st := reflect.TypeFor[app.Services]()
	for i := range st.NumField() {
		f := st.Field(i)
		if !f.IsExported() || notOwned[f.Name] {
			continue
		}
		if !rows[f.Name] {
			t.Errorf("Services.%s has no absence row", f.Name)
		}
		delete(rows, f.Name)
	}
	for name := range rows {
		t.Errorf("absence row %s names no Services field", name)
	}
}

type absenceReporter struct {
	mu   sync.Mutex
	errs []error
}

func (r *absenceReporter) Report(err error, _ *contract.ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

func (r *absenceReporter) reported() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

// wantNamed reports whether err is the missing-service error naming service.
func wantNamed(err error, service string) bool {
	var snc *contract.ServiceNotConfiguredError
	return errors.Is(err, contract.ErrServiceNotConfigured) && errors.As(err, &snc) && snc.Service == service
}

// TestOwnedServices_AbsentAndTypedNilReportServiceNotConfigured empties each
// owned field of a test app, to nil and to a typed nil, and asserts every
// lookup reports the missing-service error naming the service, never
// panicking, and that a handler returning it gets a 500 the error pipeline
// reports with the service named. Log never fails: it falls back.
func TestOwnedServices_AbsentAndTypedNilReportServiceNotConfigured(t *testing.T) {
	for _, row := range absenceRows() {
		for _, variant := range []string{"nil", "typed nil"} {
			t.Run(row.field+"/"+variant, func(t *testing.T) {
				a, err := velocitytest.NewApp()
				if err != nil {
					t.Fatalf("NewApp: %v", err)
				}
				rec := &absenceReporter{}
				a.Services.Errors.AddReporter(rec)

				field := reflect.ValueOf(a.Services).Elem().FieldByName(row.field)
				original := field.Interface()
				empty := reflect.Zero(field.Type())
				if variant == "typed nil" {
					sample := row.typedNil
					if sample == nil {
						sample = original
					}
					st := reflect.TypeOf(sample)
					if st == nil || st.Kind() != reflect.Pointer {
						t.Fatalf("Services.%s needs a pointer typed-nil sample, the app holds %T", row.field, original)
					}
					empty = reflect.Zero(st)
				}
				field.Set(empty)
				t.Cleanup(func() {
					if original == nil {
						field.Set(reflect.Zero(field.Type()))
					} else {
						field.Set(reflect.ValueOf(original))
					}
					_ = a.Shutdown(context.Background())
				})

				got := map[string]error{}
				var logger contract.Logger
				a.Router.Get("/probe", func(c *router.Context) error {
					logger = c.Log()
					for name, lookup := range row.lookups {
						got[name] = lookup(c, a.Services)
					}
					return got["accessor"]
				})
				w := httptest.NewRecorder()
				a.Router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))

				if logger == nil {
					t.Error("Log() = nil, want the fallback logger")
				}
				if row.lookups == nil {
					if w.Code != http.StatusOK {
						t.Errorf("status = %d, want 200: a missing logger fails nothing", w.Code)
					}
					return
				}
				for name, err := range got {
					if name == "Authorize" {
						var he *contract.HTTPError
						if !errors.As(err, &he) || he.Status != http.StatusForbidden {
							t.Errorf("Authorize = %v, want a 403", err)
						}
					}
					if !wantNamed(err, row.service) {
						t.Errorf("%s = %v, want the missing-service error naming %q", name, err, row.service)
					}
				}
				if w.Code != http.StatusInternalServerError {
					t.Errorf("status = %d, want 500", w.Code)
				}
				if row.field == "Errors" {
					return // no error handler is left to report through
				}
				reported := rec.reported()
				if len(reported) != 1 || !wantNamed(reported[0], row.service) {
					t.Errorf("reported %v, want one missing-service error naming %q", reported, row.service)
				}
			})
		}
	}
}

// TestRegistryLookup_UnregisteredComponentReported pins the registry side:
// a handler asking for an unregistered component gets the missing-service
// error naming the component key, and the pipeline reports it as a 500.
func TestRegistryLookup_UnregisteredComponentReported(t *testing.T) {
	type unregistered struct{}
	a, err := velocitytest.NewApp()
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	rec := &absenceReporter{}
	a.Services.Errors.AddReporter(rec)
	const key = "*velocity_test.unregistered"
	a.Router.Get("/probe", func(c *router.Context) error {
		_, err := router.Service[*unregistered](c)
		return err
	})
	w := httptest.NewRecorder()
	a.Router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if r := rec.reported(); len(r) != 1 || !wantNamed(r[0], key) {
		t.Errorf("reported %v, want one missing-service error naming %q", r, key)
	}
}
