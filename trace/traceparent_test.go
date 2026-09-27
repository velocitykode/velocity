package trace

import (
	"context"
	"strings"
	"testing"
)

const (
	w3cTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	w3cSpan  = "00f067aa0ba902b7"
)

func TestParseTraceparent_Valid(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  Parent
	}{
		{"sampled", "00-" + w3cTrace + "-" + w3cSpan + "-01", Parent{TraceID: w3cTrace, SpanID: w3cSpan, Sampled: true}},
		{"not sampled", "00-" + w3cTrace + "-" + w3cSpan + "-00", Parent{TraceID: w3cTrace, SpanID: w3cSpan}},
		{"unknown flag bits dropped", "00-" + w3cTrace + "-" + w3cSpan + "-fe", Parent{TraceID: w3cTrace, SpanID: w3cSpan}},
		{"unknown flag bits with sampled", "00-" + w3cTrace + "-" + w3cSpan + "-ff", Parent{TraceID: w3cTrace, SpanID: w3cSpan, Sampled: true}},
		{"later version, same length", "01-" + w3cTrace + "-" + w3cSpan + "-01", Parent{TraceID: w3cTrace, SpanID: w3cSpan, Sampled: true}},
		{"later version, appended field", "cc-" + w3cTrace + "-" + w3cSpan + "-01-what-the-future-holds", Parent{TraceID: w3cTrace, SpanID: w3cSpan, Sampled: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseTraceparent(tt.value)
			if !ok {
				t.Fatalf("ParseTraceparent(%q) rejected a valid value", tt.value)
			}
			if got != tt.want {
				t.Errorf("ParseTraceparent(%q) = %+v, want %+v", tt.value, got, tt.want)
			}
		})
	}
}

func TestParseTraceparent_Invalid(t *testing.T) {
	valid := "00-" + w3cTrace + "-" + w3cSpan + "-01"
	tests := map[string]string{
		"empty":                   "",
		"garbage":                 "garbage",
		"short":                   valid[:54],
		"version 00 too long":     valid + "-00",
		"version ff":              "ff" + valid[2:],
		"version not hex":         "0x" + valid[2:],
		"version uppercase":       "0A" + valid[2:],
		"trace id uppercase":      "00-" + strings.ToUpper(w3cTrace) + "-" + w3cSpan + "-01",
		"trace id all zeros":      "00-" + strings.Repeat("0", 32) + "-" + w3cSpan + "-01",
		"trace id not hex":        "00-" + strings.Repeat("g", 32) + "-" + w3cSpan + "-01",
		"parent id all zeros":     "00-" + w3cTrace + "-" + strings.Repeat("0", 16) + "-01",
		"parent id not hex":       "00-" + w3cTrace + "-" + strings.Repeat("z", 16) + "-01",
		"flags not hex":           "00-" + w3cTrace + "-" + w3cSpan + "-0g",
		"flags uppercase":         "00-" + w3cTrace + "-" + w3cSpan + "-0A",
		"wrong separator":         "00_" + w3cTrace + "-" + w3cSpan + "-01",
		"missing separator":       "00-" + w3cTrace + w3cSpan + "-01xx",
		"later version no dash":   "01-" + w3cTrace + "-" + w3cSpan + "-01x",
		"later version oversized": "01-" + w3cTrace + "-" + w3cSpan + "-01-" + strings.Repeat("a", 512),
		"leading space":           " " + valid[:54],
		"crlf injection":          valid[:53] + "\r\n",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if p, ok := ParseTraceparent(value); ok {
				t.Errorf("ParseTraceparent(%q) = %+v, want rejection", value, p)
			}
		})
	}
}

func TestFormatTraceparent(t *testing.T) {
	got, ok := FormatTraceparent(Parent{TraceID: w3cTrace, SpanID: w3cSpan, Sampled: true})
	if !ok || got != "00-"+w3cTrace+"-"+w3cSpan+"-01" {
		t.Errorf("FormatTraceparent(sampled) = %q, %v", got, ok)
	}
	got, ok = FormatTraceparent(Parent{TraceID: w3cTrace, SpanID: w3cSpan})
	if !ok || got != "00-"+w3cTrace+"-"+w3cSpan+"-00" {
		t.Errorf("FormatTraceparent(not sampled) = %q, %v", got, ok)
	}

	for name, p := range map[string]Parent{
		"zero":             {},
		"no span":          {TraceID: w3cTrace},
		"short trace":      {TraceID: w3cTrace[:31], SpanID: w3cSpan},
		"uppercase":        {TraceID: strings.ToUpper(w3cTrace), SpanID: w3cSpan},
		"zero trace":       {TraceID: strings.Repeat("0", 32), SpanID: w3cSpan},
		"zero span":        {TraceID: w3cTrace, SpanID: strings.Repeat("0", 16)},
		"fallback markers": {TraceID: fallbackTraceID(), SpanID: fallbackSpanID()},
	} {
		if got, ok := FormatTraceparent(p); ok {
			t.Errorf("%s: FormatTraceparent(%+v) = %q, want rejection", name, p, got)
		}
	}
}

// TestTraceparent_RoundTrip pins that every id pair the generators produce
// survives Format then Parse unchanged.
func TestTraceparent_RoundTrip(t *testing.T) {
	for i := 0; i < 100; i++ {
		p := Parent{TraceID: MustGenerateTraceID(), SpanID: MustGenerateSpanID(), Sampled: i%2 == 0}
		value, ok := FormatTraceparent(p)
		if !ok {
			t.Fatalf("FormatTraceparent(%+v) rejected generated ids", p)
		}
		got, ok := ParseTraceparent(value)
		if !ok || got != p {
			t.Fatalf("round trip %+v -> %q -> %+v (%v)", p, value, got, ok)
		}
	}
}

func TestPropagate(t *testing.T) {
	collect := func(ctx context.Context) map[string]string {
		out := map[string]string{}
		Propagate(ctx, func(name, value string) { out[name] = value })
		return out
	}

	ctx := WithRequestID(WithTrace(context.Background(), w3cTrace, w3cSpan), "req-42")
	got := collect(ctx)
	if got[TraceparentHeader] != "00-"+w3cTrace+"-"+w3cSpan+"-01" {
		t.Errorf("traceparent = %q", got[TraceparentHeader])
	}
	if got[RequestIDHeader] != "req-42" {
		t.Errorf("request id = %q", got[RequestIDHeader])
	}

	if got := collect(context.Background()); len(got) != 0 {
		t.Errorf("empty ctx wrote %v, want nothing", got)
	}

	// Ids that cannot travel are skipped rather than failing the call.
	bad := WithRequestID(WithTrace(context.Background(), "not-a-w3c-trace", "span"), "line\nbreak")
	if got := collect(bad); len(got) != 0 {
		t.Errorf("unsendable ids wrote %v, want nothing", got)
	}

	//lint:ignore SA1012 exercising the nil-context code path is the point of this test
	Propagate(nil, func(string, string) { t.Error("nil ctx wrote a header") })
	Propagate(ctx, nil)
}

// FuzzParseTraceparent pins that no input panics and that every accepted
// value names W3C-shaped ids that format back to a valid header.
func FuzzParseTraceparent(f *testing.F) {
	f.Add("00-" + w3cTrace + "-" + w3cSpan + "-01")
	f.Add("cc-" + w3cTrace + "-" + w3cSpan + "-01-future")
	f.Add("")
	f.Add("ff-" + w3cTrace + "-" + w3cSpan + "-01")
	f.Fuzz(func(t *testing.T, value string) {
		p, ok := ParseTraceparent(value)
		if !ok {
			return
		}
		if _, ok := FormatTraceparent(p); !ok {
			t.Fatalf("ParseTraceparent(%q) accepted ids FormatTraceparent rejects: %+v", value, p)
		}
	})
}
