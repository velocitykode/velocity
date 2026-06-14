package drivers

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// BenchmarkSerializePayload measures the pooled hand-built fast path against
// the reference json.Marshal route. Report allocs/op to confirm the result
// allocation is removed on the fast path.
func BenchmarkSerializePayload(b *testing.B) {
	p := &Payload{
		IV:    base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")),
		Value: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("ciphertext", 8))),
		MAC:   base64.StdEncoding.EncodeToString([]byte(strings.Repeat("m", 32))),
	}

	b.Run("pooled", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := SerializePayload(p); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("json.Marshal", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			data, err := json.Marshal(p)
			if err != nil {
				b.Fatal(err)
			}
			_ = base64.URLEncoding.EncodeToString(data)
		}
	})
}

// TestSerializePayload_ByteIdentical asserts the pooled fast path emits the
// exact same bytes as the reference json.Marshal route across field
// combinations, and that the result round-trips through DeserializePayload.
func TestSerializePayload_ByteIdentical(t *testing.T) {
	cases := []*Payload{
		{IV: "aXY", Value: "dmFs", MAC: "bWFj", Tag: "dGFn"},
		{IV: "aXY", Value: "dmFs", MAC: "bWFj"},
		{IV: "aXY", Value: "dmFs", Tag: "dGFn"},
		{IV: "aXY", Value: "dmFs"},
		{IV: "", Value: ""},
		{IV: "iv+with/special==", Value: "value-with_url64=="},
	}
	for _, p := range cases {
		got, err := SerializePayload(p)
		if err != nil {
			t.Fatalf("SerializePayload(%+v): %v", p, err)
		}
		ref, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("json.Marshal(%+v): %v", p, err)
		}
		want := base64.URLEncoding.EncodeToString(ref)
		if got != want {
			t.Errorf("byte mismatch for %+v:\n got %q\nwant %q", p, got, want)
		}
		rt, err := DeserializePayload(got)
		if err != nil {
			t.Fatalf("DeserializePayload(%q): %v", got, err)
		}
		if *rt != *p {
			t.Errorf("round-trip mismatch: got %+v want %+v", *rt, *p)
		}
	}

	// A nil *Payload must not panic and must stay byte-identical to the
	// reference json.Marshal path (which emits "null").
	got, err := SerializePayload(nil)
	if err != nil {
		t.Fatalf("SerializePayload(nil): %v", err)
	}
	ref, err := json.Marshal((*Payload)(nil))
	if err != nil {
		t.Fatalf("json.Marshal((*Payload)(nil)): %v", err)
	}
	if want := base64.URLEncoding.EncodeToString(ref); got != want {
		t.Errorf("nil byte mismatch:\n got %q\nwant %q", got, want)
	}
}

// BenchmarkSecureCompare measures the cost of the constant-time string
// comparison used for HMAC verification. Skipped under -short.
func BenchmarkSecureCompare(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping in -short")
	}
	a := strings.Repeat("a", 64)
	bb := strings.Repeat("a", 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = secureCompare(a, bb)
	}
}

// TestSecureCompare_RejectsDifferentLengths guards the constant-time path
// against a panic on mismatched lengths.
func TestSecureCompare_RejectsDifferentLengths(t *testing.T) {
	if secureCompare("short", strings.Repeat("a", 32)) {
		t.Error("different-length inputs must not compare equal")
	}
}
