package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
)

// memoryFlashBag is a contract.FlashBag held in memory.
type memoryFlashBag struct {
	mu      sync.Mutex
	entries map[string]any
}

func (b *memoryFlashBag) Flash(key string, value any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		b.entries = map[string]any{}
	}
	b.entries[key] = value
}

func (b *memoryFlashBag) GetFlash(key string) any {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := b.entries[key]
	delete(b.entries, key)
	return v
}

func (b *memoryFlashBag) FlushFlash() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.entries
	b.entries = nil
	if len(out) == 0 {
		return nil
	}
	return out
}

// warnLog records Warn calls; the other levels are discarded.
type warnLog struct {
	mu    sync.Mutex
	warns []string
}

func (l *warnLog) Debug(string, ...any) {}
func (l *warnLog) Info(string, ...any)  {}
func (l *warnLog) Error(string, ...any) {}
func (l *warnLog) Fatal(string, ...any) {}
func (l *warnLog) Warn(msg string, kvs ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprint(append([]any{msg}, kvs...)...))
}

// flashContext returns a context whose services hand out bag as the
// request's session flash bag (nil: the request carries no session).
func flashContext(bag contract.FlashBag, log contract.Logger) (*Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c := NewContext(w, httptest.NewRequest(http.MethodPost, "/", nil))
	c.SetServices(&app.Services{
		Log: log,
		FlashBag: func(*http.Request) contract.FlashBag {
			if bag == nil {
				return nil
			}
			return bag
		},
	})
	return c, w
}

// TestContextFlash_IntoTheSessionFlashBag asserts FlashErrors and
// FlashInput store their value, in its JSON form, under FlashErrorsKey and
// FlashInputKey in the session flash bag and write no cookie.
func TestContextFlash_IntoTheSessionFlashBag(t *testing.T) {
	tests := []struct {
		name  string
		flash func(c *Context)
		key   string
		want  any
	}{
		{
			name:  "errors field map",
			flash: func(c *Context) { c.FlashErrors(map[string][]string{"email": {"required", "email"}}) },
			key:   FlashErrorsKey,
			want:  map[string]any{"email": "required"},
		},
		{
			name: "errors bag",
			flash: func(c *Context) {
				c.FlashErrors(&bagFailure{bag: "login", fields: map[string][]string{"email": {"required"}}})
			},
			key:  FlashErrorsKey,
			want: map[string]any{"email": "required", "login": map[string]any{"email": "required"}},
		},
		{
			name:  "old input in its JSON form",
			flash: func(c *Context) { c.FlashInput(map[string]any{"email": "bad@", "age": 7}) },
			key:   FlashInputKey,
			want:  map[string]any{"email": "bad@", "age": 7.0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bag := &memoryFlashBag{}
			c, w := flashContext(bag, nil)
			tt.flash(c)
			if got := bag.GetFlash(tt.key); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("bag[%q] = %#v, want %#v", tt.key, got, tt.want)
			}
			if cookies := w.Result().Cookies(); len(cookies) != 0 {
				t.Errorf("flash wrote cookies %#v; flash rides in the session only", cookies)
			}
		})
	}
}

// TestContextFlash_WithoutSessionIsDroppedWithWarning asserts a request
// without a session flashes nothing, writes no cookie and logs a warning,
// and that raw contexts without services do not panic.
func TestContextFlash_WithoutSessionIsDroppedWithWarning(t *testing.T) {
	log := &warnLog{}
	c, w := flashContext(nil, log)
	c.FlashErrors(map[string]string{"email": "required"})
	c.FlashInput(map[string]any{"email": "bad@"})
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("flash without a session wrote cookies %#v", cookies)
	}
	if len(log.warns) != 2 {
		t.Errorf("warnings = %q, want one per dropped flash", log.warns)
	}

	raw := NewContext(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	raw.FlashErrors(map[string]string{"email": "required"})
	raw.FlashInput(map[string]any{"email": "bad@"})
}

// TestContextFlash_UnencodableValueIsDropped asserts a value that does not
// encode as JSON flashes nothing and logs a warning instead of failing the
// handler.
func TestContextFlash_UnencodableValueIsDropped(t *testing.T) {
	bag := &memoryFlashBag{}
	log := &warnLog{}
	c, _ := flashContext(bag, log)
	c.FlashInput(map[string]any{"ch": make(chan int)})
	if got := bag.FlushFlash(); got != nil {
		t.Errorf("bag = %#v, want nothing flashed", got)
	}
	if len(log.warns) != 1 {
		t.Errorf("warnings = %q, want one", log.warns)
	}
}
