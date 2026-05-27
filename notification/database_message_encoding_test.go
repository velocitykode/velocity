package notification

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// TestDatabaseMessage_Validate_EncodableSucceeds pins the happy path:
// JSON-clean values produce no error from Validate.
func TestDatabaseMessage_Validate_EncodableSucceeds(t *testing.T) {
	m := NewDatabaseMessage("test").
		Set("subject", "hello").
		Set("count", 7).
		Set("tags", []string{"a", "b"}).
		Set("meta", map[string]interface{}{"k": "v"})

	if err := m.Validate(); err != nil {
		t.Fatalf("Validate on clean data: %v", err)
	}
}

// TestDatabaseMessage_Validate_NilAndEmpty pins the guard branches:
// nil receiver and empty Data both produce nil so the channel can
// call Validate unconditionally without nil panics.
func TestDatabaseMessage_Validate_NilAndEmpty(t *testing.T) {
	var nilMsg *DatabaseMessage
	if err := nilMsg.Validate(); err != nil {
		t.Errorf("nil receiver Validate: %v", err)
	}
	empty := NewDatabaseMessage("test")
	if err := empty.Validate(); err != nil {
		t.Errorf("empty Data Validate: %v", err)
	}
}

// TestDatabaseMessage_Validate_RejectsNaN pins that NaN (the most
// common encoding footgun; surfaces from any arithmetic on missing
// values) is rejected up-front instead of inside the channel.
func TestDatabaseMessage_Validate_RejectsNaN(t *testing.T) {
	m := NewDatabaseMessage("test").Set("ratio", math.NaN())
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error from NaN value, got nil")
	}
	if !strings.Contains(err.Error(), "not json-encodable") {
		t.Errorf("error message %q does not flag json-encodable", err.Error())
	}
}

// TestDatabaseMessage_Validate_RejectsInf pins +Inf/-Inf rejection
// (same class as NaN; json.Marshal rejects all three).
func TestDatabaseMessage_Validate_RejectsInf(t *testing.T) {
	for _, v := range []float64{math.Inf(1), math.Inf(-1)} {
		m := NewDatabaseMessage("test").Set("value", v)
		if err := m.Validate(); err == nil {
			t.Errorf("expected error for %v, got nil", v)
		}
	}
}

// TestDatabaseMessage_Validate_RejectsChan pins channel-value rejection.
// A channel cannot be JSON-encoded; this is the canonical "developer
// shoved a runtime handle into Data by mistake" case.
func TestDatabaseMessage_Validate_RejectsChan(t *testing.T) {
	ch := make(chan int)
	m := NewDatabaseMessage("test").Set("ch", ch)
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for chan value, got nil")
	}
}

// TestDatabaseMessage_Validate_RejectsFunc pins func-value rejection.
func TestDatabaseMessage_Validate_RejectsFunc(t *testing.T) {
	m := NewDatabaseMessage("test").Set("fn", func() {})
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for func value, got nil")
	}
}

// TestEncodeDatabaseData_HTMLNotEscaped pins the SetEscapeHTML(false)
// invariant: '<', '>', '&' survive into the encoded bytes as their
// raw UTF-8 form (0x3C, 0x3E, 0x26) rather than json.Marshal's
// default < / > / & unicode escapes. The default
// escaping is correct for inline browser contexts but corrupts the
// stored column for every downstream consumer (UI templates, log
// shippers, mobile clients) that does not re-decode the JSON before
// rendering it.
func TestEncodeDatabaseData_HTMLNotEscaped(t *testing.T) {
	data := map[string]interface{}{
		"html": "<b>hi</b> & welcome",
	}
	out, err := EncodeDatabaseData(data)
	if err != nil {
		t.Fatalf("EncodeDatabaseData: %v", err)
	}
	want := `"<b>hi</b> & welcome"`
	if !bytes.Contains(out, []byte(want)) {
		t.Errorf("EncodeDatabaseData() = %s, want substring %q", out, want)
	}
	// Negative: the default json.Marshal escape sequences (<,
	// >, &) must NOT appear.
	defaultEscapes := []string{"\\u003c", "\\u003e", "\\u0026"}
	for _, esc := range defaultEscapes {
		if bytes.Contains(out, []byte(esc)) {
			t.Errorf("EncodeDatabaseData() output contains default JSON escape %q: %s", esc, out)
		}
	}
}

// TestEncodeDatabaseData_NoTrailingNewline pins that the encoder
// strips the trailing newline json.Encoder appends, so output matches
// json.Marshal's byte-exact shape and consumers comparing on raw
// bytes do not see implementation detail.
func TestEncodeDatabaseData_NoTrailingNewline(t *testing.T) {
	data := map[string]interface{}{"k": "v"}
	out, err := EncodeDatabaseData(data)
	if err != nil {
		t.Fatalf("EncodeDatabaseData: %v", err)
	}
	if n := len(out); n == 0 || out[n-1] == '\n' {
		t.Errorf("EncodeDatabaseData() = %q, expected no trailing newline", out)
	}
}

// TestEncodeDatabaseData_PropagatesEncodeError pins that an unsupported
// value (NaN here) returns an error wrapping the json package error
// so callers can branch on encoding failures specifically.
func TestEncodeDatabaseData_PropagatesEncodeError(t *testing.T) {
	data := map[string]interface{}{"bad": math.NaN()}
	_, err := EncodeDatabaseData(data)
	if err == nil {
		t.Fatal("expected error for NaN value, got nil")
	}
	if !strings.Contains(err.Error(), "not json-encodable") {
		t.Errorf("error message %q does not flag json-encodable", err.Error())
	}
}
