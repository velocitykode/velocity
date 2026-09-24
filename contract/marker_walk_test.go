package contract

import (
	"errors"
	"fmt"
	"testing"
)

// testPanic stands for a recovered panic: it implements RecoveredPanic and
// unwraps to its value when that value is an error, as the framework's
// recovered-panic error does.
type testPanic struct {
	value any
}

func (p *testPanic) Error() string  { return fmt.Sprintf("panic: %v", p.value) }
func (p *testPanic) Recovered() any { return p.value }
func (p *testPanic) Unwrap() error {
	if err, ok := p.value.(error); ok {
		return err
	}
	return nil
}

// asMarkedError answers a marker target through its As method by
// delegating to the errors package on a hidden error.
type asMarkedError struct {
	hidden error
}

func (e *asMarkedError) Error() string      { return "as marked" }
func (e *asMarkedError) As(target any) bool { return errors.As(e.hidden, target) }

func TestMarkerPredicates_OutsidePanicOnly(t *testing.T) {
	base := errors.New("disk full")
	other := errors.New("other")
	inner := errors.New("rendered elsewhere")
	panicOf := func(v any) error { return &testPanic{value: v} }

	handledAround := panicOf("boom")
	bothPanic := panicOf(Handled(inner))
	joinPanic := panicOf("boom")

	tests := []struct {
		name         string
		err          error
		wantReported bool
		wantWritten  bool
		wantCause    error
	}{
		{name: "NoPanicReported", err: MarkReported(base), wantReported: true},
		{name: "NoPanicWrappedReported", err: fmt.Errorf("x: %w", MarkReported(base)), wantReported: true},
		{name: "NoPanicSentinel", err: ErrResponseWritten, wantWritten: true},
		{name: "NoPanicHandled", err: Handled(base), wantWritten: true, wantCause: base},
		{name: "ReportedInsidePanicOnly", err: panicOf(MarkReported(base))},
		{name: "WrappedPanicReportedInside", err: fmt.Errorf("mw: %w", panicOf(MarkReported(base)))},
		{name: "SentinelInsidePanicOnly", err: panicOf(ErrResponseWritten)},
		{name: "HandledInsidePanicOnly", err: panicOf(Handled(inner))},
		{name: "ReportedAroundPanic", err: MarkReported(panicOf("boom")), wantReported: true},
		{name: "HandledAroundPanic", err: Handled(handledAround), wantWritten: true, wantCause: handledAround},
		{name: "ReportedInsideAndAround", err: MarkReported(panicOf(MarkReported(base))), wantReported: true},
		{name: "HandledInsideAndAround", err: Handled(bothPanic), wantWritten: true, wantCause: bothPanic},
		{name: "JoinReportedSibling", err: errors.Join(joinPanic, MarkReported(other)), wantReported: true},
		{name: "JoinHandledSibling", err: errors.Join(joinPanic, Handled(other)), wantWritten: true, wantCause: other},
		{name: "JoinMarkersOnlyInsidePanic", err: errors.Join(panicOf(MarkReported(Handled(base))), other)},
		{name: "AsMethodReported", err: &asMarkedError{hidden: MarkReported(base)}, wantReported: true},
		{name: "AsMethodHandled", err: &asMarkedError{hidden: Handled(base)}, wantCause: base},
		{name: "PastWalkLimit", err: deepWrap(MarkReported(Handled(base)), chainWalkLimit+6), wantReported: true, wantWritten: true, wantCause: base},
		{name: "SentinelPastWalkLimit", err: deepWrap(ErrResponseWritten, chainWalkLimit+1), wantWritten: true},
		{name: "SentinelInsidePanicPastWalkLimit", err: deepWrap(panicOf(ErrResponseWritten), chainWalkLimit+1)},
		{name: "MarkersInsidePanicPastWalkLimit", err: deepWrap(panicOf(MarkReported(Handled(base))), chainWalkLimit+1)},
		{name: "JoinedPanicPastWalkLimit", err: deepWrap(errors.Join(other, panicOf(ErrResponseWritten)), chainWalkLimit+1)},
		{name: "SentinelAtMarkerCap", err: deepWrap(ErrResponseWritten, markerWalkCap-1), wantWritten: true},
		{name: "MarkersPastMarkerCap", err: deepWrap(MarkReported(Handled(base)), markerWalkCap+1)},
		{name: "SentinelPastMarkerCap", err: deepWrap(ErrResponseWritten, markerWalkCap)},
		{name: "WideJoinPastMarkerCap", err: wideJoin(MarkReported(Handled(base)), markerWalkCap)},
		{name: "WideJoinWithinMarkerCap", err: wideJoin(MarkReported(base), 100), wantReported: true},
		{name: "Unmarked", err: base},
		{name: "Nil", err: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsReported(tt.err); got != tt.wantReported {
				t.Errorf("IsReported = %v, want %v", got, tt.wantReported)
			}
			if got := IsResponseWritten(tt.err); got != tt.wantWritten {
				t.Errorf("IsResponseWritten = %v, want %v", got, tt.wantWritten)
			}
			if got := HandledCause(tt.err); got != tt.wantCause {
				t.Errorf("HandledCause = %v, want %v", got, tt.wantCause)
			}
		})
	}
}

// wideJoin joins n unmarked errors followed by last, so the walk visits
// n+2 nodes (the join, the n siblings, then last) before it reaches last.
func wideJoin(last error, n int) error {
	errs := make([]error, 0, n+1)
	for i := 0; i < n; i++ {
		errs = append(errs, fmt.Errorf("sibling %d", i))
	}
	return errors.Join(append(errs, last)...)
}

// TestMarkReported_AroundAMarkedPanic asserts a recovered panic whose value
// was marked can itself be marked: the marker inside the panic value does
// not count, so MarkReported wraps and the result reads as reported.
func TestMarkReported_AroundAMarkedPanic(t *testing.T) {
	base := errors.New("disk full")
	tests := []struct {
		name     string
		err      error
		wantWrap bool
	}{
		{name: "PanicWithMarkedValue", err: &testPanic{value: MarkReported(base)}, wantWrap: true},
		{name: "WrappedPanicWithMarkedValue", err: fmt.Errorf("mw: %w", &testPanic{value: MarkReported(base)}), wantWrap: true},
		{name: "PlainPanic", err: &testPanic{value: "boom"}, wantWrap: true},
		{name: "AlreadyMarkedAround", err: MarkReported(&testPanic{value: MarkReported(base)})},
		{name: "AlreadyMarked", err: MarkReported(base)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarkReported(tt.err)
			if wrapped := got != tt.err; wrapped != tt.wantWrap {
				t.Errorf("wrapped = %v, want %v", wrapped, tt.wantWrap)
			}
			if !IsReported(got) {
				t.Error("IsReported(MarkReported(err)) = false, want true")
			}
			if !errors.Is(got, tt.err) || got.Error() != tt.err.Error() {
				t.Error("the marker must stay transparent")
			}
		})
	}
}

func TestMarkerPredicates_Allocations(t *testing.T) {
	base := errors.New("disk full")
	tests := []struct {
		name string
		err  error
	}{
		{"direct marked", MarkReported(base)},
		{"wrapped handled", fmt.Errorf("mw: %w", Handled(base))},
		{"marker inside panic", &testPanic{value: MarkReported(Handled(base))}},
		{"handled around panic", Handled(&testPanic{value: "boom"})},
		{"joined", errors.Join(&testPanic{value: "boom"}, MarkReported(base))},
		{"nested joins", errors.Join(base, errors.Join(base, errors.Join(base, MarkReported(base))))},
		{"wide join", wideJoin(MarkReported(base), 40)},
		{"deep chain", deepWrap(Handled(base), chainWalkLimit*4)},
		{"deep panic", deepWrap(&testPanic{value: MarkReported(Handled(base))}, chainWalkLimit+1)},
		{"unmarked", fmt.Errorf("x: %w", base)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(100, func() {
				_ = IsReported(tt.err)
				_ = IsResponseWritten(tt.err)
				_ = HandledCause(tt.err)
			})
			if allocs != 0 {
				t.Errorf("marker predicates allocated %.0f times per call, want 0", allocs)
			}
		})
	}
}
