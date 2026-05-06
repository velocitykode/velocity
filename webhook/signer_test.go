package webhook

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSigner_Sign_Deterministic(t *testing.T) {
	t.Parallel()

	frozen := time.Unix(1714000000, 0)
	s := &Signer{
		Algorithm: HMACSHA256,
		Secret:    []byte("whsec_topsecret"),
		Now:       func() time.Time { return frozen },
	}
	payload := []byte(`{"event":"order.created","id":"o_1"}`)

	sig1, ts1, err := s.Sign(payload)
	if err != nil {
		t.Fatalf("Sign returned error: %v", err)
	}
	sig2, ts2, err := s.Sign(payload)
	if err != nil {
		t.Fatalf("Sign returned error: %v", err)
	}
	if sig1 != sig2 {
		t.Fatalf("expected deterministic signatures, got %q vs %q", sig1, sig2)
	}
	if ts1 != ts2 {
		t.Fatalf("expected deterministic timestamps, got %q vs %q", ts1, ts2)
	}
	if ts1 != "1714000000" {
		t.Fatalf("expected timestamp 1714000000, got %q", ts1)
	}
	if len(sig1) != 64 { // sha256 -> 32 bytes -> 64 hex chars
		t.Fatalf("expected 64-char hex signature, got %d", len(sig1))
	}
}

func TestSigner_Header_Format(t *testing.T) {
	t.Parallel()

	s := NewSigner([]byte("whsec_xyz"))
	header, err := s.Header([]byte("payload"))
	if err != nil {
		t.Fatalf("Header returned error: %v", err)
	}
	if !strings.HasPrefix(header, "t=") || !strings.Contains(header, ",v1=") {
		t.Fatalf("expected header to look like t=<n>,v1=<hex>, got %q", header)
	}

	v := NewVerifier([]byte("whsec_xyz"))
	if err := v.Verify([]byte("payload"), header); err != nil {
		t.Fatalf("round-trip Verify failed: %v", err)
	}
}

func TestSigner_Sign_RejectsMissingConfig(t *testing.T) {
	t.Parallel()

	if _, _, err := (&Signer{Secret: []byte("x")}).Sign([]byte("p")); !errors.Is(err, ErrNoAlgorithm) {
		t.Fatalf("missing Algorithm: want ErrNoAlgorithm, got %v", err)
	}
	if _, _, err := (&Signer{Algorithm: HMACSHA256}).Sign([]byte("p")); !errors.Is(err, ErrMissingSecret) {
		t.Fatalf("missing Secret: want ErrMissingSecret, got %v", err)
	}
	if _, err := (&Signer{Secret: []byte("x")}).Header([]byte("p")); !errors.Is(err, ErrNoAlgorithm) {
		t.Fatalf("Header with missing Algorithm: want ErrNoAlgorithm, got %v", err)
	}
}

// fakeAlgorithm returns a deterministic constant MAC regardless of input.
// It exercises the Algorithm interface seam.
type fakeAlgorithm struct {
	out  []byte
	name string
}

func (f *fakeAlgorithm) Name() string            { return f.name }
func (f *fakeAlgorithm) Sign(_, _ []byte) []byte { return append([]byte(nil), f.out...) }

func TestSigner_Algorithm_Pluggable(t *testing.T) {
	t.Parallel()

	alg := &fakeAlgorithm{out: []byte{0xde, 0xad, 0xbe, 0xef}, name: "fake"}
	frozen := time.Unix(42, 0)
	signer := &Signer{Algorithm: alg, Secret: []byte("k"), Now: func() time.Time { return frozen }}
	verifier := &Verifier{Algorithm: alg, Secret: []byte("k"), Tolerance: time.Hour, Now: func() time.Time { return frozen }}

	header, err := signer.Header([]byte("hello"))
	if err != nil {
		t.Fatalf("Header: %v", err)
	}
	if !strings.HasSuffix(header, ",v1=deadbeef") {
		t.Fatalf("expected v1=deadbeef suffix, got %q", header)
	}
	if err := verifier.Verify([]byte("hello"), header); err != nil {
		t.Fatalf("Verify with pluggable algorithm failed: %v", err)
	}
	if alg.Name() != "fake" {
		t.Fatalf("expected Algorithm name fake, got %q", alg.Name())
	}
}

func TestHMACSHA256_Name(t *testing.T) {
	t.Parallel()

	if HMACSHA256.Name() != "hmac-sha256" {
		t.Fatalf("unexpected name %q", HMACSHA256.Name())
	}
}
