package latency

import (
	"testing"
	"time"
)

func TestSlow(t *testing.T) {
	tests := []struct {
		name      string
		d         time.Duration
		threshold time.Duration
		want      bool
	}{
		{name: "above", d: 150 * time.Millisecond, threshold: 100 * time.Millisecond, want: true},
		{name: "below", d: 50 * time.Millisecond, threshold: 100 * time.Millisecond},
		{name: "equal is not slow", d: 100 * time.Millisecond, threshold: 100 * time.Millisecond},
		{name: "zero threshold disables", d: time.Hour},
		{name: "negative threshold disables", d: time.Hour, threshold: -time.Second},
		{name: "zero duration", threshold: time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Slow(tt.d, tt.threshold); got != tt.want {
				t.Errorf("Slow(%v, %v) = %v, want %v", tt.d, tt.threshold, got, tt.want)
			}
		})
	}
}

func TestMillis(t *testing.T) {
	for d, want := range map[time.Duration]int64{
		0:                        0,
		999 * time.Microsecond:   0,
		150 * time.Millisecond:   150,
		1500 * time.Microsecond:  1,
		2 * time.Second:          2000,
		-150 * time.Millisecond:  -150,
		5*time.Second + 1:        5000,
		150*time.Millisecond + 1: 150,
	} {
		if got := Millis(d); got != want {
			t.Errorf("Millis(%v) = %d, want %d", d, got, want)
		}
	}
	if Key != "duration_ms" {
		t.Errorf("Key = %q, want duration_ms", Key)
	}
}
