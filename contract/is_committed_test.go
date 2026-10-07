package contract

import "testing"

type fixedReporter bool

func (f fixedReporter) Committed() bool { return bool(f) }

// A reporter's answer wins whenever there is one, a false answer over a
// true fallback included; the fallback answers only without a reporter.
func TestIsCommitted(t *testing.T) {
	tests := []struct {
		name     string
		reporter CommitReporter
		fallback bool
		want     bool
	}{
		{"no reporter, fallback false", nil, false, false},
		{"no reporter, fallback true", nil, true, true},
		{"reporter true over fallback false", fixedReporter(true), false, true},
		{"reporter false over fallback true", fixedReporter(false), true, false},
		{"reporter false, fallback false", fixedReporter(false), false, false},
		{"reporter true, fallback true", fixedReporter(true), true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCommitted(tt.reporter, tt.fallback); got != tt.want {
				t.Fatalf("IsCommitted = %v, want %v", got, tt.want)
			}
		})
	}
}
