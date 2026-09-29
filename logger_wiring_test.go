package velocity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/trace"
	"github.com/velocitykode/velocity/view"
)

// newLoggerWiringApp builds a testing-env app whose logger, Services.Log,
// is the returned capture. adjust edits the config before New.
func newLoggerWiringApp(t *testing.T, adjust func(*Config), opts ...Option) (*App, *levelLogger) {
	t.Helper()
	capture := &levelLogger{}
	const driverName = "logger-wiring-capture"
	prev := log.Drivers().Override(driverName, func(context.Context, log.LogConfig) (log.Logger, error) {
		return capture, nil
	})
	t.Cleanup(func() { log.Drivers().Override(driverName, prev) })

	cfg := Config{
		Env:   "testing",
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "logger_wiring"},
		Log:   log.LogConfig{Driver: driverName, Config: make(map[string]any)},
		Queue: QueueConfig{Driver: "memory"},
		Mail:  mail.MailConfig{Driver: "log"},
	}
	if adjust != nil {
		adjust(&cfg)
	}
	a, err := New(append([]Option{WithConfig(cfg)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	return a, capture
}

// entriesStartingWith counts l's entries at level whose message starts
// with prefix.
func entriesStartingWith(l *levelLogger, level, prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.entries {
		if e.level == level && strings.HasPrefix(e.msg, prefix) {
			n++
		}
	}
	return n
}

// In a bootstrapped app, a rollback that fails after a panic in an ORM
// transaction is one error line through the app logger.
func TestNew_ORMRollbackFailureAfterPanicLogsThroughTheAppLogger(t *testing.T) {
	a, capture := newLoggerWiringApp(t, func(c *Config) {
		c.DB = DBConfig{Connection: "sqlite", Database: ":memory:"}
	})
	mgr, ok := a.DB.(*orm.Manager)
	if !ok {
		t.Fatalf("a.DB = %T, want *orm.Manager", a.DB)
	}

	func() {
		defer func() { _ = recover() }()
		_ = mgr.Transaction(context.Background(), func(ctx context.Context) error {
			tx, _ := orm.TxFromContext(ctx)
			_ = tx.Rollback() // the manager's own rollback after the panic now fails
			panic("body blew up")
		})
	}()

	if got := entriesWith(capture, "error", "velocity/orm: rollback failed after panic"); got != 1 {
		t.Errorf("app logger error lines = %d, want 1 (%+v)", got, capture.entries)
	}
}

// In a bootstrapped app with no RedirectAllowedHosts, bond's
// redirect-allowlist fallback warning is one line through the app logger.
func TestNew_BondRedirectAllowlistFallbackLogsThroughTheAppLogger(t *testing.T) {
	a, capture := newLoggerWiringApp(t, func(c *Config) {
		c.View = view.Config{ErrorPage: "Error"}
	})
	a.Router.Get("/back", func(c *router.Context) error {
		c.View().Back(c.Response, c.Request)
		return nil
	})

	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodGet, "http://app.example/back", nil)
		r.Header.Set("Referer", "http://app.example/previous")
		a.Router.ServeHTTP(httptest.NewRecorder(), r)
	}

	if got := entriesStartingWith(capture, "warn", "velocity/bond: no RedirectAllowlist configured"); got != 1 {
		t.Errorf("app logger warn lines = %d, want 1 (%+v)", got, capture.entries)
	}
}

// loggerWiringRedisJob is a registered job without a stable JobID, so the
// redis queue driver's advisory fires the first time one is popped.
type loggerWiringRedisJob struct {
	ID string `json:"id"`
}

func (*loggerWiringRedisJob) Handle() error { return nil }
func (*loggerWiringRedisJob) Failed(error)  {}

func init() {
	queue.RegisterJob(func(data []byte) (*loggerWiringRedisJob, error) {
		var j loggerWiringRedisJob
		if err := json.Unmarshal(data, &j); err != nil {
			return nil, err
		}
		return &j, nil
	})
}

// In a bootstrapped app on the redis queue driver, the driver's advisory
// is one line through the app logger.
func TestNew_RedisQueueAdvisoryLogsThroughTheAppLogger(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	a, capture := newLoggerWiringApp(t, func(c *Config) {
		c.Queue = QueueConfig{Driver: "redis", RedisHost: mr.Host(), RedisPort: mr.Port(), RedisDB: "0"}
	})

	ctx := context.Background()
	if err := a.Queue.PushCtx(ctx, &loggerWiringRedisJob{ID: "1"}, "logger-wiring"); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}
	if _, err := a.Queue.PopCtx(ctx, "logger-wiring"); err != nil {
		t.Fatalf("PopCtx: %v", err)
	}

	if got := entriesStartingWith(capture, "warn", "velocity/queue: job type does not implement Identifiable"); got != 1 {
		t.Errorf("app logger warn lines = %d, want 1 (%+v)", got, capture.entries)
	}
}

// Every exported interface field of app.Services is offered the app logger
// by the logger sweep, under its field name and holding the field's value:
// a new Services field a framework value with a logger seam can occupy
// fails here by name until the sweep covers it. Log is the logger itself;
// RedirectAllowlist is the router, which holds a forwarder to the current
// Services.Log (see appLogger) and is wired directly.
func TestLoggerWiringCandidates_CoverEveryServicesField(t *testing.T) {
	a, _ := newLoggerWiringApp(t, nil)

	candidates := map[string]any{}
	for _, c := range loggerWiringCandidates(a) {
		if _, dup := candidates[c.name]; dup {
			t.Errorf("candidate %q listed twice", c.name)
		}
		candidates[c.name] = c.value
	}
	st := reflect.TypeOf(app.Services{})
	sv := reflect.ValueOf(a.Services).Elem()
	seen := map[string]bool{}
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.IsExported() || f.Type.Kind() != reflect.Interface {
			continue
		}
		seen[f.Name] = true
		if f.Name == "Log" || f.Name == "RedirectAllowlist" {
			if _, listed := candidates[f.Name]; listed {
				t.Errorf("Services.%s must not be a logger candidate", f.Name)
			}
			continue
		}
		v, listed := candidates[f.Name]
		if !listed {
			t.Errorf("Services.%s is not offered the app logger (add it to loggerWiringCandidates)", f.Name)
			continue
		}
		if fv := sv.Field(i).Interface(); !sameServiceValue(v, fv) {
			t.Errorf("candidate %q = %T, want Services.%s (%T)", f.Name, v, f.Name, fv)
		}
	}
	for name := range candidates {
		if !seen[name] {
			t.Errorf("candidate %q names no Services interface field", name)
		}
	}
}

// sameServiceValue reports whether a and b are the same service value:
// both nil, or the same pointer.
func sameServiceValue(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Type() != vb.Type() {
		return false
	}
	if va.Kind() == reflect.Pointer {
		return va.Pointer() == vb.Pointer()
	}
	return reflect.DeepEqual(a, b)
}

// The framework-built values in Services that write operational lines
// implement the logger facet, so the sweep reaches them.
func TestNew_FrameworkBuiltServicesAreLoggerAware(t *testing.T) {
	a, _ := newLoggerWiringApp(t, func(c *Config) {
		c.DB = DBConfig{Connection: "sqlite", Database: ":memory:"}
		c.View = view.Config{ErrorPage: "Error"}
		c.Crypto = crypto.Config{Key: "0123456789abcdef0123456789abcdef", Cipher: "AES-256-GCM"}
	})
	for name, v := range map[string]any{
		"DB": a.DB, "View": a.View, "Queue": a.Queue, "Scheduler": a.Scheduler, "Auth": a.Auth,
		"Crypto": a.Crypto, "CSRF": a.CSRF, "Cache": a.Cache, "Mail": a.Mail, "Notification": a.Notification,
	} {
		if _, ok := v.(contract.LoggerAware); !ok {
			t.Errorf("Services.%s (%T) does not implement contract.LoggerAware", name, v)
		}
	}
}

// loggerProbe is a Services.Validator that records the logger the sweep
// hands it.
type loggerProbe struct {
	contract.Validator
	mu  sync.Mutex
	got []contract.Logger
}

func (p *loggerProbe) SetLogger(l contract.Logger) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, l)
}

func (p *loggerProbe) last() contract.Logger {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.got) == 0 {
		return nil
	}
	return p.got[len(p.got)-1]
}

// loggerSwapModule installs probe as Services.Validator in Init and
// replaces Services.Log with swapped in Start.
type loggerSwapModule struct {
	probe   *loggerProbe
	swapped contract.Logger
}

func (m loggerSwapModule) Init(s *app.Services) error {
	s.Validator = m.probe
	return nil
}

func (m loggerSwapModule) Start(s *app.Services) error {
	s.Log = m.swapped
	return nil
}

func (loggerSwapModule) Shutdown(context.Context) error { return nil }

// The sweep hands Services.Log to every logger-aware service and to the
// async package, and hands it again when a module replaces Services.Log:
// after New, both hold the module's logger.
func TestNew_ReHandsTheLoggerAModuleSwapsIn(t *testing.T) {
	prevAsync, prevTrace := async.GetLogger(), trace.GetLogger()
	t.Cleanup(func() { async.SetLogger(prevAsync); trace.SetLogger(prevTrace) })
	probe := &loggerProbe{}
	swapped := &levelLogger{}

	a, _ := newLoggerWiringApp(t, nil, WithModules(loggerSwapModule{probe: probe, swapped: swapped}))

	if got := probe.last(); got != contract.Logger(swapped) {
		t.Errorf("probe last got %T, want the swapped logger", got)
	}
	if got := async.GetLogger(); got != contract.Logger(swapped) {
		t.Errorf("async logger = %T, want the swapped logger", got)
	}
	if got := trace.GetLogger(); got != contract.Logger(swapped) {
		t.Errorf("trace logger = %T, want the swapped logger", got)
	}
	if a.Log != contract.Logger(swapped) {
		t.Fatalf("a.Log = %T, want the swapped logger", a.Log)
	}
}

// App.Shutdown takes the app logger back out of the async and trace
// packages, so neither writes to the app logger it closes: they return to
// the logger installed before the app (another live app's, or the
// standalone fallback logger when none).
func TestShutdown_PutsThePackageLoggersBackOnTheFallback(t *testing.T) {
	prevAsync, prevTrace := async.GetLogger(), trace.GetLogger()
	t.Cleanup(func() { async.SetLogger(prevAsync); trace.SetLogger(prevTrace) })

	a, capture := newLoggerWiringApp(t, nil)
	if got := trace.GetLogger(); got != contract.Logger(capture) {
		t.Fatalf("trace logger before Shutdown = %T, want the app logger", got)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := trace.GetLogger(); got != prevTrace {
		t.Errorf("trace logger after Shutdown = %T, want the one installed before the app (%T)", got, prevTrace)
	}
	if got := async.GetLogger(); got != prevAsync {
		t.Errorf("async logger after Shutdown = %T, want the one installed before the app (%T)", got, prevAsync)
	}
	packageStateMu.Lock()
	unowned := len(packageStack) == 0
	packageStateMu.Unlock()
	if _, ok := trace.GetLogger().(fallbacklog.Logger); unowned && !ok {
		t.Errorf("trace logger after Shutdown = %T, want fallbacklog.Logger with no live app", trace.GetLogger())
	}
}
