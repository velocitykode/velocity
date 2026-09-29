package eventemit

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// DispatchContained returns the dispatcher's own error unchanged and a
// panic in it as the recovered panic, without letting it escape.
func TestDispatchContained(t *testing.T) {
	ctx := context.Background()
	errBroke := errors.New("broke")
	if err := DispatchContained(ctx, func(context.Context, any) error { return nil }, "evt"); err != nil {
		t.Errorf("nil dispatch error = %v, want nil", err)
	}
	if err := DispatchContained(ctx, func(context.Context, any) error { return errBroke }, "evt"); err != errBroke {
		t.Errorf("error = %v, want %v", err, errBroke)
	}
	var rp contract.RecoveredPanic
	err := DispatchContained(ctx, func(context.Context, any) error { panic("dispatcher broke") }, "evt")
	if !errors.As(err, &rp) || rp.Recovered() != "dispatcher broke" {
		t.Errorf("error = %v, want the recovered panic", err)
	}
}
