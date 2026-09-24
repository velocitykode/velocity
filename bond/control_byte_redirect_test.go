package bond

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
)

// controlByteRedirectTargets are redirect targets that look same-origin
// to a prefix check but reach the browser as the network-path reference
// "//evil.test/x": the WHATWG URL parser removes every TAB/LF/CR and
// trims leading/trailing C0-control-or-space, and net/http trims header
// values on write. Every one must be REJECTED to "/". Stripping is not
// an acceptable outcome: stripping "/\n/evil.test/x" after validation is
// what produces "//evil.test/x".
var controlByteRedirectTargets = []struct {
	name   string
	target string
}{
	{"tab between slashes", "/\t/evil.test/x"},
	{"lf between slashes", "/\n/evil.test/x"},
	{"cr between slashes", "/\r/evil.test/x"},
	{"crlf between slashes", "/\r\n/evil.test/x"},
	{"leading space", " //evil.test/x"},
	{"leading tab", "\t//evil.test/x"},
	{"leading lf", "\n//evil.test/x"},
	{"leading nul", "\x00//evil.test/x"},
	{"trailing space", "/ok "},
	{"del in path", "/ok\x7f"},
	{"tab inside scheme", "java\tscript:alert(1)"},
	{"lf inside scheme", "ht\ntps://evil.test/x"},
	{"header injection", "/ok\r\nX-Injected: 1"},
	{"header injection absolute", "https://external.example/\r\nX-Injected: 1"},
}

// wireHeaders serves h on a real socket and returns the status line plus
// the raw response header lines keyed by lower-cased name. A recorder is
// not enough: the leading-space variant only appears once net/http trims
// the value while writing it to the connection.
func wireHeaders(t *testing.T, h http.Handler, inertia bool) (status string, headers map[string]string) {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := "GET /login HTTP/1.1\r\nHost: app.test\r\nConnection: close\r\n"
	if inertia {
		req += "X-Inertia: true\r\n"
	}
	if _, err := fmt.Fprint(conn, req+"\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	headers = map[string]string{}
	sc := bufio.NewScanner(conn)
	if sc.Scan() {
		status = sc.Text()
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ": "); ok {
			headers[strings.ToLower(name)] = value
		}
	}
	return status, headers
}

// TestRedirectSinks_ControlBytes_WireHeaders drives every bond sink that
// writes a caller-supplied target to a header and asserts the headers on
// the wire, for Inertia and non-Inertia requests.
func TestRedirectSinks_ControlBytes_WireHeaders(t *testing.T) {
	b := setupBond(t)

	sinks := []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request, target string)
		// inertiaOnly409 marks sinks that answer an Inertia request with
		// a 409 + X-Inertia-Location and no Location header.
		inertiaOnly409 bool
	}{
		{"Redirect", b.Redirect, false},
		{"RedirectWithStatus", func(w http.ResponseWriter, r *http.Request, target string) {
			b.RedirectWithStatus(w, r, target, http.StatusFound)
		}, false},
		{"Back", func(w http.ResponseWriter, r *http.Request, target string) {
			r.Header.Set("Referer", target)
			b.Back(w, r)
		}, false},
		{"Location", b.Location, true},
		{"LocationExternal", b.LocationExternal, true},
	}

	for _, sink := range sinks {
		for _, inertia := range []bool{false, true} {
			for _, tc := range controlByteRedirectTargets {
				name := fmt.Sprintf("%s/inertia=%v/%s", sink.name, inertia, tc.name)
				t.Run(name, func(t *testing.T) {
					h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						sink.call(w, r, tc.target)
					})
					status, headers := wireHeaders(t, h, inertia)

					if _, ok := headers["x-injected"]; ok {
						t.Errorf("injected header reached the wire: %v", headers)
					}

					loc, hasLoc := headers["location"]
					xloc, hasXLoc := headers["x-inertia-location"]

					if inertia && sink.inertiaOnly409 {
						if !strings.Contains(status, "409") {
							t.Errorf("status = %q, want 409", status)
						}
						if hasLoc {
							t.Errorf("unexpected Location %q on 409", loc)
						}
					} else if !hasLoc || loc != "/" {
						t.Errorf("Location on the wire = %q (present=%v), want /", loc, hasLoc)
					}

					if inertia {
						if !hasXLoc || xloc != "/" {
							t.Errorf("X-Inertia-Location on the wire = %q (present=%v), want /", xloc, hasXLoc)
						}
					} else if hasXLoc {
						t.Errorf("unexpected X-Inertia-Location %q on non-Inertia request", xloc)
					}
				})
			}
		}
	}
}

func TestSanitizeRedirectURL_RejectsControlBytes(t *testing.T) {
	for _, tc := range controlByteRedirectTargets {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeRedirectURL(tc.target, []string{"external.example"}); got != "/" {
				t.Errorf("sanitizeRedirectURL(%q) = %q, want /", tc.target, got)
			}
		})
	}
}

// TestSanitizeLocationScheme_RejectsControlBytes covers the leading-"/"
// shortcut in sanitizeLocationScheme, which returns without consulting
// contract.SanitizeRedirect.
func TestSanitizeLocationScheme_RejectsControlBytes(t *testing.T) {
	for _, tc := range controlByteRedirectTargets {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeLocationScheme(tc.target); got != "/" {
				t.Errorf("sanitizeLocationScheme(%q) = %q, want /", tc.target, got)
			}
		})
	}
	for _, target := range []string{"/ok", "/path with spaces", "https://external.example/cb?x=1"} {
		if got := sanitizeLocationScheme(target); got != target {
			t.Errorf("sanitizeLocationScheme(%q) = %q, want unchanged", target, got)
		}
	}
}

// TestUnsafeRedirectTargets_RefusedEverywhere asserts every unsafe
// target is refused identically by router.SanitizeRedirect, the bare
// contract RenderContext and bond (its sanitizer and its Redirect,
// Location and Back sinks, which answer with "/").
func TestUnsafeRedirectTargets_RefusedEverywhere(t *testing.T) {
	const host = "trusted.example"
	targets := []string{
		"", "//evil.test/x", "///evil.test", `/\evil.test/x`, `\\evil.test/x`,
		"/／evil.test", "/⧸evil.test", "/⁄evil.test", "/∕evil.test",
		"javascript:alert(1)", "data:text/html,x", "http:evil.test",
		"https://evil.test/x", "https://" + host + "@evil.test/x", "http://[::1",
	}
	for _, tc := range controlByteRedirectTargets {
		targets = append(targets, tc.target)
	}
	b := setupBond(t)
	sinks := map[string]func(w http.ResponseWriter, r *http.Request, target string){
		"Redirect": b.Redirect,
		"Location": b.Location,
		"Back": func(w http.ResponseWriter, r *http.Request, target string) {
			r.Header.Set("Referer", target)
			b.Back(w, r)
		},
	}
	for _, target := range targets {
		if got := router.SanitizeRedirect(target, []string{host}); got != "/" {
			t.Errorf("router.SanitizeRedirect(%q) = %q, want /", target, got)
		}
		if got := contract.SanitizeRedirect(target, []string{host}); got != "/" {
			t.Errorf("contract.SanitizeRedirect(%q) = %q, want /", target, got)
		}
		if got := sanitizeRedirectURL(target, []string{host}); got != "/" {
			t.Errorf("sanitizeRedirectURL(%q) = %q, want /", target, got)
		}

		w := httptest.NewRecorder()
		rc := contract.NewRenderContext(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if err := rc.Redirect(http.StatusFound, target); !errors.Is(err, contract.ErrInvalidRedirect) || w.Header().Get("Location") != "" {
			t.Errorf("NewRenderContext Redirect(%q) = %v, Location %q; want refused, no Location", target, err, w.Header().Get("Location"))
		}

		for name, sink := range sinks {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Host = host
			sink(w, r, target)
			if got := w.Header().Get("Location"); got != "/" {
				t.Errorf("bond %s(%q): Location = %q, want /", name, target, got)
			}
		}
	}
}

// TestMiddleware_VersionMismatch_ControlBytesInRequestURL covers the
// version-mismatch 409, which echoes the request URL into
// X-Inertia-Location. A real server rejects such request lines, so the
// URL is forged directly. A path is escaped by url.URL.String and is
// echoed in that escaped form; a raw query is not escaped, so the whole
// value is rejected.
func TestMiddleware_VersionMismatch_ControlBytesInRequestURL(t *testing.T) {
	b := setupBond(t)

	cases := []struct {
		name string
		u    *url.URL
		want string
	}{
		{"crlf in path is escaped", &url.URL{Path: "/foo\r\nX-Injected: 1"}, "/foo%0D%0AX-Injected:%201"},
		{"crlf in raw query is rejected", &url.URL{Path: "/foo", RawQuery: "a=1\r\nX-Injected: 1"}, "/"},
		{"tab in raw query is rejected", &url.URL{Path: "/foo", RawQuery: "a=\t1"}, "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("next handler must not run on version mismatch")
			})
			w := httptest.NewRecorder()
			r := &http.Request{
				Method: http.MethodGet,
				URL:    tc.u,
				Header: http.Header{
					"X-Inertia":   []string{"true"},
					HeaderVersion: []string{"client-version-mismatch"},
				},
			}

			b.Middleware(next).ServeHTTP(w, r)

			if w.Code != http.StatusConflict {
				t.Errorf("status = %d, want %d", w.Code, http.StatusConflict)
			}
			if w.Header().Get("X-Injected") != "" {
				t.Errorf("X-Injected header present: %q", w.Header().Get("X-Injected"))
			}
			if got := w.Header().Get(HeaderLocation); got != tc.want {
				t.Errorf("%s = %q, want %q", HeaderLocation, got, tc.want)
			}
		})
	}
}
