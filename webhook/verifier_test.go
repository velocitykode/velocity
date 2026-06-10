package webhook

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestVerifier_Verify_RoundTrip(t *testing.T) {
	t.Parallel()

	frozen := time.Unix(1714000000, 0)
	signer := &Signer{Algorithm: HMACSHA256, Secret: []byte("k"), Now: func() time.Time { return frozen }}
	verifier := &Verifier{Algorithm: HMACSHA256, Secret: []byte("k"), Tolerance: time.Minute, Now: func() time.Time { return frozen }}

	payload := []byte(`{"hello":"world"}`)
	header, err := signer.Header(payload)
	if err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := verifier.Verify(payload, header); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifier_Verify_TamperedPayload_Rejected(t *testing.T) {
	t.Parallel()

	frozen := time.Unix(1714000000, 0)
	signer := &Signer{Algorithm: HMACSHA256, Secret: []byte("k"), Now: func() time.Time { return frozen }}
	verifier := &Verifier{Algorithm: HMACSHA256, Secret: []byte("k"), Tolerance: time.Minute, Now: func() time.Time { return frozen }}

	payload := []byte("aaaaaa")
	header, err := signer.Header(payload)
	if err != nil {
		t.Fatalf("Header: %v", err)
	}

	// Flip one byte.
	tampered := append([]byte(nil), payload...)
	tampered[0] = 'b'

	err = verifier.Verify(tampered, header)
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("expected ErrSignatureMismatch, got %v", err)
	}
}

func TestVerifier_Verify_TamperedSignature_Rejected(t *testing.T) {
	t.Parallel()

	frozen := time.Unix(1714000000, 0)
	signer := &Signer{Algorithm: HMACSHA256, Secret: []byte("k"), Now: func() time.Time { return frozen }}
	verifier := &Verifier{Algorithm: HMACSHA256, Secret: []byte("k"), Tolerance: time.Minute, Now: func() time.Time { return frozen }}

	header, err := signer.Header([]byte("payload"))
	if err != nil {
		t.Fatalf("Header: %v", err)
	}

	// Mutate one hex char of the signature without changing length.
	idx := len(header) - 1
	mutated := []byte(header)
	if mutated[idx] == '0' {
		mutated[idx] = '1'
	} else {
		mutated[idx] = '0'
	}

	err = verifier.Verify([]byte("payload"), string(mutated))
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("expected ErrSignatureMismatch, got %v", err)
	}
}

func TestVerifier_Verify_ExpiredTimestamp_Rejected(t *testing.T) {
	t.Parallel()

	signed := time.Unix(1714000000, 0)
	signer := &Signer{Algorithm: HMACSHA256, Secret: []byte("k"), Now: func() time.Time { return signed }}
	header, err := signer.Header([]byte("p"))
	if err != nil {
		t.Fatalf("Header: %v", err)
	}

	// Move the verifier clock 10 minutes forward; tolerance is 1 minute.
	verifier := &Verifier{
		Algorithm: HMACSHA256,
		Secret:    []byte("k"),
		Tolerance: time.Minute,
		Now:       func() time.Time { return signed.Add(10 * time.Minute) },
	}
	if err := verifier.Verify([]byte("p"), header); !errors.Is(err, ErrTimestampOutOfTolerance) {
		t.Fatalf("expected ErrTimestampOutOfTolerance, got %v", err)
	}

	// Symmetry: reject signatures from the future too.
	verifier.Now = func() time.Time { return signed.Add(-10 * time.Minute) }
	if err := verifier.Verify([]byte("p"), header); !errors.Is(err, ErrTimestampOutOfTolerance) {
		t.Fatalf("expected ErrTimestampOutOfTolerance for future ts, got %v", err)
	}
}

// fakeNonceStore is a minimal in-memory NonceStore for verifier tests. It
// is intentionally simple but performs CheckAndMark atomically under a
// single mutex, mirroring the contract real drivers must honour.
type fakeNonceStore struct {
	mu       sync.Mutex
	seen     map[string]struct{}
	checkErr error
}

func newFakeNonceStore() *fakeNonceStore {
	return &fakeNonceStore{seen: make(map[string]struct{})}
}

func (f *fakeNonceStore) CheckAndMark(_ context.Context, n string, _ time.Duration) (bool, error) {
	if f.checkErr != nil {
		return false, f.checkErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.seen[n]; ok {
		return true, nil
	}
	f.seen[n] = struct{}{}
	return false, nil
}

func TestVerifier_Verify_Replay_Rejected(t *testing.T) {
	t.Parallel()

	frozen := time.Unix(1714000000, 0)
	signer := &Signer{Algorithm: HMACSHA256, Secret: []byte("k"), Now: func() time.Time { return frozen }}
	store := newFakeNonceStore()
	verifier := &Verifier{
		Algorithm: HMACSHA256,
		Secret:    []byte("k"),
		Tolerance: time.Minute,
		Nonces:    store,
		Now:       func() time.Time { return frozen },
	}

	header, err := signer.Header([]byte("p"))
	if err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := verifier.Verify([]byte("p"), header); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if err := verifier.Verify([]byte("p"), header); !errors.Is(err, ErrReplay) {
		t.Fatalf("expected ErrReplay on second Verify, got %v", err)
	}
}

func TestVerifier_Verify_NonceStoreErrors_Surface(t *testing.T) {
	t.Parallel()

	frozen := time.Unix(1714000000, 0)
	signer := &Signer{Algorithm: HMACSHA256, Secret: []byte("k"), Now: func() time.Time { return frozen }}
	header, _ := signer.Header([]byte("p"))

	sentinel := errors.New("storage down")
	store := newFakeNonceStore()
	store.checkErr = sentinel
	v := &Verifier{Algorithm: HMACSHA256, Secret: []byte("k"), Tolerance: time.Minute, Nonces: store, Now: func() time.Time { return frozen }}
	if err := v.Verify([]byte("p"), header); !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel from CheckAndMark, got %v", err)
	}
}

func TestVerifier_Verify_RejectsMissingConfig(t *testing.T) {
	t.Parallel()

	if err := (&Verifier{Secret: []byte("x")}).Verify(nil, "t=1,v1=00"); !errors.Is(err, ErrNoAlgorithm) {
		t.Fatalf("missing Algorithm: want ErrNoAlgorithm, got %v", err)
	}
	if err := (&Verifier{Algorithm: HMACSHA256}).Verify(nil, "t=1,v1=00"); !errors.Is(err, ErrMissingSecret) {
		t.Fatalf("missing Secret: want ErrMissingSecret, got %v", err)
	}
}

func TestVerifier_Verify_MalformedHeader(t *testing.T) {
	t.Parallel()

	// DisableTimestampCheck so we exercise the parse + hex paths without
	// timestamp interference (Tolerance=0 now means the 5-minute default).
	v := &Verifier{Algorithm: HMACSHA256, Secret: []byte("k"), DisableTimestampCheck: true}
	cases := []string{
		"",
		"garbage",
		"t=,v1=00",
		"t=abc,v1=00",
		"=,v1=00",
		"t=1,v1=",
		"t=1",         // missing v1
		"v1=deadbeef", // missing t
		"t=1,v1=ZZZZ", // non-hex (decoder fails)
		"t=1,v1=abc",  // odd length
	}
	for _, c := range cases {
		err := v.Verify([]byte("p"), c)
		if !errors.Is(err, ErrMalformedHeader) {
			t.Errorf("case %q: expected ErrMalformedHeader, got %v", c, err)
		}
	}
}

// TestVerifier_Verify_ZeroToleranceDefaultsToFiveMinutes is the regression
// test for the fail-open bug where Tolerance=0 (e.g. a struct-literal
// Verifier that bypassed NewVerifier) disabled the freshness check entirely.
func TestVerifier_Verify_ZeroToleranceDefaultsToFiveMinutes(t *testing.T) {
	t.Parallel()

	signed := time.Unix(1714000000, 0)
	signer := &Signer{Algorithm: HMACSHA256, Secret: []byte("k"), Now: func() time.Time { return signed }}
	header, _ := signer.Header([]byte("p"))

	v := &Verifier{
		Algorithm: HMACSHA256,
		Secret:    []byte("k"),
		Tolerance: 0,
		Now:       func() time.Time { return signed.Add(6 * time.Minute) },
	}
	if err := v.Verify([]byte("p"), header); !errors.Is(err, ErrTimestampOutOfTolerance) {
		t.Fatalf("Tolerance=0 must apply the 5m default: want ErrTimestampOutOfTolerance, got %v", err)
	}

	// Inside the default window the same verifier accepts.
	v.Now = func() time.Time { return signed.Add(4 * time.Minute) }
	if err := v.Verify([]byte("p"), header); err != nil {
		t.Fatalf("expected acceptance within default tolerance, got %v", err)
	}
}

func TestVerifier_Verify_DisableTimestampCheck_AcceptsStale(t *testing.T) {
	t.Parallel()

	signed := time.Unix(1, 0) // ancient
	signer := &Signer{Algorithm: HMACSHA256, Secret: []byte("k"), Now: func() time.Time { return signed }}
	header, _ := signer.Header([]byte("p"))

	v := &Verifier{
		Algorithm:             HMACSHA256,
		Secret:                []byte("k"),
		DisableTimestampCheck: true,
		Now:                   func() time.Time { return signed.Add(100 * 365 * 24 * time.Hour) },
	}
	if err := v.Verify([]byte("p"), header); err != nil {
		t.Fatalf("expected no error with DisableTimestampCheck, got %v", err)
	}
}

func TestNewVerifier_DefaultsToleranceExplicitly(t *testing.T) {
	t.Parallel()

	v := NewVerifier([]byte("k"))
	if v.Tolerance != 5*time.Minute {
		t.Fatalf("NewVerifier Tolerance = %v, want 5m", v.Tolerance)
	}
	if v.DisableTimestampCheck {
		t.Fatal("NewVerifier must not disable the timestamp check")
	}
}

func TestVerifier_Verify_NonceTTLDefaultsWhenToleranceZero(t *testing.T) {
	t.Parallel()

	frozen := time.Unix(1714000000, 0)
	signer := &Signer{Algorithm: HMACSHA256, Secret: []byte("k"), Now: func() time.Time { return frozen }}
	header, _ := signer.Header([]byte("p"))

	store := newFakeNonceStore()
	v := &Verifier{Algorithm: HMACSHA256, Secret: []byte("k"), Tolerance: 0, Nonces: store, Now: func() time.Time { return frozen }}
	if err := v.Verify([]byte("p"), header); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if err := v.Verify([]byte("p"), header); !errors.Is(err, ErrReplay) {
		t.Fatalf("expected ErrReplay, got %v", err)
	}
}

func TestVerifier_Verify_ContextPlumbing(t *testing.T) {
	t.Parallel()

	frozen := time.Unix(1714000000, 0)
	signer := &Signer{Algorithm: HMACSHA256, Secret: []byte("k"), Now: func() time.Time { return frozen }}
	header, _ := signer.Header([]byte("p"))

	store := newFakeNonceStore()
	v := &Verifier{Algorithm: HMACSHA256, Secret: []byte("k"), Tolerance: time.Minute, Nonces: store, Now: func() time.Time { return frozen }}

	type ctxKey string
	ctx := context.WithValue(context.Background(), ctxKey("k"), "v")
	if err := v.VerifyContext(ctx, []byte("p"), header); err != nil {
		t.Fatalf("VerifyContext: %v", err)
	}
}

// TestVerifier_ReplayRace_Concurrent fires many parallel Verify calls of
// the same payload through the real in-memory NonceStore (which performs
// CheckAndMark atomically) and asserts exactly one verification observes
// success while every other observes ErrReplay. This is the regression
// test for the TOCTOU race between the previous Seen / Mark interface.
func TestVerifier_ReplayRace_Concurrent(t *testing.T) {
	t.Parallel()

	frozen := time.Unix(1714000000, 0)
	signer := &Signer{Algorithm: HMACSHA256, Secret: []byte("k"), Now: func() time.Time { return frozen }}
	header, err := signer.Header([]byte("p"))
	if err != nil {
		t.Fatalf("Header: %v", err)
	}

	store := newFakeNonceStore()
	v := &Verifier{
		Algorithm: HMACSHA256,
		Secret:    []byte("k"),
		Tolerance: time.Minute,
		Nonces:    store,
		Now:       func() time.Time { return frozen },
	}

	const goroutines = 256
	var (
		successes atomic.Int64
		replays   atomic.Int64
		other     atomic.Int64
		wg        sync.WaitGroup
		start     = make(chan struct{})
	)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			err := v.Verify([]byte("p"), header)
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, ErrReplay):
				replays.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Fatalf("expected exactly 1 success under concurrent replay, got %d (replays=%d other=%d)", got, replays.Load(), other.Load())
	}
	if got := replays.Load(); got != goroutines-1 {
		t.Fatalf("expected %d replays, got %d (successes=%d other=%d)", goroutines-1, got, successes.Load(), other.Load())
	}
	if got := other.Load(); got != 0 {
		t.Fatalf("expected 0 unexpected errors, got %d", got)
	}
}

// Sanity: the framing prefix of the timestamp in parseHeader matches a hand-
// rolled timestamp (smoke test for the strconv.FormatInt round-trip).
func TestParseHeader_TimestampRoundTrip(t *testing.T) {
	t.Parallel()

	for _, n := range []int64{0, 1, 1714000000, 9_999_999_999} {
		s := "t=" + strconv.FormatInt(n, 10) + ",v1=00"
		ts, _, err := parseHeader(s)
		if err != nil {
			t.Fatalf("parseHeader(%q): %v", s, err)
		}
		if ts != n {
			t.Fatalf("got %d, want %d", ts, n)
		}
	}
}
