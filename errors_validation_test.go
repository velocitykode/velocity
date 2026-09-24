package velocity

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
	"github.com/velocitykode/velocity/validation/vform"
	"github.com/velocitykode/velocity/view"
)

// backToSignup is a contract.ViewEngine whose Back answers 303 /signup,
// standing in for the view engine's redirect back.
type backToSignup struct{}

func (backToSignup) Back(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/signup", http.StatusSeeOther)
}

// signupForm is the rule set every validation entry point below checks.
type signupForm struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (signupForm) Rules() validation.Rules {
	return validation.Rules{
		"email":    {validation.Required(), validation.Email()},
		"password": {validation.Required(), validation.Min(8)},
	}
}

// invalidSignup is a body that fails both fields.
const invalidSignup = `{"email":"bad","password":"x"}`

// signupFailure validates invalidSignup by hand, the way a handler builds
// a Failure itself.
func signupFailure(t *testing.T) *validation.Failure {
	t.Helper()
	result, err := validation.CheckData(map[string]interface{}{"email": "bad", "password": "x"}, signupForm{}.Rules())
	if err != nil {
		t.Fatalf("CheckData: %v", err)
	}
	return validation.NewFailure(result)
}

// validationApp builds a pipeline app with debug off, a flash encryptor
// and, when view is non-nil, a view engine, with one route per validation
// entry point.
func validationApp(t *testing.T, view contract.ViewEngine) (*App, *levelLogger, *recordingReporter, crypto.Encryptor) {
	t.Helper()
	a, logs, rec := newPipelineApp(t)
	a.Services.Errors.SetDebug(false)
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	a.Services.Crypto = enc
	a.Services.View = view

	a.Router.Post("/validate", func(c *router.Context) error {
		return c.Validate(signupForm{}.Rules())
	})
	a.Router.Post("/vform", func(c *router.Context) error {
		_, err := vform.Form[signupForm](c)
		return err
	})
	a.Router.Post("/bindvalid", func(c *router.Context) error {
		var form signupForm
		return c.BindValid(&form)
	})
	a.Router.Post("/manual", func(*router.Context) error {
		return signupFailure(t)
	})
	a.Router.Post("/manual-redirect", func(*router.Context) error {
		f := signupFailure(t)
		f.RedirectTo = "/elsewhere"
		return f
	})
	a.Router.Post("/manual-unsafe-redirect", func(*router.Context) error {
		f := signupFailure(t)
		f.RedirectTo = "https://evil.example/phish"
		return f
	})
	a.Router.Post("/manual-bag", func(*router.Context) error {
		f := signupFailure(t)
		f.Bag = "login"
		return f
	})
	return a, logs, rec, enc
}

// postSignup posts invalidSignup to path with headers.
func postSignup(a *App, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(invalidSignup))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	a.Router.ServeHTTP(w, req)
	return w
}

// problemDoc is the part of a problem details body the tests read.
type problemDoc struct {
	Type   string              `json:"type"`
	Title  string              `json:"title"`
	Status int                 `json:"status"`
	Detail string              `json:"detail"`
	Errors map[string][]string `json:"errors"`
}

// assertValidationProblem asserts a 422 problem+json answer with errors on
// email and password and no flash or redirect.
func assertValidationProblem(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %q)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != problem.ProblemTypeContent {
		t.Errorf("Content-Type = %q, want %q", ct, problem.ProblemTypeContent)
	}
	var doc problemDoc
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, w.Body.String())
	}
	if doc.Status != http.StatusUnprocessableEntity || doc.Title != "Unprocessable Entity" {
		t.Errorf("problem = %+v, want status 422 titled Unprocessable Entity", doc)
	}
	if len(doc.Errors["email"]) == 0 || len(doc.Errors["password"]) == 0 {
		t.Errorf("errors = %v, want messages keyed by email and password", doc.Errors)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("Location = %q, want none", loc)
	}
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("Set-Cookie = %v, want none", cookies)
	}
}

// assertFlashRedirect asserts the browser answer: 303 to location with the
// errors and old input flash cookies (Path=/, HttpOnly, SameSite=Lax,
// Max-Age=300), old input redacted. It returns the decrypted errors.
func assertFlashRedirect(t *testing.T, w *httptest.ResponseRecorder, enc crypto.Encryptor, location string) map[string]any {
	t.Helper()
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != location {
		t.Errorf("Location = %q, want %q", got, location)
	}
	cookies := map[string]*http.Cookie{}
	for _, c := range w.Result().Cookies() {
		cookies[c.Name] = c
	}
	for _, name := range []string{router.FlashErrorsCookie, router.FlashInputCookie} {
		c := cookies[name]
		if c == nil {
			t.Fatalf("cookie %s missing (got %v)", name, w.Result().Cookies())
		}
		if c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 300 {
			t.Errorf("cookie %s = path %q httponly %v samesite %v maxage %d; want / true Lax 300",
				name, c.Path, c.HttpOnly, c.SameSite, c.MaxAge)
		}
	}
	errs, err := router.OpenFlash(enc, router.FlashErrorsCookie, cookies[router.FlashErrorsCookie].Value)
	if err != nil {
		t.Fatalf("open errors cookie: %v", err)
	}
	old, err := router.OpenFlash(enc, router.FlashInputCookie, cookies[router.FlashInputCookie].Value)
	if err != nil {
		t.Fatalf("open old input cookie: %v", err)
	}
	oldMap, _ := old.(map[string]any)
	if oldMap["email"] != "bad" {
		t.Errorf("old email = %v, want bad", oldMap["email"])
	}
	if _, leaked := oldMap["password"]; leaked {
		t.Error("old input carries the password")
	}
	errMap, _ := errs.(map[string]any)
	return errMap
}

// TestValidationFailure_Wire drives every validation entry point (the
// ctx.Validate callback, vform.Form, ctx.BindValid, a Failure returned by
// hand) through the real app for each kind of client, and asserts the wire
// answer: 422 problem+json for a JSON client or an app with no view
// engine, the flash-and-redirect flow for a browser with a view engine. A
// validation failure is never reported.
func TestValidationFailure_Wire(t *testing.T) {
	jsonClient := map[string]string{"Accept": "application/json"}
	browser := map[string]string{"Accept": "text/html,application/xhtml+xml"}
	inertia := map[string]string{"X-Inertia": "true", "Accept": "text/html"}

	tests := []struct {
		name     string
		path     string
		headers  map[string]string
		noView   bool
		location string // empty: expect the problem+json answer
	}{
		{name: "validate json client", path: "/validate", headers: jsonClient},
		{name: "validate browser", path: "/validate", headers: browser, location: "/signup"},
		{name: "validate browser no view engine", path: "/validate", headers: browser, noView: true},
		{name: "validate inertia", path: "/validate", headers: inertia, location: "/signup"},
		{name: "vform json client", path: "/vform", headers: jsonClient},
		{name: "vform browser", path: "/vform", headers: browser, location: "/signup"},
		{name: "vform browser no view engine", path: "/vform", headers: browser, noView: true},
		{name: "bindvalid json client", path: "/bindvalid", headers: jsonClient},
		{name: "bindvalid browser", path: "/bindvalid", headers: browser, location: "/signup"},
		{name: "bindvalid browser no view engine", path: "/bindvalid", headers: browser, noView: true},
		{name: "bindvalid inertia", path: "/bindvalid", headers: inertia, location: "/signup"},
		{name: "manual json client", path: "/manual", headers: jsonClient},
		{name: "manual browser", path: "/manual", headers: browser, location: "/signup"},
		{name: "manual browser no view engine", path: "/manual", headers: browser, noView: true},
		{name: "manual inertia", path: "/manual", headers: inertia, location: "/signup"},
		{name: "manual redirect target", path: "/manual-redirect", headers: browser, location: "/elsewhere"},
		{name: "manual unsafe redirect target goes back", path: "/manual-unsafe-redirect", headers: browser, location: "/signup"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var view contract.ViewEngine = backToSignup{}
			if tt.noView {
				view = nil
			}
			a, logs, rec, enc := validationApp(t, view)

			w := postSignup(a, tt.path, tt.headers)

			if tt.location == "" {
				assertValidationProblem(t, w)
			} else {
				errs := assertFlashRedirect(t, w, enc, tt.location)
				if errs["email"] == nil || errs["password"] == nil {
					t.Errorf("flashed errors = %v, want email and password", errs)
				}
			}
			if rec.count() != 0 {
				t.Errorf("reports = %d, want 0", rec.count())
			}
			if n := logs.count("error"); n != 0 {
				t.Errorf("error log entries = %d, want 0", n)
			}
		})
	}
}

// TestValidationFailure_ErrorBag asserts a Failure naming a bag flashes its
// errors as the error bag envelope: the bag's name and field -> first
// message.
func TestValidationFailure_ErrorBag(t *testing.T) {
	a, _, _, enc := validationApp(t, backToSignup{})
	w := postSignup(a, "/manual-bag", map[string]string{"Accept": "text/html"})

	errs := assertFlashRedirect(t, w, enc, "/signup")
	if errs[router.FlashErrorBagKey] != "login" {
		t.Errorf("flashed bag = %v, want login (%v)", errs[router.FlashErrorBagKey], errs)
	}
	messages, _ := errs[router.FlashBaggedErrorsKey].(map[string]any)
	for _, field := range []string{"email", "password"} {
		if msg, ok := messages[field].(string); !ok || msg == "" {
			t.Errorf("flashed %s = %v, want its first message (%v)", field, messages[field], errs)
		}
	}
}

// TestValidationFailure_UserRulesSeeEveryEntryPoint asserts the framework
// default stays overridable for every validation entry point (the
// ctx.Validate callback, vform.Form, ctx.BindValid, a Failure returned by
// hand): a user render rule for *validation.Failure answers first, and a
// user map rule that gives the failure another status keeps the browser
// flash-and-redirect from running.
func TestValidationFailure_UserRulesSeeEveryEntryPoint(t *testing.T) {
	rules := []struct {
		name       string
		configure  func(h contract.ErrorHandler)
		wantStatus int
		wantBody   string
	}{
		{
			name: "render rule",
			configure: func(h contract.ErrorHandler) {
				problem.RenderFor(h, func(rc contract.RenderContext, f *validation.Failure, _ *contract.ErrorContext) bool {
					rc.WriteHeader(http.StatusTeapot)
					_, _ = rc.Write([]byte(strings.Join([]string{"custom", f.Errors()["email"][0]}, ": ")))
					return true
				})
			},
			wantStatus: http.StatusTeapot,
			wantBody:   "custom: ",
		},
		{
			name: "map rule",
			configure: func(h contract.ErrorHandler) {
				problem.MapFor(h, func(f *validation.Failure) error {
					return contract.NewHTTPError(http.StatusBadRequest).WithCause(f)
				})
			},
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, rule := range rules {
		for _, path := range []string{"/validate", "/vform", "/bindvalid", "/manual"} {
			t.Run(rule.name+" "+path, func(t *testing.T) {
				a, _, _, _ := validationApp(t, backToSignup{})
				rule.configure(a.Services.Errors)

				w := postSignup(a, path, map[string]string{"Accept": "text/html"})
				if w.Code != rule.wantStatus || !strings.HasPrefix(w.Body.String(), rule.wantBody) {
					t.Fatalf("response = %d %q, want the user rule's %d", w.Code, w.Body.String(), rule.wantStatus)
				}
				if loc := w.Header().Get("Location"); loc != "" {
					t.Errorf("Location = %q, want none", loc)
				}
				if cookies := w.Result().Cookies(); len(cookies) != 0 {
					t.Errorf("Set-Cookie = %v, want none", cookies)
				}
			})
		}
	}
}

// TestValidationFailure_BrowserFlashIdenticalAcrossEntryPoints asserts
// every validation entry point gives a browser the same answer through the
// framework render rule: 303 back, both flash cookies with the same
// attributes, and the same sealed errors and old input.
func TestValidationFailure_BrowserFlashIdenticalAcrossEntryPoints(t *testing.T) {
	type answer struct {
		status   int
		location string
		errs     any
		old      any
	}
	answers := map[string]answer{}
	for _, path := range []string{"/manual", "/validate", "/vform", "/bindvalid"} {
		a, _, _, enc := validationApp(t, backToSignup{})
		w := postSignup(a, path, map[string]string{"Accept": "text/html"})
		assertFlashRedirect(t, w, enc, "/signup")
		got := answer{status: w.Code, location: w.Header().Get("Location")}
		for _, c := range w.Result().Cookies() {
			value, err := router.OpenFlash(enc, c.Name, c.Value)
			if err != nil {
				t.Fatalf("%s: open %s: %v", path, c.Name, err)
			}
			switch c.Name {
			case router.FlashErrorsCookie:
				got.errs = value
			case router.FlashInputCookie:
				got.old = value
			}
		}
		answers[path] = got
	}
	want := answers["/manual"]
	for path, got := range answers {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s answered %+v, want %+v (the /manual answer)", path, got, want)
		}
	}
}

// jsonMarker is a JSON renderer that stamps its output, to prove which
// renderer answered.
type jsonMarker struct{}

func (jsonMarker) ContentType() string { return "application/json" }

func (jsonMarker) Render(rc contract.RenderContext, err error, _ *contract.ErrorContext, status int, _ bool) error {
	rc.SetHeader("Content-Type", "application/json")
	rc.WriteHeader(status)
	_, werr := rc.Write([]byte(`{"marker":true}`))
	return werr
}

// TestInstallValidationErrorRules asserts the rule on a bare handler with
// no router services (so no view engine): every client gets problem+json,
// through the configured JSON renderer and BeforeRender hooks when set,
// and an error that is not a Failure is left to the rest of the pipeline.
func TestInstallValidationErrorRules(t *testing.T) {
	tests := []struct {
		name       string
		accept     string
		err        func(t *testing.T) error
		configure  func(h *problem.Handler)
		wantStatus int
		wantCT     string
		wantBody   string
		wantHeader string
	}{
		{
			name:       "browser without view engine",
			accept:     "text/html",
			err:        func(t *testing.T) error { return signupFailure(t) },
			wantStatus: http.StatusUnprocessableEntity,
			wantCT:     problem.ProblemTypeContent,
			wantBody:   `"errors":{"email"`,
		},
		{
			name:       "json client",
			accept:     "application/json",
			err:        func(t *testing.T) error { return signupFailure(t) },
			wantStatus: http.StatusUnprocessableEntity,
			wantCT:     problem.ProblemTypeContent,
			wantBody:   `"errors":{"email"`,
		},
		{
			name:   "custom status",
			accept: "text/html",
			err: func(t *testing.T) error {
				f := signupFailure(t)
				f.Status = http.StatusBadRequest
				return f
			},
			wantStatus: http.StatusBadRequest,
			wantCT:     problem.ProblemTypeContent,
			wantBody:   `"errors":{"email"`,
		},
		{
			name:   "configured json renderer and hooks",
			accept: "text/html",
			err:    func(t *testing.T) error { return signupFailure(t) },
			configure: func(h *problem.Handler) {
				h.AddRenderer("json", jsonMarker{})
				h.BeforeRender(func(rc contract.RenderContext, _ error, status int) int {
					rc.SetHeader("X-Hook", "ran")
					return status
				})
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCT:     "application/json",
			wantBody:   `{"marker":true}`,
			wantHeader: "ran",
		},
		{
			name:       "not a failure",
			accept:     "text/html",
			err:        func(*testing.T) error { return problem.NotFound() },
			wantStatus: http.StatusNotFound,
			wantCT:     "text/html; charset=utf-8",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recordingReporter{}
			h := problem.NewHandler(problem.WithReporters(rec))
			installValidationErrorRules(h)
			if tt.configure != nil {
				tt.configure(h)
			}

			req := httptest.NewRequest(http.MethodPost, "/signup", nil)
			req.Header.Set("Accept", tt.accept)
			w := httptest.NewRecorder()
			h.HandleRequest(contract.NewRenderContext(w, req), tt.err(t), nil)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); ct != tt.wantCT {
				t.Errorf("Content-Type = %q, want %q", ct, tt.wantCT)
			}
			if !strings.Contains(w.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", w.Body.String(), tt.wantBody)
			}
			if got := w.Header().Get("X-Hook"); got != tt.wantHeader {
				t.Errorf("X-Hook = %q, want %q", got, tt.wantHeader)
			}
			if rec.count() != 0 {
				t.Errorf("reports = %d, want 0", rec.count())
			}
		})
	}
}

// TestValidateCallback_ReturnsFailure asserts what ctx.Validate hands the
// handler: the Failure itself, with nothing written, for every client; the
// error pipeline answers it once the handler returns.
func TestValidateCallback_ReturnsFailure(t *testing.T) {
	tests := []struct {
		name   string
		view   contract.ViewEngine
		accept string
	}{
		{name: "json client", view: backToSignup{}, accept: "application/json"},
		{name: "no view engine", accept: "text/html"},
		{name: "browser with view engine", view: backToSignup{}, accept: "text/html"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, _, _ := validationApp(t, tt.view)
			var (
				got     error
				written bool
			)
			a.Router.Post("/probe", func(c *router.Context) error {
				got = c.Validate(signupForm{}.Rules())
				written = c.RenderContext().Written() || len(c.Response.Header().Values("Set-Cookie")) != 0
				return got
			})
			postSignup(a, "/probe", map[string]string{"Accept": tt.accept})

			var f *validation.Failure
			if !errors.As(got, &f) {
				t.Fatalf("error = %v, want a *validation.Failure", got)
			}
			if errors.Is(got, contract.ErrResponseWritten) {
				t.Errorf("error = %v, want no response-written marker", got)
			}
			if written {
				t.Error("ctx.Validate wrote to the response")
			}
		})
	}
}

// TestErrorPipeline_OneJSONAnswer asserts the validation entry points and
// the auth render rule answer JSON exactly when the handler's negotiation
// does: API prefixes turn a browser request into problem+json, and a
// JSONWhen predicate that says no sends a JSON client down the browser
// flow.
func TestErrorPipeline_OneJSONAnswer(t *testing.T) {
	jsonClient := map[string]string{"Accept": "application/json", "X-Requested-With": "XMLHttpRequest"}
	browser := map[string]string{"Accept": "text/html"}
	tests := []struct {
		name         string
		configure    func(h contract.ErrorHandler)
		path         string
		headers      map[string]string
		wantStatus   int
		wantLocation string
	}{
		{name: "api prefix validate", configure: apiPrefix, path: "/api/validate", headers: browser, wantStatus: http.StatusUnprocessableEntity},
		{name: "api prefix vform", configure: apiPrefix, path: "/api/vform", headers: browser, wantStatus: http.StatusUnprocessableEntity},
		{name: "api prefix bindvalid", configure: apiPrefix, path: "/api/bindvalid", headers: browser, wantStatus: http.StatusUnprocessableEntity},
		{name: "api prefix manual failure", configure: apiPrefix, path: "/api/manual", headers: browser, wantStatus: http.StatusUnprocessableEntity},
		{name: "api prefix unauthenticated", configure: apiPrefix, path: "/api/unauth", headers: browser, wantStatus: http.StatusUnauthorized},
		{name: "json when no validate", configure: neverJSON, path: "/validate", headers: jsonClient, wantStatus: http.StatusSeeOther, wantLocation: "/signup"},
		{name: "json when no vform", configure: neverJSON, path: "/vform", headers: jsonClient, wantStatus: http.StatusSeeOther, wantLocation: "/signup"},
		{name: "json when no bindvalid", configure: neverJSON, path: "/bindvalid", headers: jsonClient, wantStatus: http.StatusSeeOther, wantLocation: "/signup"},
		{name: "json when no manual failure", configure: neverJSON, path: "/manual", headers: jsonClient, wantStatus: http.StatusSeeOther, wantLocation: "/signup"},
		{name: "json when no unauthenticated", configure: neverJSON, path: "/unauth", headers: jsonClient, wantStatus: http.StatusSeeOther, wantLocation: "/login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, rec, _ := validationApp(t, backToSignup{})
			tt.configure(a.Services.Errors)
			a.Router.Post("/api/validate", func(c *router.Context) error {
				return c.Validate(signupForm{}.Rules())
			})
			a.Router.Post("/api/vform", func(c *router.Context) error {
				_, err := vform.Form[signupForm](c)
				return err
			})
			a.Router.Post("/api/bindvalid", func(c *router.Context) error {
				var form signupForm
				return c.BindValid(&form)
			})
			a.Router.Post("/api/manual", func(*router.Context) error { return signupFailure(t) })
			unauth := func(*router.Context) error { return &auth.UnauthenticatedError{} }
			a.Router.Post("/api/unauth", unauth)
			a.Router.Post("/unauth", unauth)

			w := postSignup(a, tt.path, tt.headers)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			if got := w.Header().Get("Location"); got != tt.wantLocation {
				t.Errorf("Location = %q, want %q", got, tt.wantLocation)
			}
			if tt.wantLocation == "" && w.Header().Get("Content-Type") != problem.ProblemTypeContent {
				t.Errorf("Content-Type = %q, want %q", w.Header().Get("Content-Type"), problem.ProblemTypeContent)
			}
			if rec.count() != 0 {
				t.Errorf("reports = %d, want 0", rec.count())
			}
		})
	}
}

// apiPrefix answers every /api request with JSON.
func apiPrefix(h contract.ErrorHandler) { h.SetAPIPrefixes("/api") }

// neverJSON answers no request with JSON.
func neverJSON(h contract.ErrorHandler) {
	h.JSONWhen(func(*http.Request, error) bool { return false })
}

// TestValidationFailure_ErrorBagReachesInertiaProps drives a Failure
// naming a bag through the real write and read paths: the browser POST is
// flashed and redirected, and the next Inertia visit renders the errors at
// props.errors (field -> first message) and under props.errors.{bag}.
func TestValidationFailure_ErrorBagReachesInertiaProps(t *testing.T) {
	a := newInertiaApp(t, "", false)
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	a.Services.Crypto = enc
	engine, ok := a.Services.View.(*view.Engine)
	if !ok {
		t.Fatal("view engine not built")
	}
	a.Router.Post("/signup", func(*router.Context) error {
		f := signupFailure(t)
		f.Bag = "login"
		f.RedirectTo = "/form"
		return f
	})
	a.Router.Get("/form", func(c *router.Context) error {
		return engine.Render(c.Response, c.Request, "Form")
	})

	w := postSignup(a, "/signup", map[string]string{"Accept": "text/html"})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/form" {
		t.Fatalf("POST = %d %q, want 303 /form (body %q)", w.Code, w.Header().Get("Location"), w.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/form", nil)
	req.Header.Set("X-Inertia", "true")
	req.Header.Set("X-Inertia-Version", "v1")
	for _, c := range w.Result().Cookies() {
		req.AddCookie(c)
	}
	w = httptest.NewRecorder()
	a.Router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	page := inertiaPage(t, w, true)
	props, _ := page["props"].(map[string]any)
	errs, _ := props["errors"].(map[string]any)
	bag, _ := errs["login"].(map[string]any)
	for _, field := range []string{"email", "password"} {
		top, ok := errs[field].(string)
		if !ok || top == "" {
			t.Errorf("props.errors.%s = %v, want its first message (%v)", field, errs[field], errs)
		}
		if bag[field] != top {
			t.Errorf("props.errors.login.%s = %v, want %q", field, bag[field], top)
		}
	}
}
