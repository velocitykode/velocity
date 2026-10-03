package velocity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/notification"
	// The database and mail channel drivers, as an app blank-imports them.
	_ "github.com/velocitykode/velocity/notification/database"
	_ "github.com/velocitykode/velocity/notification/mail"
	"github.com/velocitykode/velocity/router"
)

// sessionAppEnv sets the env of a starter-template app with the session
// scheme as the default.
func sessionAppEnv(t *testing.T, extra map[string]string) {
	t.Helper()
	env := map[string]string{
		"APP_ENV":        "local",
		"APP_KEY":        strings.Repeat("k", 32),
		"AUTH_SCHEME":    "web",
		"LOG_DRIVER":     "null",
		"CACHE_DRIVER":   "memory",
		"QUEUE_DRIVER":   "memory",
		"MAIL_DRIVER":    "log",
		"SESSION_SECURE": "false",
	}
	for k, v := range extra {
		env[k] = v
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func TestRebind_CryptoSwapSealsSessionsUnderTheNewKey(t *testing.T) {
	sessionAppEnv(t, nil)
	cfg := ConfigFromEnv()
	next, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("n", 32), Cipher: cfg.Crypto.Cipher})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	var boot contract.Encryptor
	a, err := New(WithConfig(cfg), WithModules(swapIn(func(s *app.Services) {
		boot = s.Crypto
		s.Crypto = next
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	a.Router.Get("/put", func(c *router.Context) error {
		schemes.SessionFromContext(c.Request.Context()).Put("k", "v")
		return c.String(http.StatusOK, "ok")
	})
	rec := httptest.NewRecorder()
	a.Router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/put", nil))
	var sealed string
	for _, c := range rec.Result().Cookies() {
		if c.Name == a.config.Session.Name {
			sealed = c.Value
		}
	}
	if sealed == "" {
		t.Fatalf("no session cookie set (status %d)", rec.Code)
	}
	if _, err := next.Decrypt(sealed); err != nil {
		t.Fatalf("the replacement cannot open the session cookie: %v", err)
	}
	if _, err := boot.Decrypt(sealed); err == nil {
		t.Fatal("the session cookie is still sealed under the boot key")
	}
}

func TestRebind_CryptoReplacedWithNoneIsRefused(t *testing.T) {
	sessionAppEnv(t, nil)
	_, err := New(WithConfig(ConfigFromEnv()), WithModules(swapIn(func(s *app.Services) {
		s.Crypto = nil
	})))
	if err == nil || !strings.Contains(err.Error(), "Services.Crypto") || !strings.Contains(err.Error(), "session scheme") {
		t.Fatalf("New = %v, want the refusal naming Services.Crypto and the session scheme", err)
	}
}

// countingMailer records the messages sent through it.
type countingMailer struct{ sent int }

func (m *countingMailer) Send(context.Context, *contract.Message) error { m.sent++; return nil }

// mailNote is a notification sent through the mail channel.
type mailNote struct{}

func (mailNote) Via(any) []string { return []string{"mail"} }
func (mailNote) ToMail(any) *notification.MailMessage {
	return notification.NewMailMessage().To("a@example.test").Subject("s").Line("l")
}

func TestRebind_NotificationMailChannelFollowsMail(t *testing.T) {
	next := &countingMailer{}
	a, err := NewTestApp(WithModules(swapIn(func(s *app.Services) { s.Mail = next })))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	if err := a.Notification.Send(context.Background(), struct{}{}, mailNote{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if next.sent != 1 {
		t.Fatalf("the replacement mailer sent %d messages, want 1", next.sent)
	}
}

// plainCache is a cache manager whose default store has the base
// operations only: no compare-and-swap, no set operations.
type plainCache struct {
	contract.CacheManager
	store contract.CacheStore
}

type plainStore struct{ contract.CacheStore }

func (c *plainCache) DefaultStore() (contract.CacheStore, error) { return c.store, nil }

func TestRebind_ServerSessionStoreFollowsCache(t *testing.T) {
	sessionAppEnv(t, map[string]string{"SESSION_STORE": "server"})
	var next contract.CacheStore
	a, err := New(WithConfig(ConfigFromEnv()), WithModules(swapIn(func(s *app.Services) {
		cm := initCache(CacheConfig{Driver: "memory", Prefix: "swapped"}, s.Log)
		next, _ = cm.DefaultStore()
		s.Cache = cm
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shutdownApp(t, a)
	if got := a.sessionCacheCell.load(); any(got) != any(next) {
		t.Fatalf("server session store keeps records in %p, want the replacement's default store %p", got, next)
	}
}

func TestRebind_ServerSessionStoreRefusesAnIncapableCache(t *testing.T) {
	sessionAppEnv(t, map[string]string{"SESSION_STORE": "server"})
	_, err := New(WithConfig(ConfigFromEnv()), WithModules(swapIn(func(s *app.Services) {
		boot, _ := s.Cache.DefaultStore()
		s.Cache = &plainCache{CacheManager: s.Cache, store: plainStore{boot}}
	})))
	if !errors.Is(err, session.ErrCacheStoreUnsupported) || !strings.Contains(err.Error(), "Services.Cache") || !strings.Contains(err.Error(), "compare-and-swap") {
		t.Fatalf("New = %v, want the refusal naming Services.Cache and the capability", err)
	}
}

// noopEncryptor and noopStore answer without work, so a benchmark through
// a forwarder measures the forwarder.
type noopEncryptor struct{ contract.Encryptor }

func (noopEncryptor) Decrypt(payload string) (string, error) { return payload, nil }

type noopSessionBackend struct{ sessionCacheBackend }

func (noopSessionBackend) GetStringCtx(context.Context, string) (string, bool) { return "", false }

// BenchmarkBindingCell_Forward measures one call through each forwarder:
// one atomic load and no allocation.
func BenchmarkBindingCell_Forward(b *testing.B) {
	var ec bindingCell[contract.Encryptor]
	ec.store(noopEncryptor{})
	enc := appEncryptor{cell: &ec}
	var mc bindingCell[contract.Mailer]
	mc.store(&countingMailer{})
	mailer := appMailer{cell: &mc}
	var sc bindingCell[sessionCacheBackend]
	sc.store(noopSessionBackend{})
	store := appSessionCache{cell: &sc}
	ctx := context.Background()
	b.Run("encryptor", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = enc.Decrypt("x")
		}
	})
	b.Run("mailer", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = mailer.Send(ctx, nil)
		}
	})
	b.Run("session_cache", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = store.GetStringCtx(ctx, "k")
		}
	})
	b.Run("encryptor_parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, _ = enc.Decrypt("x")
			}
		})
	})
}

// idMailer sends nothing; distinct instances tell stores apart.
type idMailer struct{ _ [1]byte }

func (*idMailer) Send(context.Context, *contract.Message) error { return nil }

// TestBindingCell_ConcurrentStoreAndLoad stores replacements while
// forwarders read: each read sees one stored instance whole (run with
// -race).
func TestBindingCell_ConcurrentStoreAndLoad(t *testing.T) {
	var c bindingCell[contract.Mailer]
	mailers := []*idMailer{{}, {}, {}}
	c.store(mailers[0])
	m := appMailer{cell: &c}
	done := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				got := c.load()
				if got != mailers[0] && got != mailers[1] && got != mailers[2] {
					t.Errorf("load = %p, not a stored mailer", got)
					return
				}
				_ = m.Send(context.Background(), nil)
			}
		}()
	}
	for i := range 2000 {
		c.store(mailers[i%len(mailers)])
	}
	close(done)
	wg.Wait()
}
