package queue

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
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

	wrapper, err := CreateJobWrapper(&registryRoundTripJob{N: 7}, "default")
	if err != nil {
		t.Fatalf("CreateJobWrapper: %v", err)
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
