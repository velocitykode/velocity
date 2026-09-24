package velocity

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
	"github.com/velocitykode/velocity/validation/vform"
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
// ctx.Validate callback, vform.Form, a Failure returned by hand) through
// the real app for each kind of client, and asserts the wire answer: 422
// problem+json for a JSON client or an app with no view engine, the
// flash-and-redirect flow for a browser with a view engine. A validation
// failure is never reported.
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
// errors nested under the bag.
func TestValidationFailure_ErrorBag(t *testing.T) {
	a, _, _, enc := validationApp(t, backToSignup{})
	w := postSignup(a, "/manual-bag", map[string]string{"Accept": "text/html"})

	errs := assertFlashRedirect(t, w, enc, "/signup")
	bag, _ := errs["login"].(map[string]any)
	if bag["email"] == nil || bag["password"] == nil {
		t.Errorf("flashed errors = %v, want email and password under login", errs)
	}
	if errs["email"] != nil {
		t.Errorf("flashed errors = %v, want nothing at the top level", errs)
	}
}

// TestValidationFailure_UserRuleWins asserts the framework default stays
// overridable: a user render rule for *validation.Failure answers first.
func TestValidationFailure_UserRuleWins(t *testing.T) {
	a, _, _, _ := validationApp(t, backToSignup{})
	problem.RenderFor(a.Services.Errors, func(rc contract.RenderContext, f *validation.Failure, _ *contract.ErrorContext) bool {
		rc.WriteHeader(http.StatusTeapot)
		_, _ = rc.Write([]byte(strings.Join([]string{"custom", f.Errors()["email"][0]}, ": ")))
		return true
	})

	w := postSignup(a, "/manual", map[string]string{"Accept": "text/html"})
	if w.Code != http.StatusTeapot || !strings.HasPrefix(w.Body.String(), "custom: ") {
		t.Fatalf("response = %d %q, want the user rule's 418", w.Code, w.Body.String())
	}
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("Set-Cookie = %v, want none", cookies)
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
// handler: the Failure itself when nothing was written, the bare
// contract.ErrResponseWritten once the browser flow wrote the redirect.
func TestValidateCallback_ReturnsFailure(t *testing.T) {
	tests := []struct {
		name        string
		view        contract.ViewEngine
		accept      string
		wantFailure bool
	}{
		{name: "json client", view: backToSignup{}, accept: "application/json", wantFailure: true},
		{name: "no view engine", accept: "text/html", wantFailure: true},
		{name: "browser with view engine", view: backToSignup{}, accept: "text/html"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, _, _ := validationApp(t, tt.view)
			var got error
			a.Router.Post("/probe", func(c *router.Context) error {
				got = c.Validate(signupForm{}.Rules())
				return got
			})
			postSignup(a, "/probe", map[string]string{"Accept": tt.accept})

			var f *validation.Failure
			if isFailure := errors.As(got, &f); isFailure != tt.wantFailure {
				t.Fatalf("error = %v (failure %v), want failure %v", got, isFailure, tt.wantFailure)
			}
			if !tt.wantFailure && (!errors.Is(got, contract.ErrResponseWritten) || contract.HandledCause(got) != nil) {
				t.Errorf("error = %v, want the bare contract.ErrResponseWritten", got)
			}
		})
	}
}
