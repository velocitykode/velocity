package httpclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	t.Run("default client", func(t *testing.T) {
		c := New()
		if c == nil {
			t.Fatal("New() returned nil")
		}
		if c.client == nil {
			t.Error("client.client is nil")
		}
	})

	t.Run("with custom http client", func(t *testing.T) {
		customClient := &http.Client{Timeout: 60 * time.Second}
		c := New(WithHTTPClient(customClient))
		if c.client != customClient {
			t.Error("WithHTTPClient did not set custom client")
		}
	})

	t.Run("with base URL", func(t *testing.T) {
		c := New(WithBaseURL("https://api.example.com"))
		if c.baseURL != "https://api.example.com" {
			t.Errorf("baseURL = %q, want %q", c.baseURL, "https://api.example.com")
		}
	})

	t.Run("with timeout", func(t *testing.T) {
		c := New(WithTimeout(5 * time.Second))
		if c.client.Timeout != 5*time.Second {
			t.Errorf("Timeout = %v, want %v", c.client.Timeout, 5*time.Second)
		}
	})
}

func TestClientDo(t *testing.T) {
	var sentEvents []*RequestSent
	var failedEvents []*RequestFailed
	var mu sync.Mutex

	client := New(WithoutPrivateIPDeny())
	client.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		switch e := event.(type) {
		case *RequestSent:
			sentEvents = append(sentEvents, e)
		case *RequestFailed:
			failedEvents = append(failedEvents, e)
		}
		return nil
	})

	t.Run("successful request", func(t *testing.T) {
		mu.Lock()
		sentEvents = nil
		failedEvents = nil
		mu.Unlock()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "13")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Hello, World!"))
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL+"/test", nil)
		resp, err := client.Do(context.Background(), req)

		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
		}
		resp.Body.Close()

		mu.Lock()
		defer mu.Unlock()

		if len(sentEvents) != 1 {
			t.Fatalf("expected 1 sent event, got %d", len(sentEvents))
		}
		if sentEvents[0].Method != "GET" {
			t.Errorf("Method = %q, want %q", sentEvents[0].Method, "GET")
		}
		if sentEvents[0].StatusCode != 200 {
			t.Errorf("StatusCode = %d, want 200", sentEvents[0].StatusCode)
		}
		if len(failedEvents) != 0 {
			t.Errorf("expected 0 failed events, got %d", len(failedEvents))
		}
	})

	t.Run("failed request", func(t *testing.T) {
		mu.Lock()
		sentEvents = nil
		failedEvents = nil
		mu.Unlock()

		failClient := New(WithTimeout(100*time.Millisecond), WithoutPrivateIPDeny())
		failClient.SetEventDispatcher(func(_ context.Context, event interface{}) error {
			mu.Lock()
			defer mu.Unlock()
			switch e := event.(type) {
			case *RequestSent:
				sentEvents = append(sentEvents, e)
			case *RequestFailed:
				failedEvents = append(failedEvents, e)
			}
			return nil
		})

		req, _ := http.NewRequest("GET", "http://localhost:59999/nonexistent", nil)
		_, err := failClient.Do(context.Background(), req)

		if err == nil {
			t.Fatal("Do() expected error, got nil")
		}

		mu.Lock()
		defer mu.Unlock()

		if len(failedEvents) != 1 {
			t.Fatalf("expected 1 failed event, got %d", len(failedEvents))
		}
		if failedEvents[0].Method != "GET" {
			t.Errorf("Method = %q, want %q", failedEvents[0].Method, "GET")
		}
		if failedEvents[0].Error == "" {
			t.Error("Error should not be empty")
		}
		if len(sentEvents) != 0 {
			t.Errorf("expected 0 sent events, got %d", len(sentEvents))
		}
	})
}

func TestClientGet(t *testing.T) {
	var sentEvents []*RequestSent
	var mu sync.Mutex

	client := New(WithoutPrivateIPDeny())
	client.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		if e, ok := event.(*RequestSent); ok {
			sentEvents = append(sentEvents, e)
		}
		return nil
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("Method = %q, want GET", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	resp, err := client.Get(context.Background(), server.URL+"/users")

	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(sentEvents) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sentEvents))
	}
	if sentEvents[0].Method != "GET" {
		t.Errorf("Method = %q, want GET", sentEvents[0].Method)
	}
}

func TestClientPost(t *testing.T) {
	var sentEvents []*RequestSent
	var mu sync.Mutex

	client := New(WithoutPrivateIPDeny())
	client.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		if e, ok := event.(*RequestSent); ok {
			sentEvents = append(sentEvents, e)
		}
		return nil
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Method = %q, want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"name":"test"}` {
			t.Errorf("Body = %q, want %q", string(body), `{"name":"test"}`)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	resp, err := client.Post(context.Background(), server.URL+"/users", "application/json", strings.NewReader(`{"name":"test"}`))

	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(sentEvents) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sentEvents))
	}
	if sentEvents[0].Method != "POST" {
		t.Errorf("Method = %q, want POST", sentEvents[0].Method)
	}
	if sentEvents[0].StatusCode != 201 {
		t.Errorf("StatusCode = %d, want 201", sentEvents[0].StatusCode)
	}
}

func TestClientPut(t *testing.T) {
	var sentEvents []*RequestSent
	var mu sync.Mutex

	client := New(WithoutPrivateIPDeny())
	client.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		if e, ok := event.(*RequestSent); ok {
			sentEvents = append(sentEvents, e)
		}
		return nil
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("Method = %q, want PUT", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	resp, err := client.Put(context.Background(), server.URL+"/users/1", "application/json", strings.NewReader(`{"name":"updated"}`))

	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(sentEvents) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sentEvents))
	}
	if sentEvents[0].Method != "PUT" {
		t.Errorf("Method = %q, want PUT", sentEvents[0].Method)
	}
}

func TestClientDelete(t *testing.T) {
	var sentEvents []*RequestSent
	var mu sync.Mutex

	client := New(WithoutPrivateIPDeny())
	client.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		if e, ok := event.(*RequestSent); ok {
			sentEvents = append(sentEvents, e)
		}
		return nil
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("Method = %q, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	resp, err := client.Delete(context.Background(), server.URL+"/users/1")

	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(sentEvents) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sentEvents))
	}
	if sentEvents[0].Method != "DELETE" {
		t.Errorf("Method = %q, want DELETE", sentEvents[0].Method)
	}
	if sentEvents[0].StatusCode != 204 {
		t.Errorf("StatusCode = %d, want 204", sentEvents[0].StatusCode)
	}
}

func TestClientPatch(t *testing.T) {
	var sentEvents []*RequestSent
	var mu sync.Mutex

	client := New(WithoutPrivateIPDeny())
	client.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		if e, ok := event.(*RequestSent); ok {
			sentEvents = append(sentEvents, e)
		}
		return nil
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" {
			t.Errorf("Method = %q, want PATCH", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	resp, err := client.Patch(context.Background(), server.URL+"/users/1", "application/json", strings.NewReader(`{"status":"active"}`))

	if err != nil {
		t.Fatalf("Patch() error = %v", err)
	}
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(sentEvents) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sentEvents))
	}
	if sentEvents[0].Method != "PATCH" {
		t.Errorf("Method = %q, want PATCH", sentEvents[0].Method)
	}
}

func TestResolveURL(t *testing.T) {
	t.Run("without base URL", func(t *testing.T) {
		client := New()
		url := client.resolveURL("https://api.example.com/users")
		if url != "https://api.example.com/users" {
			t.Errorf("resolveURL() = %q, want %q", url, "https://api.example.com/users")
		}
	})

	t.Run("with base URL and absolute path", func(t *testing.T) {
		client := New(WithBaseURL("https://api.example.com"))
		url := client.resolveURL("/users")
		if url != "https://api.example.com/users" {
			t.Errorf("resolveURL() = %q, want %q", url, "https://api.example.com/users")
		}
	})

	t.Run("with base URL and full URL", func(t *testing.T) {
		client := New(WithBaseURL("https://api.example.com"))
		url := client.resolveURL("https://other.example.com/resource")
		if url != "https://other.example.com/resource" {
			t.Errorf("resolveURL() = %q, want %q", url, "https://other.example.com/resource")
		}
	})

	t.Run("with base URL and empty path", func(t *testing.T) {
		client := New(WithBaseURL("https://api.example.com"))
		url := client.resolveURL("")
		if url != "" {
			t.Errorf("resolveURL() = %q, want empty string", url)
		}
	})
}

func TestClientMethods(t *testing.T) {
	client := New(WithoutPrivateIPDeny())
	client.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		return nil
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Run("Get", func(t *testing.T) {
		resp, err := client.Get(context.Background(), server.URL)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		resp.Body.Close()
	})

	t.Run("Post", func(t *testing.T) {
		resp, err := client.Post(context.Background(), server.URL, "text/plain", strings.NewReader("test"))
		if err != nil {
			t.Fatalf("Post() error = %v", err)
		}
		resp.Body.Close()
	})

	t.Run("Put", func(t *testing.T) {
		resp, err := client.Put(context.Background(), server.URL, "text/plain", strings.NewReader("test"))
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}
		resp.Body.Close()
	})

	t.Run("Delete", func(t *testing.T) {
		resp, err := client.Delete(context.Background(), server.URL)
		if err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		resp.Body.Close()
	})

	t.Run("Patch", func(t *testing.T) {
		resp, err := client.Patch(context.Background(), server.URL, "text/plain", strings.NewReader("test"))
		if err != nil {
			t.Fatalf("Patch() error = %v", err)
		}
		resp.Body.Close()
	})
}
