package velocity

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/router"
)

// cookiePolicyApp boots a real app from env the way a starter template
// does (ConfigFromEnv, AUTH_SCHEME=web, the framework CSRF
// RouterMiddleware) with the given env overrides. Routes live under
// SESSION_PATH so a non-root session cookie reaches them.
func cookiePolicyApp(t *testing.T, env map[string]string) *httptest.Server {
	t.Helper()
	base := map[string]string{
		"APP_ENV":      "local",
		"APP_KEY":      strings.Repeat("k", 32),
		"AUTH_SCHEME":  "web",
		"LOG_DRIVER":   "null",
		"CACHE_DRIVER": "memory",
		"QUEUE_DRIVER": "memory",
		"MAIL_DRIVER":  "log",
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	a, err := New(WithConfig(ConfigFromEnv()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	a.Router.Use(a.Services.CSRF.(*csrf.CSRF).RouterMiddleware())
	prefix := strings.TrimSuffix(env["SESSION_PATH"], "/")
	a.Router.Get(prefix+"/page", func(c *router.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	a.Router.Get(prefix+"/flash", func(c *router.Context) error {
		c.FlashErrors(map[string]string{"email": "required"})
		c.FlashInput(map[string]string{"email": "a@example.test"})
		return c.String(http.StatusOK, "ok")
	})
	a.Router.Get(prefix+"/forget", func(c *router.Context) error {
		c.DeleteCookie("velocity_session")
		return c.String(http.StatusOK, "ok")
	})
	a.Router.Post(prefix+"/submit", func(c *router.Context) error {
		return c.String(http.StatusOK, "submitted")
	})
	srv := httptest.NewServer(a.Router)
	t.Cleanup(srv.Close)
	return srv
}

func cookiePolicyGet(t *testing.T, client *http.Client, u string) map[string]*http.Cookie {
	t.Helper()
	resp, err := client.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	out := map[string]*http.Cookie{}
	for _, ck := range resp.Cookies() {
		out[ck.Name] = ck
	}
	return out
}

// SESSION_SAME_SITE and SESSION_DOMAIN reach the XSRF-TOKEN cookie, not
// only the session cookie; flashed errors and old input ride in the
// session cookie and write no cookie of their own.
func TestCookiePolicy_SessionSameSiteAndDomainReachXSRFCookie(t *testing.T) {
	srv := cookiePolicyApp(t, map[string]string{
		"SESSION_SAME_SITE": "none",
		"SESSION_SECURE":    "true",
		"SESSION_DOMAIN":    "example.test",
		"SESSION_PATH":      "/",
	})
	client := &http.Client{}

	page := cookiePolicyGet(t, client, srv.URL+"/page")
	flash := cookiePolicyGet(t, client, srv.URL+"/flash")
	sess := page["velocity_session"]
	if sess == nil {
		t.Fatal("no session cookie written")
	}
	if sess.SameSite != http.SameSiteNoneMode || sess.Domain != "example.test" {
		t.Fatalf("session cookie SameSite=%v Domain=%q, want None and example.test", sess.SameSite, sess.Domain)
	}
	for name := range flash {
		if name != "velocity_session" && name != "XSRF-TOKEN" {
			t.Errorf("flashing errors and old input wrote cookie %q; flash rides in the session cookie", name)
		}
	}
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
	}{
		{"XSRF-TOKEN", page["XSRF-TOKEN"]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cookie == nil {
				t.Fatal("cookie not written")
			}
			if tc.cookie.SameSite != sess.SameSite {
				t.Errorf("SameSite=%v, session cookie SameSite=%v", tc.cookie.SameSite, sess.SameSite)
			}
			if tc.cookie.Domain != sess.Domain {
				t.Errorf("Domain=%q, session cookie Domain=%q", tc.cookie.Domain, sess.Domain)
			}
			if tc.cookie.Path != sess.Path || tc.cookie.Secure != sess.Secure {
				t.Errorf("Path=%q Secure=%v, session cookie Path=%q Secure=%v", tc.cookie.Path, tc.cookie.Secure, sess.Path, sess.Secure)
			}
		})
	}
}

// With a non-root SESSION_PATH, ctx.DeleteCookie of the session cookie
// removes it from a real browser cookie jar.
func TestDeleteCookie_RemovesNonRootSessionCookieFromJar(t *testing.T) {
	srv := cookiePolicyApp(t, map[string]string{
		"SESSION_SECURE": "false",
		"SESSION_PATH":   "/app",
	})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	page := cookiePolicyGet(t, client, srv.URL+"/app/page")
	if s := page["velocity_session"]; s == nil || s.Path != "/app" {
		t.Fatalf("session cookie not set at /app: %v", s)
	}
	cookiePolicyGet(t, client, srv.URL+"/app/forget")
	u, _ := url.Parse(srv.URL + "/app/page")
	for _, c := range jar.Cookies(u) {
		if c.Name == "velocity_session" {
			t.Error("velocity_session still in the cookie jar for /app after ctx.DeleteCookie")
		}
	}
}

// csrfEnvProbe is one observation of the CSRF behaviour an env key
// governs, taken against a running app.
type csrfEnvProbe func(t *testing.T, srv *httptest.Server) string

// csrfSessionAndToken performs the safe-method bootstrap GET and returns
// a client holding the session cookie plus the XSRF cookie's decoded
// value ("" when none was written) under the given cookie name.
func csrfSessionAndToken(t *testing.T, srv *httptest.Server, xsrfName string) (*http.Client, *http.Cookie) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	got := cookiePolicyGet(t, client, srv.URL+"/page")
	return client, got[xsrfName]
}

func csrfTokenValue(t *testing.T, c *http.Cookie) string {
	t.Helper()
	if c == nil {
		t.Fatal("no XSRF cookie to take the token from")
	}
	v, err := url.QueryUnescape(c.Value)
	if err != nil {
		t.Fatalf("unescape XSRF cookie: %v", err)
	}
	return v
}

func csrfPost(t *testing.T, client *http.Client, u string, header http.Header, form url.Values) (int, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(http.MethodPost, u, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, string(b)
}

// Every CSRF_* env key ConfigFromEnv reads changes an observable
// Set-Cookie attribute or request outcome: the probe's result with the
// key set differs from the result without it. The key list is pinned to
// the keys config.go reads, so a new key without a row (or a removed key
// with a stale row) fails the test.
func TestCSRFEnvKeys_ChangeObservableBehaviour(t *testing.T) {
	xsrfMaxAge := func(name string) csrfEnvProbe {
		return func(t *testing.T, srv *httptest.Server) string {
			_, c := csrfSessionAndToken(t, srv, name)
			if c == nil {
				return "no cookie"
			}
			return "max-age=" + strconv.Itoa(c.MaxAge)
		}
	}
	xsrfPresent := func(name string) csrfEnvProbe {
		return func(t *testing.T, srv *httptest.Server) string {
			_, c := csrfSessionAndToken(t, srv, name)
			if c == nil {
				return "no " + name
			}
			return name + " written"
		}
	}
	postWithHeader := func(header string) csrfEnvProbe {
		return func(t *testing.T, srv *httptest.Server) string {
			client, c := csrfSessionAndToken(t, srv, "XSRF-TOKEN")
			code, _ := csrfPost(t, client, srv.URL+"/submit", http.Header{header: {csrfTokenValue(t, c)}}, nil)
			return "status " + strconv.Itoa(code)
		}
	}
	postWithField := func(field string) csrfEnvProbe {
		return func(t *testing.T, srv *httptest.Server) string {
			client, c := csrfSessionAndToken(t, srv, "XSRF-TOKEN")
			code, _ := csrfPost(t, client, srv.URL+"/submit", nil, url.Values{field: {csrfTokenValue(t, c)}})
			return "status " + strconv.Itoa(code)
		}
	}
	postTwice := func(t *testing.T, srv *httptest.Server) string {
		// The token comes from the store via the bootstrap cookie when
		// multi-use; single-use suppresses that cookie, so read the
		// outcome of a second submit of the same token instead.
		client, c := csrfSessionAndToken(t, srv, "XSRF-TOKEN")
		if c == nil {
			return "no XSRF cookie (single-use)"
		}
		tok := csrfTokenValue(t, c)
		csrfPost(t, client, srv.URL+"/submit", http.Header{"X-CSRF-Token": {tok}}, nil)
		code, _ := csrfPost(t, client, srv.URL+"/submit", http.Header{"X-CSRF-Token": {tok}}, nil)
		return "second submit status " + strconv.Itoa(code)
	}
	rejectionBody := func(t *testing.T, srv *httptest.Server) string {
		client, _ := csrfSessionAndToken(t, srv, "XSRF-TOKEN")
		_, body := csrfPost(t, client, srv.URL+"/submit", http.Header{"Accept": {"application/json"}}, nil)
		if strings.Contains(body, "custom rejection text") {
			return "custom message"
		}
		return "default message"
	}

	rows := []struct {
		key   string
		value string
		probe csrfEnvProbe
	}{
		{"CSRF_TOKEN_LIFETIME", "1h", xsrfMaxAge("XSRF-TOKEN")},
		{"CSRF_HEADER", "X-App-Token", postWithHeader("X-App-Token")},
		{"CSRF_FORM_FIELD", "app_token", postWithField("app_token")},
		{"CSRF_SESSION_COOKIE", "other_session", xsrfPresent("XSRF-TOKEN")},
		{"CSRF_SINGLE_USE", "true", postTwice},
		{"CSRF_ERROR_MESSAGE", "custom rejection text", rejectionBody},
		{"CSRF_WRITE_XSRF_COOKIE", "false", xsrfPresent("XSRF-TOKEN")},
		{"CSRF_XSRF_COOKIE_NAME", "APP-XSRF", xsrfPresent("APP-XSRF")},
	}

	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	read := map[string]bool{}
	for _, m := range regexp.MustCompile(`"(CSRF_[A-Z_]+)"`).FindAllStringSubmatch(string(src), -1) {
		read[m[1]] = true
	}
	covered := map[string]bool{}
	for _, r := range rows {
		covered[r.key] = true
	}
	var missing, stale []string
	for k := range read {
		if !covered[k] {
			missing = append(missing, k)
		}
	}
	for k := range covered {
		if !read[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 || len(stale) > 0 {
		t.Fatalf("CSRF env keys read by config.go without a row: %v; rows for keys config.go does not read: %v", missing, stale)
	}

	for _, r := range rows {
		t.Run(r.key, func(t *testing.T) {
			baseline := r.probe(t, cookiePolicyApp(t, map[string]string{"SESSION_SECURE": "false"}))
			withKey := r.probe(t, cookiePolicyApp(t, map[string]string{"SESSION_SECURE": "false", r.key: r.value}))
			t.Logf("without %s: %s; with %s=%q: %s", r.key, baseline, r.key, r.value, withKey)
			if baseline == withKey {
				t.Errorf("%s=%q changed nothing observable: %q both ways", r.key, r.value, baseline)
			}
		})
	}
}

// No non-test framework file other than the cookie policy builder
// (contract/cookie.go) constructs an http.Cookie, so every framework
// cookie carries the one policy. Context.SetCookie stays a raw
// pass-through for cookies an application builds itself.
func TestFrameworkCookies_OnlyBuiltByCookiePolicy(t *testing.T) {
	const builder = "contract/cookie.go"
	fset := token.NewFileSet()
	var offenders []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.ToSlash(path) == builder {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			var typ ast.Expr
			switch x := n.(type) {
			case *ast.CompositeLit:
				typ = x.Type
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 {
					typ = x.Args[0]
				}
			}
			if sel, ok := typ.(*ast.SelectorExpr); ok && sel.Sel.Name == "Cookie" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "http" {
					offenders = append(offenders, fset.Position(n.Pos()).String())
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("http.Cookie constructed outside %s (build it with contract.CookiePolicy.Cookie):\n  %s", builder, strings.Join(offenders, "\n  "))
	}
}
