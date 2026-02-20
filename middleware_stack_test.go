package velocity

import (
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/router"
)

// noopMiddleware returns a no-op middleware for testing.
func noopMiddleware() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return next
	}
}

func TestMiddlewareStack_Global(t *testing.T) {
	s := &MiddlewareStack{}
	s.Global(noopMiddleware(), noopMiddleware())

	if len(s.global) != 2 {
		t.Fatalf("global: got %d, want 2", len(s.global))
	}
	if len(s.web) != 0 {
		t.Errorf("web should be empty, got %d", len(s.web))
	}
	if len(s.api) != 0 {
		t.Errorf("api should be empty, got %d", len(s.api))
	}
}

func TestMiddlewareStack_Web(t *testing.T) {
	s := &MiddlewareStack{}
	s.Web(noopMiddleware(), noopMiddleware())

	if len(s.web) != 2 {
		t.Fatalf("web: got %d, want 2", len(s.web))
	}
	if len(s.global) != 0 {
		t.Errorf("global should be empty, got %d", len(s.global))
	}
	if len(s.api) != 0 {
		t.Errorf("api should be empty, got %d", len(s.api))
	}
}

func TestMiddlewareStack_API(t *testing.T) {
	s := &MiddlewareStack{}
	s.API(noopMiddleware(), noopMiddleware())

	if len(s.api) != 2 {
		t.Fatalf("api: got %d, want 2", len(s.api))
	}
	if len(s.global) != 0 {
		t.Errorf("global should be empty, got %d", len(s.global))
	}
	if len(s.web) != 0 {
		t.Errorf("web should be empty, got %d", len(s.web))
	}
}

func TestMiddlewareStack_MultipleCalls(t *testing.T) {
	s := &MiddlewareStack{}
	s.Global(noopMiddleware())
	s.Global(noopMiddleware(), noopMiddleware())

	if len(s.global) != 3 {
		t.Fatalf("global after two calls: got %d, want 3", len(s.global))
	}

	s.Web(noopMiddleware())
	s.Web(noopMiddleware())

	if len(s.web) != 2 {
		t.Fatalf("web after two calls: got %d, want 2", len(s.web))
	}

	s.API(noopMiddleware())
	s.API(noopMiddleware())
	s.API(noopMiddleware())

	if len(s.api) != 3 {
		t.Fatalf("api after three calls: got %d, want 3", len(s.api))
	}
}

func TestMiddlewareStack_Services(t *testing.T) {
	svc := &app.Services{}
	s := &MiddlewareStack{services: svc}

	got := s.Services()
	if got != svc {
		t.Fatalf("Services() returned %p, want %p", got, svc)
	}
}

func TestMiddlewareStack_EmptyByDefault(t *testing.T) {
	s := &MiddlewareStack{}

	if len(s.global) != 0 {
		t.Errorf("global should start empty, got %d", len(s.global))
	}
	if len(s.web) != 0 {
		t.Errorf("web should start empty, got %d", len(s.web))
	}
	if len(s.api) != 0 {
		t.Errorf("api should start empty, got %d", len(s.api))
	}
	if s.Services() != nil {
		t.Errorf("services should be nil by default")
	}
}

func TestMiddlewareStack_Independent(t *testing.T) {
	a := &MiddlewareStack{}
	b := &MiddlewareStack{}

	a.Global(noopMiddleware(), noopMiddleware())
	a.Web(noopMiddleware())
	b.API(noopMiddleware(), noopMiddleware(), noopMiddleware())

	if len(a.global) != 2 {
		t.Errorf("a.global: got %d, want 2", len(a.global))
	}
	if len(a.web) != 1 {
		t.Errorf("a.web: got %d, want 1", len(a.web))
	}
	if len(a.api) != 0 {
		t.Errorf("a.api: got %d, want 0", len(a.api))
	}
	if len(b.global) != 0 {
		t.Errorf("b.global: got %d, want 0", len(b.global))
	}
	if len(b.web) != 0 {
		t.Errorf("b.web: got %d, want 0", len(b.web))
	}
	if len(b.api) != 3 {
		t.Errorf("b.api: got %d, want 3", len(b.api))
	}
}
