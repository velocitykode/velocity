package router

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// controlByteRedirectTargets are redirect targets that look same-origin
// to a prefix check but reach the browser as the network-path reference
// "//evil.test/x": the WHATWG URL parser removes every TAB/LF/CR and
// trims leading/trailing C0-control-or-space, and net/http trims header
// values on write. Every one must be rejected, never stripped, by each
// router sink that writes a redirect target.
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
	{"leading unit separator", "\x1f//evil.test/x"},
	{"trailing space", "/ok "},
	{"trailing tab", "/ok\t"},
	{"nul in path", "/ok\x00"},
	{"del in path", "/ok\x7f"},
	{"tab inside scheme", "java\tscript:alert(1)"},
	{"lf inside scheme", "ht\ntps://evil.test/x"},
	{"header injection", "/ok\r\nX-Injected: 1"},
}

// wireHeaders serves h on a real socket and returns the raw response
// header lines keyed by lower-cased name. A recorder is not enough here:
// the leading-space variant only appears once net/http trims the value
// while writing it to the connection.
func wireHeaders(t *testing.T, h http.Handler, reqHeaders ...string) map[string]string {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := "GET /login HTTP/1.1\r\nHost: app.test\r\nConnection: close\r\n"
	for _, h := range reqHeaders {
		req += h + "\r\n"
	}
	if _, err := fmt.Fprint(conn, req+"\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	out := map[string]string{}
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ": "); ok {
			out[strings.ToLower(name)] = value
		}
	}
	return out
}

func TestContextRedirect_ControlBytes_WireLocation(t *testing.T) {
	for _, tc := range controlByteRedirectTargets {
		t.Run(tc.name, func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = NewContext(w, r).Redirect(http.StatusFound, tc.target)
			})
			headers := wireHeaders(t, h)
			if got := headers["location"]; got != "/" {
				t.Errorf("Location on the wire = %q, want /", got)
			}
			if _, ok := headers["x-injected"]; ok {
				t.Errorf("injected header reached the wire: %v", headers)
			}
		})
	}
}

func TestRedirectToIntended_ControlBytes_WireLocation(t *testing.T) {
	for _, tc := range controlByteRedirectTargets {
		t.Run(tc.name, func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c := NewContext(w, r)
				stashIntended(c, tc.target)
				_ = c.RedirectToIntended("/home")
			})
			headers := wireHeaders(t, h)
			if got := headers["location"]; got != "/home" {
				t.Errorf("Location on the wire = %q, want /home (rejected target must fall back)", got)
			}
		})
	}
}

// TestIntended_ControlByteFallbackRejected covers a caller-supplied
// fallback carrying the same bytes: it is sanitised too.
func TestIntended_ControlByteFallbackRejected(t *testing.T) {
	c, _ := NewTestContext("GET", "/login")
	if got := c.Intended("/\t/evil.test/x"); got != "/" {
		t.Errorf("Intended fallback = %q, want /", got)
	}
}
