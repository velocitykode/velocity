package testing_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/httpclient"
	httpclienttest "github.com/velocitykode/velocity/httpclient/testing"
)

func TestMatchers(t *testing.T) {
	req := func(method, rawurl string) *http.Request {
		r, err := http.NewRequest(method, rawurl, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		return r
	}

	tests := []struct {
		name  string
		match httpclienttest.Matcher
		req   *http.Request
		want  bool
	}{
		{"method hit", httpclienttest.MatchMethod(http.MethodPost), req(http.MethodPost, "https://x/a"), true},
		{"method miss", httpclienttest.MatchMethod(http.MethodPost), req(http.MethodGet, "https://x/a"), false},
		{"url hit", httpclienttest.MatchURL("/users/1"), req(http.MethodGet, "https://x/users/1"), true},
		{"url miss", httpclienttest.MatchURL("/users/2"), req(http.MethodGet, "https://x/users/1"), false},
		{"method+url hit", httpclienttest.MatchMethodAndURL(http.MethodGet, "/users/1"), req(http.MethodGet, "https://x/users/1"), true},
		{"method+url method miss", httpclienttest.MatchMethodAndURL(http.MethodPost, "/users/1"), req(http.MethodGet, "https://x/users/1"), false},
		{"method+url url miss", httpclienttest.MatchMethodAndURL(http.MethodGet, "/users/2"), req(http.MethodGet, "https://x/users/1"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.match(tc.req); got != tc.want {
				t.Errorf("match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRoundTripServesFirstMatchingStub(t *testing.T) {
	fake := httpclienttest.New()
	fake.
		Stub(httpclienttest.MatchURL("/a"), httpclienttest.NewResponse(http.StatusTeapot, []byte("first"))).
		Stub(httpclienttest.MatchURL("/a"), httpclienttest.NewResponse(http.StatusOK, []byte("second")))

	client := fake.Client(httpclient.WithBaseURL("https://api.example.com"), httpclient.WithoutPrivateIPDeny())
	resp, err := client.Get(context.Background(), "/a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d (first registered stub wins)", resp.StatusCode, http.StatusTeapot)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "first" {
		t.Errorf("body = %q, want %q", body, "first")
	}
}

func TestRoundTripNoStub(t *testing.T) {
	fake := httpclienttest.New()
	client := fake.Client(httpclient.WithoutPrivateIPDeny())

	_, err := client.Get(context.Background(), "https://api.example.com/missing")
	if err == nil {
		t.Fatal("expected error for unstubbed request, got nil")
	}
}

func TestRecordsRequestBody(t *testing.T) {
	fake := httpclienttest.New()
	fake.Stub(httpclienttest.MatchMethod(http.MethodPost), httpclienttest.NewResponse(http.StatusOK, nil))

	client := fake.Client(httpclient.WithoutPrivateIPDeny())
	resp, err := client.Post(context.Background(), "https://api.example.com/submit", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	resp.Body.Close()

	reqs := fake.GetRequests()
	if len(reqs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(reqs))
	}
	got, _ := io.ReadAll(reqs[0].Body)
	if string(got) != "payload" {
		t.Errorf("recorded body = %q, want %q", got, "payload")
	}
}

func TestGetRequestsIsDefensiveCopy(t *testing.T) {
	fake := httpclienttest.New()
	fake.Stub(httpclienttest.MatchMethod(http.MethodGet), httpclienttest.NewResponse(http.StatusOK, nil))

	client := fake.Client(httpclient.WithoutPrivateIPDeny())
	resp, _ := client.Get(context.Background(), "https://api.example.com/a")
	resp.Body.Close()

	first := fake.GetRequests()
	first[0] = nil // mutate the copy

	second := fake.GetRequests()
	if second[0] == nil {
		t.Error("GetRequests returned a slice sharing backing storage; mutation leaked")
	}
}

func TestAssertions(t *testing.T) {
	fake := httpclienttest.New()
	fake.Stub(httpclienttest.MatchMethod(http.MethodGet), httpclienttest.NewResponse(http.StatusOK, nil))

	client := fake.Client(httpclient.WithoutPrivateIPDeny())
	resp, _ := client.Get(context.Background(), "https://api.example.com/users/1")
	resp.Body.Close()

	fake.AssertSent(t, httpclienttest.MatchURL("/users/1"))
	fake.AssertNotSent(t, httpclienttest.MatchURL("/orders"))

	empty := httpclienttest.New()
	empty.AssertNothingSent(t)
}

// TestAssertionFailures verifies the Assert* helpers report via t.Errorf
// rather than panicking, by running them against a recording *testing.T.
func TestAssertionFailures(t *testing.T) {
	fake := httpclienttest.New()
	fake.Stub(httpclienttest.MatchMethod(http.MethodGet), httpclienttest.NewResponse(http.StatusOK, nil))
	client := fake.Client(httpclient.WithoutPrivateIPDeny())
	resp, _ := client.Get(context.Background(), "https://api.example.com/a")
	resp.Body.Close()

	t.Run("AssertSent miss fails", func(t *testing.T) {
		var spy testing.T
		fake.AssertSent(&spy, httpclienttest.MatchURL("/nope"))
		if !spy.Failed() {
			t.Error("expected AssertSent to fail")
		}
	})

	t.Run("AssertNotSent hit fails", func(t *testing.T) {
		var spy testing.T
		fake.AssertNotSent(&spy, httpclienttest.MatchURL("/a"))
		if !spy.Failed() {
			t.Error("expected AssertNotSent to fail")
		}
	})

	t.Run("AssertNothingSent with sends fails", func(t *testing.T) {
		var spy testing.T
		fake.AssertNothingSent(&spy)
		if !spy.Failed() {
			t.Error("expected AssertNothingSent to fail")
		}
	})
}

func TestConcurrentSends(t *testing.T) {
	fake := httpclienttest.New()
	fake.Stub(httpclienttest.MatchURL("/race"), httpclienttest.NewResponse(http.StatusOK, []byte("shared-body")))

	client := fake.Client(httpclient.WithoutPrivateIPDeny())

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			resp, err := client.Get(context.Background(), "https://api.example.com/race")
			if err != nil {
				t.Errorf("Get: %v", err)
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Errorf("ReadAll: %v", err)
				return
			}
			if string(body) != "shared-body" {
				t.Errorf("body = %q, want %q", body, "shared-body")
			}
		}()
	}
	wg.Wait()

	if got := len(fake.GetRequests()); got != n {
		t.Errorf("recorded %d requests, want %d", got, n)
	}
}
