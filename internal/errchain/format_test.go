package errchain_test

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	. "github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/hostile"
)

// fmt's own formatting of a nested hostile value crashes: the premise the
// contained forms exist for.
func TestFmt_NestedPanicEscapes(t *testing.T) {
	for _, v := range []any{hostile.PanicError{Nested: true}, hostile.PanicStringer{Nested: true}, hostile.PanicFormatter{Nested: true}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("fmt.Sprintf(%%v, %T) did not panic", v)
				}
			}()
			_ = fmt.Sprintf("%v", v)
		}()
	}
}

// Every contained form formats every hostile value without a panic, the
// hostile operand as Unreadable when the panic is nested, and keeps the
// rest of the message.
func TestFormat_HostileValues(t *testing.T) {
	for name, v := range hostile.Unformattables() {
		t.Run(name, func(t *testing.T) {
			nested := strings.HasSuffix(name, "/nested")
			check := func(form, got string) {
				t.Helper()
				if !strings.HasPrefix(got, "prefix 7: ") || !strings.HasSuffix(got, " suffix") {
					t.Errorf("%s lost the message around the operand: %q", form, got)
				}
				if nested && !strings.Contains(got, Unreadable) {
					t.Errorf("%s = %q, want the nested operand as Unreadable", form, got)
				}
			}
			check("Sprintf", Sprintf("prefix %d: %v suffix", 7, v))
			check("Errorf", Errorf("prefix %d: %v suffix", 7, v).Error())
			if got := Sprint(v); got != Unreadable {
				t.Errorf("Sprint = %q, want Unreadable", got)
			}
			if e, ok := v.(error); ok {
				err := Errorf("prefix %d: %w suffix", 7, e)
				check("Errorf %w", err.Error())
				if Unwrap(err) != e {
					t.Errorf("Errorf %%w unwraps to %T, want the operand itself", Unwrap(err))
				}
			}
		})
	}
}

// A benign call is fmt's own: same text, same wrapping.
func TestFormat_BenignMatchesFmt(t *testing.T) {
	type pair struct{ A, B int }
	args := []any{io.EOF, "s", 3, pair{1, 2}, []byte("b"), nil, fmt.Stringer(nil)}
	const format = "%v %q %d %+v %x %v %v"
	if got, want := Sprintf(format, args...), fmt.Sprintf(format, args...); got != want {
		t.Errorf("Sprintf = %q, want %q", got, want)
	}
	err := Errorf("read: %w", io.EOF)
	want := fmt.Errorf("read: %w", io.EOF)
	if err.Error() != want.Error() || errors.Unwrap(err) != io.EOF || fmt.Sprintf("%T", err) != fmt.Sprintf("%T", want) {
		t.Errorf("Errorf = %T %q, want fmt's %T %q", err, err, want, want)
	}
	if got := Sprint("plain"); got != "plain" {
		t.Errorf("Sprint(string) = %q", got)
	}
}

// Plain operands keep their verb in the second formatting; the hostile one
// stands in.
func TestFormat_StandInKeepsPlainOperands(t *testing.T) {
	got := Sprintf("%05d %x %q %v", 42, []byte{1, 2}, "q", hostile.PanicError{Nested: true})
	if want := `00042 0102 "q" ` + Unreadable; got != want {
		t.Errorf("Sprintf = %q, want %q", got, want)
	}
	// Two %w operands, one hostile: both stay wrapped.
	err := Errorf("%w and %w", io.EOF, hostile.PanicError{Nested: true})
	if !strings.HasPrefix(err.Error(), "EOF and ") {
		t.Errorf("Errorf = %q, lost the benign %%w operand", err)
	}
	multi, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Errorf with two %%w = %T, want Unwrap() []error", err)
	}
	if got := multi.Unwrap(); len(got) != 2 || got[0] != io.EOF || got[1] != (hostile.PanicError{Nested: true}) {
		t.Errorf("Unwrap() = %#v, want the two operands themselves", got)
	}
}

// The contained forms run concurrently on shared hostile values; run with
// -race -cpu 1,2.
func TestFormat_Concurrent(t *testing.T) {
	values := hostile.Unformattables()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				for _, v := range values {
					_ = Sprintf("%v", v)
					_ = Errorf("%v", v)
					_ = Sprint(v)
				}
			}
		})
	}
	wg.Wait()
}
