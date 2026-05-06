package drivers

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStore is a minimal in-memory implementation of CacheGetter + CachePutter
// used to drive the helper unit tests without spinning up a real driver.
// It also lets a test override Put/Forever to inject errors.
type fakeStore struct {
	mu       sync.Mutex
	data     map[string]interface{}
	putErr   error
	foreverE error
	puts     int
	forevers int
}

func newFakeStore() *fakeStore {
	return &fakeStore{data: make(map[string]interface{})}
}

func (f *fakeStore) Get(key string) (interface{}, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[key]
	return v, ok
}

func (f *fakeStore) Put(key string, value interface{}, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	if f.putErr != nil {
		return f.putErr
	}
	f.data[key] = value
	return nil
}

func (f *fakeStore) Forever(key string, value interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forevers++
	if f.foreverE != nil {
		return f.foreverE
	}
	f.data[key] = value
	return nil
}

var errFakeBoom = errors.New("fake boom")

func TestRememberFromE(t *testing.T) {
	t.Run("HappyPath_PutsAndReturns", func(t *testing.T) {
		s := newFakeStore()
		var calls int32
		val, err := RememberFromE(s, s, "k", time.Hour, func() (interface{}, error) {
			atomic.AddInt32(&calls, 1)
			return "ok", nil
		})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if val != "ok" {
			t.Fatalf("val = %v, want ok", val)
		}
		if got, _ := s.Get("k"); got != "ok" {
			t.Fatalf("store get = %v, want ok", got)
		}
		if s.puts != 1 {
			t.Fatalf("puts = %d, want 1", s.puts)
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1", calls)
		}
	})

	t.Run("CallbackError_NoPut", func(t *testing.T) {
		s := newFakeStore()
		_, err := RememberFromE(s, s, "k", time.Hour, func() (interface{}, error) {
			return "ignored", errFakeBoom
		})
		if !errors.Is(err, errFakeBoom) {
			t.Fatalf("err = %v, want errFakeBoom", err)
		}
		if s.puts != 0 {
			t.Fatalf("puts = %d on error, want 0", s.puts)
		}
		if _, ok := s.Get("k"); ok {
			t.Fatal("store wrote a value despite callback error")
		}
	})

	t.Run("PutError_Surfaces", func(t *testing.T) {
		s := newFakeStore()
		s.putErr = errFakeBoom
		_, err := RememberFromE(s, s, "k", time.Hour, func() (interface{}, error) {
			return "ok", nil
		})
		if !errors.Is(err, errFakeBoom) {
			t.Fatalf("err = %v, want errFakeBoom", err)
		}
	})

	t.Run("CacheHit_SkipsCallback", func(t *testing.T) {
		s := newFakeStore()
		s.data["k"] = "preloaded"
		var calls int32
		val, err := RememberFromE(s, s, "k", time.Hour, func() (interface{}, error) {
			atomic.AddInt32(&calls, 1)
			return "should-not-run", nil
		})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if val != "preloaded" {
			t.Fatalf("val = %v, want preloaded", val)
		}
		if calls != 0 {
			t.Fatalf("calls = %d, want 0", calls)
		}
		if s.puts != 0 {
			t.Fatalf("puts = %d, want 0", s.puts)
		}
	})
}

func TestRememberForeverFromE(t *testing.T) {
	t.Run("HappyPath_ForeverPath", func(t *testing.T) {
		s := newFakeStore()
		val, err := RememberForeverFromE(s, s, "k", func() (interface{}, error) {
			return "ok", nil
		})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if val != "ok" {
			t.Fatalf("val = %v, want ok", val)
		}
		if s.forevers != 1 {
			t.Fatalf("forevers = %d, want 1", s.forevers)
		}
	})

	t.Run("CallbackError_NoForever", func(t *testing.T) {
		s := newFakeStore()
		_, err := RememberForeverFromE(s, s, "k", func() (interface{}, error) {
			return nil, errFakeBoom
		})
		if !errors.Is(err, errFakeBoom) {
			t.Fatalf("err = %v, want errFakeBoom", err)
		}
		if s.forevers != 0 {
			t.Fatalf("forevers = %d on error, want 0", s.forevers)
		}
	})

	t.Run("ForeverError_Surfaces", func(t *testing.T) {
		s := newFakeStore()
		s.foreverE = errFakeBoom
		_, err := RememberForeverFromE(s, s, "k", func() (interface{}, error) {
			return "ok", nil
		})
		if !errors.Is(err, errFakeBoom) {
			t.Fatalf("err = %v, want errFakeBoom", err)
		}
	})
}
