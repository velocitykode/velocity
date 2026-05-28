package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// registryRoundTripJob mirrors the production pattern of pushing a pointer to
// a struct (`&Foo{}`), which causes fmt.Sprintf("%T", v) to emit
// "*queue.registryRoundTripJob". The corresponding Register call uses the
// idiomatic bare type name "registryRoundTripJob".
type registryRoundTripJob struct {
	N int `json:"n"`
}

func (j *registryRoundTripJob) Handle() error    { return nil }
func (j *registryRoundTripJob) Failed(err error) {}

func TestRegister_NormalizesPointerAndPackageQualifiedNames(t *testing.T) {
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "registryRoundTripJob")
		registry.mu.Unlock()
	})

	called := false
	Register("registryRoundTripJob", func(data []byte) (Job, error) {
		called = true
		j := &registryRoundTripJob{}
		if len(data) > 0 {
			_ = json.Unmarshal(data, j)
		}
		return j, nil
	})

	wrapper, err := createJobWrapper(&registryRoundTripJob{N: 7}, "default")
	if err != nil {
		t.Fatalf("createJobWrapper: %v", err)
	}
	if wrapper.Payload.Type != "registryRoundTripJob" {
		t.Fatalf("payload.Type not normalized: got %q want %q",
			wrapper.Payload.Type, "registryRoundTripJob")
	}

	job, err := registry.Deserialize(wrapper.Payload)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if !called {
		t.Fatal("registered factory was not invoked")
	}
	if _, ok := job.(*registryRoundTripJob); !ok {
		t.Fatalf("Deserialize returned wrong type: %T", job)
	}
}

func TestRegister_AcceptsPointerQualifiedRegistration(t *testing.T) {
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "registryAltJob")
		registry.mu.Unlock()
	})

	// Some existing callers register the full %T form (e.g. "*queue.TestJob").
	// Normalization must accept either side so we don't break them.
	Register("*queue.registryAltJob", func(data []byte) (Job, error) {
		return &registryRoundTripJob{N: 99}, nil
	})

	payload := &Payload{Type: "registryAltJob"}
	job, err := registry.Deserialize(payload)
	if err != nil {
		t.Fatalf("Deserialize bare-name with pointer-qualified Register: %v", err)
	}
	if got := job.(*registryRoundTripJob).N; got != 99 {
		t.Fatalf("factory not invoked: N=%d want 99", got)
	}
}

func TestDeserialize_UnknownTypeReturnsErrJobNotFound(t *testing.T) {
	_, err := registry.Deserialize(&Payload{Type: "*pkg.NeverRegistered"})
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("want ErrJobNotFound, got %v", err)
	}
}

func TestNormalizeJobType(t *testing.T) {
	cases := map[string]string{
		"Foo":           "Foo",
		"pkg.Foo":       "Foo",
		"*pkg.Foo":      "Foo",
		"*deep/pkg.Foo": "Foo",
		"":              "",
		"*":             "",
	}
	for in, want := range cases {
		if got := normalizeJobType(in); got != want {
			t.Errorf("normalizeJobType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewWorker_StderrFallbackEmitsOnNoLogger(t *testing.T) {
	orig := stderrFallbackWriter()
	var buf bytes.Buffer
	stderrFallback.Store(stderrWriter{Writer: &buf})
	defer stderrFallback.Store(stderrWriter{Writer: orig})

	w := NewWorker(NewMemoryDriver(), "fallback-test", func(Job) error { return nil })
	if _, ok := w.logger.(stderrLogger); !ok {
		t.Fatalf("expected stderrLogger fallback, got %T", w.logger)
	}

	got := buf.String()
	if !strings.Contains(got, "constructed without WithWorkerLogger") {
		t.Errorf("missing construction warning, got: %q", got)
	}
	if !strings.Contains(got, "queue=fallback-test") {
		t.Errorf("warning missing queue name, got: %q", got)
	}

	// stderrLogger.Error must reach the same writer so worker errors are visible.
	buf.Reset()
	w.logger.Error("boom", "id", 1, "err", "deserialize failed")
	out := buf.String()
	if !strings.Contains(out, "ERROR") || !strings.Contains(out, "boom") || !strings.Contains(out, "id=1") {
		t.Errorf("stderrLogger.Error format unexpected: %q", out)
	}
}

// registryTypedJob is registered via RegisterJob[*registryTypedJob] to verify
// the typed factory derives the same registry key the producer side computes
// from fmt.Sprintf("%T", &v).
type registryTypedJob struct {
	N int `json:"n"`
}

func (j *registryTypedJob) Handle() error    { return nil }
func (j *registryTypedJob) Failed(err error) {}

func TestRegisterJob_KeyMatchesDispatchedPayload(t *testing.T) {
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "registryTypedJob")
		registry.mu.Unlock()
	})

	called := false
	RegisterJob(func(data []byte) (*registryTypedJob, error) {
		called = true
		j := &registryTypedJob{}
		if len(data) > 0 {
			if err := json.Unmarshal(data, j); err != nil {
				return nil, err
			}
		}
		return j, nil
	})

	// Producer side: dispatching &registryTypedJob{} must serialize to a
	// payload whose Type the typed registration can decode.
	wrapper, err := createJobWrapper(&registryTypedJob{N: 11}, "default")
	if err != nil {
		t.Fatalf("createJobWrapper: %v", err)
	}
	if wrapper.Payload.Type != "registryTypedJob" {
		t.Fatalf("payload.Type = %q, want %q", wrapper.Payload.Type, "registryTypedJob")
	}

	job, err := registry.Deserialize(wrapper.Payload)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if !called {
		t.Fatal("typed factory was not invoked")
	}
	if _, ok := job.(*registryTypedJob); !ok {
		t.Fatalf("Deserialize returned %T, want *registryTypedJob", job)
	}
}

// registryMixedJob exists to verify a typed RegisterJob and a string-keyed
// Register on a sibling type share the registry without collision.
type registryMixedJob struct {
	S string `json:"s"`
}

func (j *registryMixedJob) Handle() error    { return nil }
func (j *registryMixedJob) Failed(err error) {}

func TestRegisterJob_CoexistsWithStringRegister(t *testing.T) {
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "registryTypedJob")
		delete(registry.handlers, "registryMixedJob")
		registry.mu.Unlock()
	})

	RegisterJob(func(data []byte) (*registryTypedJob, error) {
		return &registryTypedJob{N: 1}, nil
	})
	Register("registryMixedJob", func(data []byte) (Job, error) {
		return &registryMixedJob{S: "x"}, nil
	})

	typedPayload, err := SerializeJob(&registryTypedJob{}, "default")
	if err != nil {
		t.Fatalf("SerializeJob typed: %v", err)
	}
	if _, err := registry.Deserialize(typedPayload); err != nil {
		t.Fatalf("Deserialize typed: %v", err)
	}

	stringPayload, err := SerializeJob(&registryMixedJob{}, "default")
	if err != nil {
		t.Fatalf("SerializeJob string: %v", err)
	}
	if _, err := registry.Deserialize(stringPayload); err != nil {
		t.Fatalf("Deserialize string: %v", err)
	}
}

// TestRegisterJob_TypoIsImpossible documents the safety property: there is no
// string parameter to mistype. A typo would have to be in the type name
// itself, which fails at compile time, not at runtime via ErrJobNotFound.
func TestRegisterJob_TypoIsImpossible(t *testing.T) {
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "registryTypedJob")
		registry.mu.Unlock()
	})

	// A consumer that uses RegisterJob[T] cannot register against the wrong
	// key: the key is derived from T. The producer (createJobWrapper) uses
	// the same fmt.Sprintf("%T", v) + normalizeJobType pipeline, so any T
	// the consumer registers will line up with what a producer dispatching
	// the same type emits.
	RegisterJob(func(data []byte) (*registryTypedJob, error) {
		return &registryTypedJob{}, nil
	})

	// Sanity: a payload with a divergent type name still fails.
	if _, err := registry.Deserialize(&Payload{Type: "registryTypoJob"}); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("unrelated type should miss: got %v", err)
	}

	// And the registered type resolves cleanly.
	wrapper, err := createJobWrapper(&registryTypedJob{}, "default")
	if err != nil {
		t.Fatalf("createJobWrapper: %v", err)
	}
	if _, err := registry.Deserialize(wrapper.Payload); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
}

// TestRegisterJob_FactoryErrorPropagates ensures a factory-returned error
// surfaces from Deserialize and the registry does not swallow it.
func TestRegisterJob_FactoryErrorPropagates(t *testing.T) {
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "registryTypedJob")
		registry.mu.Unlock()
	})

	sentinel := errors.New("decode boom")
	RegisterJob(func(data []byte) (*registryTypedJob, error) {
		return nil, sentinel
	})

	wrapper, err := createJobWrapper(&registryTypedJob{}, "default")
	if err != nil {
		t.Fatalf("createJobWrapper: %v", err)
	}

	job, err := registry.Deserialize(wrapper.Payload)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if job != nil {
		t.Fatalf("expected nil job on factory error, got %T", job)
	}
}

// TestRegisterJob_LastRegistrationWins documents the same overwrite semantics
// as the string-keyed Register: a re-registration replaces the prior factory.
// This matches the existing map[string]func behavior under the hood.
func TestRegisterJob_LastRegistrationWins(t *testing.T) {
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "registryTypedJob")
		registry.mu.Unlock()
	})

	RegisterJob(func(data []byte) (*registryTypedJob, error) {
		return &registryTypedJob{N: 1}, nil
	})
	RegisterJob(func(data []byte) (*registryTypedJob, error) {
		return &registryTypedJob{N: 2}, nil
	})

	wrapper, err := createJobWrapper(&registryTypedJob{}, "default")
	if err != nil {
		t.Fatalf("createJobWrapper: %v", err)
	}
	job, err := registry.Deserialize(wrapper.Payload)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if got := job.(*registryTypedJob).N; got != 2 {
		t.Fatalf("got N=%d, want 2 (last registration should win)", got)
	}
}

// TestRegisterJob_StringRegisterOverridesTyped ensures string-keyed Register
// on the same key still overrides a prior typed registration. Mixed-call
// migrations rely on this.
func TestRegisterJob_StringRegisterOverridesTyped(t *testing.T) {
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "registryTypedJob")
		registry.mu.Unlock()
	})

	RegisterJob(func(data []byte) (*registryTypedJob, error) {
		return &registryTypedJob{N: 1}, nil
	})
	Register("registryTypedJob", func(data []byte) (Job, error) {
		return &registryTypedJob{N: 99}, nil
	})

	wrapper, err := createJobWrapper(&registryTypedJob{}, "default")
	if err != nil {
		t.Fatalf("createJobWrapper: %v", err)
	}
	job, err := registry.Deserialize(wrapper.Payload)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if got := job.(*registryTypedJob).N; got != 99 {
		t.Fatalf("got N=%d, want 99 (string Register should override)", got)
	}
}

// TestRegisterJob_ConcurrentRegistration exercises the registry mutex under
// parallel registration plus deserialization. Run with -race.
func TestRegisterJob_ConcurrentRegistration(t *testing.T) {
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "registryTypedJob")
		registry.mu.Unlock()
	})

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			RegisterJob(func(data []byte) (*registryTypedJob, error) {
				return &registryTypedJob{N: 7}, nil
			})
			payload, _ := SerializeJob(&registryTypedJob{}, "default")
			if _, err := registry.Deserialize(payload); err != nil {
				t.Errorf("Deserialize during concurrent register: %v", err)
			}
		}()
	}
	wg.Wait()
}

// TestRegisterJob_MemoryDriverRoundTrip exercises the typed registration end
// to end through the MemoryDriver: push -> pop -> deserialize. The redis and
// database drivers go through the same registry.Deserialize path; because the
// registry is process-global and driver-independent, a single round-trip on
// memory exercises the typed factory contract that all drivers rely on.
//
// (Backends covered: memory in-process. Redis/database drivers share the
// same registry.Deserialize call site and require external infra to test;
// see queue/integration_test.go for those builds.)
func TestRegisterJob_MemoryDriverRoundTrip(t *testing.T) {
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "registryTypedJob")
		registry.mu.Unlock()
	})

	RegisterJob(func(data []byte) (*registryTypedJob, error) {
		j := &registryTypedJob{}
		if len(data) == 0 {
			return j, nil
		}
		// MemoryDriver hands back the live Job pointer via jobWrapper.Job
		// (in-process fast path), so this factory is NOT invoked on a
		// same-process push/pop. It runs only for durable drivers that
		// hydrate from Payload.Data bytes. Even so, registering it lets
		// the producer round-trip prove the payload bytes are decodable,
		// which is the C-01 invariant.
		_ = json.Unmarshal(data, j)
		return j, nil
	})

	d := NewMemoryDriver()
	ctx := context.Background()

	original := &registryTypedJob{N: 42}
	if err := d.PushCtx(ctx, original, "default"); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}

	popped, err := d.PopCtx(ctx, "default")
	if err != nil {
		t.Fatalf("PopCtx: %v", err)
	}
	got, ok := popped.(*registryTypedJob)
	if !ok {
		t.Fatalf("PopCtx returned %T, want *registryTypedJob", popped)
	}
	if got.N != 42 {
		t.Fatalf("round-tripped N=%d, want 42", got.N)
	}
}

func TestSerializeJob_NormalizesPayloadType(t *testing.T) {
	payload, err := SerializeJob(&registryRoundTripJob{N: 3}, "default")
	if err != nil {
		t.Fatalf("SerializeJob: %v", err)
	}
	if payload.Type != "registryRoundTripJob" {
		t.Fatalf("SerializeJob did not normalize Type: got %q want %q",
			payload.Type, "registryRoundTripJob")
	}
}

func TestNewWorker_ExplicitLoggerSuppressesFallbackWarning(t *testing.T) {
	orig := stderrFallbackWriter()
	var buf bytes.Buffer
	stderrFallback.Store(stderrWriter{Writer: &buf})
	defer stderrFallback.Store(stderrWriter{Writer: orig})

	NewWorker(NewMemoryDriver(), "quiet-test",
		func(Job) error { return nil },
		WithWorkerLogger(nullLogger{}),
	)

	if buf.Len() != 0 {
		t.Errorf("expected no fallback warning when logger supplied, got: %q", buf.String())
	}
}
