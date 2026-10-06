package fallbacklog

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// nilLogger and nilAware are pointer types whose nil value, held in an
// interface, is a typed nil: a method call on it dereferences nil.
type nilLogger struct{ contract.Logger }

type nilAware struct{ contract.LoggerAware }

// A typed-nil logger is no logger: Resolve answers with the fallback, and
// a Forwarder set to one reports none installed and writes to the fallback.
func TestTypedNilLoggerIsNoLogger(t *testing.T) {
	var typed *nilLogger

	if _, ok := Resolve(typed).(Logger); !ok {
		t.Errorf("Resolve(typed nil) = %T, want the fallback Logger", Resolve(typed))
	}

	var f Forwarder
	f.Set(typed)
	if got := f.Installed(); got != nil {
		t.Errorf("Installed after Set(typed nil) = %T, want nil", got)
	}
	out := capture(t)
	f.Warn("typed nil target")
	if got := out.String(); !strings.Contains(got, "typed nil target") {
		t.Errorf("fallback output = %q, want the line", got)
	}

	wrote := 0
	Write(typed, func(l contract.Logger) {
		wrote++
		if _, ok := l.(Logger); !ok {
			t.Errorf("Write handed %T, want the fallback Logger", l)
		}
	})
	if wrote != 1 {
		t.Errorf("write ran %d times, want 1", wrote)
	}
}

// Hand ignores a typed-nil receiver: nothing is called and no warning is
// written.
func TestForwarderHand_TypedNilIsIgnored(t *testing.T) {
	var typed *nilAware
	out := capture(t)
	var f Forwarder
	f.Hand(typed, "handing failed")
	if got := out.String(); got != "" {
		t.Errorf("Hand(typed nil) wrote %q, want nothing", got)
	}
}
