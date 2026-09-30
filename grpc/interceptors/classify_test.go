package interceptors

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/internal/errchain"
)

// wideChain joins more errors than a walk looks at, with first at the
// front and last at the end.
type wideChain struct{ first, last error }

func (wideChain) Error() string { return "wide" }
func (w wideChain) Unwrap() []error {
	errs := []error{w.first}
	for i := 0; i < 2*errchain.Max; i++ {
		errs = append(errs, errors.New("filler"))
	}
	return append(errs, w.last)
}

// A chain cut short still classifies by a status found before the cut; one
// cut short before any status is Internal with the budget reason, never
// the Unknown a shorter chain would give.
func TestClassify_BudgetExhausted(t *testing.T) {
	found := classify(context.Background(), wideChain{first: status.Error(codes.NotFound, "x"), last: errors.New("y")})
	if found.code != codes.NotFound || found.reason != "" {
		t.Errorf("status before the cut: code %v reason %q, want NotFound without a reason", found.code, found.reason)
	}
	cut := classify(context.Background(), wideChain{first: errors.New("x"), last: status.Error(codes.NotFound, "y")})
	if cut.code != codes.Internal || cut.reason != reasonBudgetExhausted || cut.wire != internalError {
		t.Errorf("status past the cut: %+v, want Internal with %q", cut, reasonBudgetExhausted)
	}
	if !cut.reportable() {
		t.Error("an unclassifiable error is not reportable")
	}
}

// The call's own context error is not reportable; a nil error classifies
// as OK with no wire error.
func TestClassify_ContextAndNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := classify(ctx, context.Canceled)
	if o.code != codes.Canceled || !o.ctxEnded || o.reportable() {
		t.Errorf("own context error: %+v, want Canceled, ctxEnded, not reportable", o)
	}
	if o := classify(context.Background(), nil); o.code != codes.OK || o.wire != nil || o.reportable() {
		t.Errorf("nil error: %+v, want OK without a wire error", o)
	}
}
