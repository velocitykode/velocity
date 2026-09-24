package csrf

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf/stores"
)

// protectFixture is one request the differential test runs through both
// Middleware and Protect plus next.
type protectFixture struct {
	name string
	// config adjusts the shared test config.
	config func(cfg *Config)
	// request builds the request from the fixture's tokens.
	request func(tok fixtureTokens) *http.Request
	// wantPass reports whether next must run.
	wantPass bool
	// wantReason is the rejection reason when wantPass is false.
	wantReason error
}

// fixtureTokens are the token values one fixture submits: stored is the
// token held for session "s1", masked a masked form of it and other a
// token that does not match. They are fixed per fixture so both paths see
// byte-identical requests.
type fixtureTokens struct {
	stored string
	masked string
	other  string
}

// protectObservation is everything one run exposes: the response and what
// next saw.
type protectObservation struct {
	status  int
	headers http.Header
	cookies []string

	called      bool
	ownState    bool
	token       string
	tokenErr    string
	body        string
	method      string
	path        string
	rejectedWhy error
}

// protectFixtures covers every branch of Protect.
func protectFixtures() []protectFixture {
	withSession := func(r *http.Request) *http.Request {
		r.AddCookie(&http.Cookie{Name: "session_id", Value: "s1"})
		return r
	}
	return []protectFixture{
		{
			name: "safe GET bootstraps the XSRF cookie",
			request: func(fixtureTokens) *http.Request {
				return withSession(httptest.NewRequest(http.MethodGet, "/page", nil))
			},
			wantPass: true,
		},
		{
			name:     "safe GET without a session writes no cookie",
			request:  func(fixtureTokens) *http.Request { return httptest.NewRequest(http.MethodGet, "/page", nil) },
			wantPass: true,
		},
		{
			name: "valid token in the header",
			request: func(tok fixtureTokens) *http.Request {
				r := withSession(httptest.NewRequest(http.MethodPost, "/submit", nil))
				r.Header.Set("X-CSRF-Token", tok.stored)
				return r
			},
			wantPass: true,
		},
		{
			name: "valid masked token in the form body",
			request: func(tok fixtureTokens) *http.Request {
				form := url.Values{"_token": {tok.masked}, "name": {"x"}}.Encode()
				r := withSession(httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(form)))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return r
			},
			wantPass: true,
		},
		{
			name: "missing token",
			request: func(fixtureTokens) *http.Request {
				return withSession(httptest.NewRequest(http.MethodPost, "/submit", nil))
			},
			wantReason: ErrTokenMissing,
		},
		{
			name: "mismatched token",
			request: func(tok fixtureTokens) *http.Request {
				r := withSession(httptest.NewRequest(http.MethodPut, "/submit", nil))
				r.Header.Set("X-CSRF-Token", tok.other)
				return r
			},
			wantReason: ErrTokenInvalid,
		},
		{
			name: "no session",
			request: func(tok fixtureTokens) *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/submit", nil)
				r.Header.Set("X-CSRF-Token", tok.stored)
				return r
			},
			wantReason: ErrNoSession,
		},
		{
			name:   "oversize form body",
			config: func(cfg *Config) { cfg.MaxFormBodyBytes = 16 },
			request: func(fixtureTokens) *http.Request {
				r := withSession(httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader("_token="+strings.Repeat("a", 64))))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return r
			},
			wantReason: ErrFormBodyTooLarge,
		},
		{
			name:   "excluded path",
			config: func(cfg *Config) { cfg.ExcludePaths = []string{"/webhooks/*"} },
			request: func(fixtureTokens) *http.Request {
				return withSession(httptest.NewRequest(http.MethodPost, "/webhooks/stripe", nil))
			},
			wantPass: true,
		},
		{
			name: "excluded by func",
			config: func(cfg *Config) {
				cfg.ExcludeFunc = func(r *http.Request) bool { return r.Header.Get("X-Api-Key") != "" }
			},
			request: func(fixtureTokens) *http.Request {
				r := httptest.NewRequest(http.MethodDelete, "/item", nil)
				r.Header.Set("X-Api-Key", "k")
				return r
			},
			wantPass: true,
		},
		{
			name:     "testing environment bypass",
			config:   func(cfg *Config) { cfg.Env = "testing" },
			request:  func(fixtureTokens) *http.Request { return httptest.NewRequest(http.MethodPatch, "/submit", nil) },
			wantPass: true,
		},
	}
}

// newFixtureTokens mints one fixture's token values.
func newFixtureTokens(t *testing.T) fixtureTokens {
	t.Helper()
	stored, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	masked, err := MaskToken(stored)
	if err != nil {
		t.Fatalf("MaskToken: %v", err)
	}
	other, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return fixtureTokens{stored: stored, masked: masked, other: other}
}

// newProtectInstance builds a fresh instance whose store holds token for
// session "s1", with an ErrorHandler that records the reason and writes
// nothing, so both paths leave the response untouched on rejection.
func newProtectInstance(t *testing.T, fx protectFixture, token string, reason *error) *CSRF {
	t.Helper()
	cfg := testConfig()
	cfg.Store = stores.NewSessionStore()
	cfg.Secure = false
	if fx.config != nil {
		fx.config(cfg)
	}
	cfg.ErrorHandler = func(_ http.ResponseWriter, _ *http.Request, err error) { *reason = err }
	c, err := NewE(cfg)
	if err != nil {
		t.Fatalf("NewE: %v", err)
	}
	if err := c.config.Store.Set("s1", token); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	return c
}

// observingNext records what the downstream handler sees.
func observingNext(t *testing.T, c *CSRF, obs *protectObservation) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		obs.called = true
		obs.method = r.Method
		obs.path = r.URL.Path
		if st := tokenStateFromContext(r.Context()); st != nil && st.csrf == c {
			obs.ownState = true
		}
		tok, err := TokenForRequest(r)
		obs.token = UnmaskToken(tok)
		if err != nil {
			obs.tokenErr = err.Error()
		}
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read body downstream: %v", err)
			}
			obs.body = string(b)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// recordResponse fills the response half of obs, replacing each masked
// XSRF-TOKEN value with the token it carries (the mask is random per
// response, the token is not).
func recordResponse(t *testing.T, w *httptest.ResponseRecorder, obs *protectObservation) {
	t.Helper()
	obs.status = w.Code
	obs.headers = w.Header().Clone()
	delete(obs.headers, "Set-Cookie")
	for _, ck := range w.Result().Cookies() {
		value := ck.Value
		if ck.Name == "XSRF-TOKEN" {
			raw, err := url.QueryUnescape(value)
			if err != nil {
				t.Fatalf("XSRF-TOKEN not URL-encoded: %v", err)
			}
			value = UnmaskToken(raw)
		}
		obs.cookies = append(obs.cookies, fmt.Sprintf("%s=%s; Path=%s; MaxAge=%d; HttpOnly=%v; Secure=%v; SameSite=%v",
			ck.Name, value, ck.Path, ck.MaxAge, ck.HttpOnly, ck.Secure, ck.SameSite))
	}
	sort.Strings(obs.cookies)
}

// TestProtect_MatchesMiddleware runs every fixture through Middleware and
// through Protect plus next, and asserts identical responses (status,
// headers, cookies) and identical downstream observations (context state,
// request token, body).
func TestProtect_MatchesMiddleware(t *testing.T) {
	for _, fx := range protectFixtures() {
		t.Run(fx.name, func(t *testing.T) {
			tok := newFixtureTokens(t)
			token := tok.stored

			var viaMiddleware protectObservation
			mc := newProtectInstance(t, fx, token, &viaMiddleware.rejectedWhy)
			mw := httptest.NewRecorder()
			mc.Middleware(observingNext(t, mc, &viaMiddleware)).ServeHTTP(mw, fx.request(tok))
			recordResponse(t, mw, &viaMiddleware)

			var viaProtect protectObservation
			var unused error
			pc := newProtectInstance(t, fx, token, &unused)
			pw := httptest.NewRecorder()
			r, perr := pc.Protect(pw, fx.request(tok))
			if r == nil {
				t.Fatal("Protect returned a nil request")
			}
			if perr == nil {
				observingNext(t, pc, &viaProtect).ServeHTTP(pw, r)
			} else {
				var tm *TokenMismatchError
				if !errors.As(perr, &tm) {
					t.Fatalf("Protect error %T is not a *TokenMismatchError", perr)
				}
				viaProtect.rejectedWhy = tm.Reason
				if st := tokenStateFromContext(r.Context()); st == nil || st.csrf != pc {
					t.Error("rejected request must still carry this instance's token state")
				}
			}
			if unused != nil {
				t.Errorf("Protect must not call Config.ErrorHandler, got %v", unused)
			}
			recordResponse(t, pw, &viaProtect)

			if viaMiddleware.called != fx.wantPass {
				t.Fatalf("Middleware called next = %v, want %v", viaMiddleware.called, fx.wantPass)
			}
			if !fx.wantPass {
				if !errors.Is(viaMiddleware.rejectedWhy, fx.wantReason) {
					t.Errorf("Middleware reason = %v, want %v", viaMiddleware.rejectedWhy, fx.wantReason)
				}
				if pw.Body.Len() != 0 || pw.Code != http.StatusOK {
					t.Errorf("Protect wrote a response: %d %q", pw.Code, pw.Body.String())
				}
			}
			if fx.wantPass && !viaProtect.ownState {
				t.Error("next must see this instance's token state")
			}
			if !reflect.DeepEqual(viaMiddleware, viaProtect) {
				t.Errorf("paths differ\nMiddleware: %+v\nProtect:    %+v", viaMiddleware, viaProtect)
			}
		})
	}
}

// TestProtect_BootstrapCookieCarriesStoredToken pins the one fixture whose
// cookie the differential compares only through unmasking: the cookie
// holds the stored token.
func TestProtect_BootstrapCookieCarriesStoredToken(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	var unused error
	c := newProtectInstance(t, protectFixture{}, token, &unused)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "s1"})
	w := httptest.NewRecorder()
	if _, err := c.Protect(w, req); err != nil {
		t.Fatalf("Protect: %v", err)
	}
	ck := findXSRFCookie(w.Result().Cookies())
	if ck == nil {
		t.Fatal("XSRF-TOKEN cookie not written")
	}
	raw, _ := url.QueryUnescape(ck.Value)
	if got := UnmaskToken(raw); got != token {
		t.Errorf("cookie token = %q, want the stored %q", got, token)
	}
}

func TestTokenMismatchError(t *testing.T) {
	tests := []struct {
		name     string
		err      *TokenMismatchError
		wantText string
		wantWhy  error
	}{
		{name: "with reason", err: &TokenMismatchError{Reason: ErrTokenInvalid}, wantText: "velocity/csrf: token mismatch: velocity/csrf: token invalid", wantWhy: ErrTokenInvalid},
		{name: "without reason", err: &TokenMismatchError{}, wantText: "velocity/csrf: token mismatch", wantWhy: ErrTokenMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.wantText {
				t.Errorf("Error() = %q, want %q", got, tt.wantText)
			}
			if got := tt.err.StatusCode(); got != 419 {
				t.Errorf("StatusCode() = %d, want 419", got)
			}
			if tt.err.ShouldReport() {
				t.Error("ShouldReport() = true, want false")
			}
			if got := tt.err.reason(); !errors.Is(got, tt.wantWhy) {
				t.Errorf("reason() = %v, want %v", got, tt.wantWhy)
			}
			if got := tt.err.Unwrap(); got != tt.wantWhy {
				t.Errorf("Unwrap() = %v, want %v", got, tt.wantWhy)
			}

			var se contract.StatusError
			var rep contract.Reportable
			if !errors.As(error(tt.err), &se) || !errors.As(error(tt.err), &rep) {
				t.Error("must satisfy contract.StatusError and contract.Reportable")
			}
			status, _, ok := contract.StatusOf(tt.err)
			if !ok || status != 419 {
				t.Errorf("StatusOf = %d, %v; want 419, true", status, ok)
			}
		})
	}
}

// TestTokenMismatchError_WrappedMatchesReason pins that a wrapped
// rejection matches its reason (not ErrTokenMissing) and unwraps to the
// typed error.
func TestTokenMismatchError_WrappedMatchesReason(t *testing.T) {
	c := New(testConfig())
	req := httptest.NewRequest(http.MethodPost, "/submit", nil)
	req.Header.Set("X-CSRF-Token", newFixtureTokens(t).stored)
	_, err := c.Protect(httptest.NewRecorder(), req)
	if err == nil {
		t.Fatal("expected a rejection")
	}
	wrapped := []error{
		err,
		fmt.Errorf("handler: %w", err),
		fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", err)),
		errors.Join(errors.New("other"), err),
		&contract.HTTPError{Status: 419, Cause: err},
	}
	for i, w := range wrapped {
		if !errors.Is(w, ErrNoSession) || errors.Is(w, ErrTokenMissing) {
			t.Errorf("[%d] errors.Is(_, ErrNoSession) = %v, errors.Is(_, ErrTokenMissing) = %v; want true, false", i, errors.Is(w, ErrNoSession), errors.Is(w, ErrTokenMissing))
		}
		var tm *TokenMismatchError
		if !errors.As(w, &tm) || !errors.Is(tm.Reason, ErrNoSession) {
			t.Errorf("[%d] errors.As did not reach the rejection with reason ErrNoSession", i)
		}
	}
}

// TestProtect_NilErrorIsUntyped pins that an accepted request returns a
// nil error interface, not a typed nil *TokenMismatchError.
func TestProtect_NilErrorIsUntyped(t *testing.T) {
	c := New(testConfig())
	_, err := c.Protect(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("err = %#v, want untyped nil", err)
	}
}

func TestRenderTokenMismatch(t *testing.T) {
	var gotReason error
	handler := func(w http.ResponseWriter, r *http.Request, err error) {
		gotReason = err
		w.Header().Set("X-Handled-By", "custom")
		w.WriteHeader(http.StatusTeapot)
	}
	tests := []struct {
		name       string
		err        error
		wantOK     bool
		wantStatus int
		wantReason error
	}{
		{name: "handler configured", err: &TokenMismatchError{Reason: ErrTokenInvalid, handler: handler}, wantOK: true, wantStatus: http.StatusTeapot, wantReason: ErrTokenInvalid},
		{name: "handler configured, wrapped", err: &contract.HTTPError{Status: 419, Cause: &TokenMismatchError{Reason: ErrNoSession, handler: handler}}, wantOK: true, wantStatus: http.StatusTeapot, wantReason: ErrNoSession},
		{name: "handler configured, no reason", err: &TokenMismatchError{handler: handler}, wantOK: true, wantStatus: http.StatusTeapot, wantReason: ErrTokenMissing},
		{name: "no handler falls through", err: &TokenMismatchError{Reason: ErrTokenInvalid}, wantStatus: http.StatusOK},
		{name: "other error falls through", err: errors.New("boom"), wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReason = nil
			w := httptest.NewRecorder()
			rc := contract.NewRenderContext(w, httptest.NewRequest(http.MethodPost, "/submit", nil))
			if got := RenderTokenMismatch(rc, tt.err, nil); got != tt.wantOK {
				t.Fatalf("RenderTokenMismatch = %v, want %v", got, tt.wantOK)
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantOK {
				if !errors.Is(gotReason, tt.wantReason) {
					t.Errorf("handler reason = %v, want %v", gotReason, tt.wantReason)
				}
				if w.Header().Get("X-Handled-By") != "custom" {
					t.Error("custom handler did not write")
				}
			} else if gotReason != nil || w.Body.Len() != 0 || len(w.Header()) != 0 {
				t.Errorf("fall-through wrote a response: reason=%v headers=%v body=%q", gotReason, w.Header(), w.Body.String())
			}
		})
	}
	if RenderTokenMismatch(nil, &TokenMismatchError{handler: handler}, nil) {
		t.Error("nil RenderContext must fall through")
	}
}

// rejectionJSON is the problem+json body the bare Middleware writes for a
// rejected JSON client under the default Config.ErrorMessage.
const rejectionJSON = `{"type":"about:blank","title":"Page Expired","status":419,"detail":"CSRF token validation failed. Please refresh and try again.","instance":"/submit"}`

// TestMiddleware_RejectionBody pins the standalone writer: JSON is chosen
// by contract.WantsJSON, and Config.ErrorHandler wins when configured.
func TestMiddleware_RejectionBody(t *testing.T) {
	tests := []struct {
		name        string
		headers     map[string]string
		custom      bool
		wantStatus  int
		wantType    string
		wantBody    string
		wantHandled bool
	}{
		{name: "JSON accept", headers: map[string]string{"Accept": "application/json"}, wantStatus: 419, wantType: "application/problem+json", wantBody: rejectionJSON},
		{name: "XHR", headers: map[string]string{"X-Requested-With": "XMLHttpRequest"}, wantStatus: 419, wantType: "application/problem+json", wantBody: rejectionJSON},
		{name: "JSON body without JSON accept", headers: map[string]string{"Content-Type": "application/json", "Accept": "text/html"}, wantStatus: 419, wantType: "text/plain; charset=utf-8", wantBody: "CSRF token validation failed. Please refresh and try again.\n"},
		{name: "Inertia", headers: map[string]string{"X-Inertia": "true", "Accept": "application/json"}, wantStatus: 419, wantType: "text/plain; charset=utf-8", wantBody: "CSRF token validation failed. Please refresh and try again.\n"},
		{name: "browser", headers: map[string]string{"Accept": "text/html"}, wantStatus: 419, wantType: "text/plain; charset=utf-8", wantBody: "CSRF token validation failed. Please refresh and try again.\n"},
		{name: "custom handler", headers: map[string]string{"Accept": "application/json"}, custom: true, wantStatus: http.StatusTeapot, wantBody: "custom", wantHandled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			var handled error
			if tt.custom {
				cfg.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
					handled = err
					w.WriteHeader(http.StatusTeapot)
					_, _ = w.Write([]byte("custom"))
				}
			}
			c := New(cfg)
			req := httptest.NewRequest(http.MethodPost, "/submit", nil)
			req.Header.Set("X-CSRF-Token", newFixtureTokens(t).stored)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			c.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("next must not run on rejection")
			})).ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantType != "" && w.Header().Get("Content-Type") != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", w.Header().Get("Content-Type"), tt.wantType)
			}
			if w.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", w.Body.String(), tt.wantBody)
			}
			if tt.wantHandled != (handled != nil) {
				t.Errorf("ErrorHandler called = %v, want %v", handled != nil, tt.wantHandled)
			}
			if tt.wantHandled && !errors.Is(handled, ErrNoSession) {
				t.Errorf("ErrorHandler reason = %v, want ErrNoSession (the raw reason, not the typed error)", handled)
			}
			var tm *TokenMismatchError
			if errors.As(handled, &tm) {
				t.Error("ErrorHandler must receive the reason, not the *TokenMismatchError")
			}
		})
	}
}
