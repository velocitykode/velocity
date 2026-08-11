package velocity

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/internal/eventqueue"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/queue"
)

// TestServeHTTP_ListenError_RunsShutdown proves the ListenAndServe error
// branch in serveHTTP tears the app down instead of returning immediately.
// Before the fix, a port-in-use error left every bootstrapped subsystem
// (modules included) dangling, while the bootstrap-failure branch just
// above ran a full Shutdown.
func TestServeHTTP_ListenError_RunsShutdown(t *testing.T) {
	// Occupy a wildcard port so the app's ListenAndServe fails fast with
	// EADDRINUSE.
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	rec := &shutdownRecorder{}
	a, err := NewTestApp(WithModules(rec))
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	a.config.Port = strconv.Itoa(port)

	err = a.serveHTTP()
	if err == nil {
		t.Fatal("expected serveHTTP to fail when the port is occupied")
	}
	if !strings.Contains(err.Error(), "velocity: server error:") {
		t.Errorf("expected 'velocity: server error:' wrap, got: %v", err)
	}
	if got := rec.shutdowns.Load(); got != 1 {
		t.Errorf("provider Shutdown called %d times after server error, want 1", got)
	}
}

// viewShutdownProbe satisfies contract.ViewEngine and contract.ShutdownAware
// so Shutdown's view-engine step is observable.
type viewShutdownProbe struct {
	shutdowns atomic.Int32
}

func (p *viewShutdownProbe) Back(_ http.ResponseWriter, _ *http.Request) {}

func (p *viewShutdownProbe) Shutdown(_ context.Context) error {
	p.shutdowns.Add(1)
	return nil
}

// TestShutdown_TearsDownViewEngine proves App.Shutdown shuts the view engine
// down. Before the fix only the New() failure path did (via the cleanup
// stack); the happy-path Shutdown skipped the view entirely.
func TestShutdown_TearsDownViewEngine(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	probe := &viewShutdownProbe{}
	var _ contract.ViewEngine = probe
	var _ contract.ShutdownAware = probe
	a.Services.View = probe

	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := probe.shutdowns.Load(); got != 1 {
		t.Errorf("view engine Shutdown called %d times, want 1", got)
	}
}

// TestShutdown_ClearsEventQueueFailureReporter proves App.Shutdown clears the
// package-global queued-listener failure reporter wired through
// eventqueue.InitializeQueueIntegration. Before the fix only the New()
// failure path cleared it, so a torn-down app's Exceptions handler kept
// receiving Failed callbacks from the events side.
func TestShutdown_ClearsEventQueueFailureReporter(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	// Install the probe AFTER New(): New wires its own reporter and would
	// overwrite an earlier install.
	var fires atomic.Int32
	eventqueue.InitializeQueueIntegration(nil, nil, func(_ *events.EventListenerJob, _ error) {
		fires.Add(1)
	})

	// Sanity: the seam is live before Shutdown. EventListenerJob.Failed is
	// the queue driver's retry-exhausted hook and routes through the
	// package-level reporter.
	job := &events.EventListenerJob{}
	job.Failed(errors.New("pre-shutdown probe"))
	if got := fires.Load(); got != 1 {
		t.Fatalf("reporter fired %d times before Shutdown, want 1 (seam broken)", got)
	}

	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	job.Failed(errors.New("post-shutdown probe"))
	if got := fires.Load(); got != 1 {
		t.Errorf("reporter fired %d times total, want 1: Shutdown left the failure reporter installed", got)
	}
}

// TestShutdown_ClearsQueuePayloadEncryptor proves App.Shutdown uninstalls the
// process-global queue payload encryptor that initQueue wires up when
// QUEUE_ENCRYPT is enabled. Before the fix, Shutdown cleared the batch
// repository, event dispatcher, callback queue, queued-listener integration,
// and signing logger but left queue.payloadEncryptor pointing at the
// torn-down app's encryptor.
func TestShutdown_ClearsQueuePayloadEncryptor(t *testing.T) {
	resetQueueSigningState(t)
	resetQueueEncryptionState(t)
	t.Setenv("QUEUE_ACCEPT_UNSIGNED", "")

	a, err := New(WithConfig(Config{
		Env:   "testing",
		Debug: true,
		Port:  "0",
		Key:   "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		Crypto: crypto.Config{
			Key: "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		},
		Cache: CacheConfig{
			Driver: "memory",
			Prefix: "test_cache",
		},
		Log: log.LogConfig{
			Driver: "console",
			Config: make(map[string]any),
		},
		Queue: QueueConfig{
			Driver:  "memory",
			Encrypt: true,
		},
		Mail: mail.MailConfig{
			Driver: "log",
		},
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !queue.IsEncryptionEnabled() {
		t.Fatal("payload encryption must be enabled after boot with QUEUE_ENCRYPT")
	}

	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if queue.IsEncryptionEnabled() {
		t.Error("Shutdown left the queue payload encryptor installed")
	}
}

// TestNew_InitFailure_DoesNotShutDownUninitializedModules pins the
// register-failure unwind scope: modules whose Register completed are shut
// down; the module that failed its own Register and modules that never
// ran are not. Before the fix all three saw Shutdown.
func TestNew_InitFailure_DoesNotShutDownUninitializedModules(t *testing.T) {
	ok := &shutdownRecorder{}
	failsRegister := &shutdownRecorder{initErr: errors.New("register kaboom")}
	never := &shutdownRecorder{}

	_, err := NewTestApp(WithModules(ok, failsRegister, never))
	if err == nil {
		t.Fatal("expected register failure to propagate from New()")
	}
	if !errors.Is(err, failsRegister.initErr) {
		t.Fatalf("expected wrapped register error, got: %v", err)
	}

	if got := ok.shutdowns.Load(); got != 1 {
		t.Errorf("registered provider Shutdown called %d times, want 1", got)
	}
	if got := failsRegister.shutdowns.Load(); got != 0 {
		t.Errorf("failing provider Shutdown called %d times, want 0", got)
	}
	if got := never.shutdowns.Load(); got != 0 {
		t.Errorf("never-registered provider Shutdown called %d times, want 0", got)
	}
	if never.registered.Load() {
		t.Error("provider after the failing one should never have registered")
	}
}

// orderRecordingQueue is a contract.QueueDriver fake that records when its
// Shutdown runs relative to other recorded teardown steps.
type orderRecordingQueue struct {
	order *[]string
}

func (q *orderRecordingQueue) PushCtx(_ context.Context, _ contract.QueueJob, _ ...string) error {
	return nil
}

func (q *orderRecordingQueue) PushDelayedCtx(_ context.Context, _ contract.QueueJob, _ time.Duration, _ ...string) error {
	return nil
}

func (q *orderRecordingQueue) PopCtx(_ context.Context, _ string) (contract.QueueJob, error) {
	return nil, errors.New("empty")
}

func (q *orderRecordingQueue) Size(_ string) (int64, error) { return 0, nil }

func (q *orderRecordingQueue) Clear(_ string) error { return nil }

func (q *orderRecordingQueue) Failed(_ contract.QueueJob, _ error, _ string) error { return nil }

func (q *orderRecordingQueue) Shutdown(_ context.Context) error {
	*q.order = append(*q.order, "queue")
	return nil
}

// orderRecordingModule records its Shutdown into the shared order slice.
type orderRecordingModule struct {
	order *[]string
}

func (p *orderRecordingModule) Init(_ *app.Services) error  { return nil }
func (p *orderRecordingModule) Start(_ *app.Services) error { return nil }
func (p *orderRecordingModule) Shutdown(_ context.Context) error {
	*p.order = append(*p.order, "provider")
	return nil
}

// TestShutdown_ModulesBeforeQueue pins the Shutdown order change: module
// teardown runs before the queue driver closes, so modules can still flush
// work into the queue. Mirrors the New() failure path, where the module
// unwind closure is pushed last and runs first.
func TestShutdown_ModulesBeforeQueue(t *testing.T) {
	var order []string
	a, err := NewTestApp(WithModules(&orderRecordingModule{order: &order}))
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	// Swap the memory queue for the recording fake; close the original
	// ourselves since the app no longer references it.
	orig := a.Services.Queue
	t.Cleanup(func() {
		if orig != nil {
			_ = orig.Shutdown(context.Background())
		}
	})
	a.Services.Queue = &orderRecordingQueue{order: &order}

	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	want := []string{"provider", "queue"}
	if len(order) != len(want) {
		t.Fatalf("recorded teardown order %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("teardown order %v, want %v (provider must shut down before the queue closes)", order, want)
		}
	}
}
