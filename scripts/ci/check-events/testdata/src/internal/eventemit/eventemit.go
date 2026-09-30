// Package eventemit stands in for the framework's eventemit package: the
// emitter rule matches its Emitter methods and DispatchContained by type.
package eventemit

import (
	"context"

	"example.com/m/contract"
)

type Failures struct{ n int }

type Emitter struct{}

func (e *Emitter) Set(fn func(context.Context, any) error)     {}
func (e *Emitter) Share(f *Failures)                           {}
func (e *Emitter) Fail(ctx context.Context, err error, ev any) {}
func (e *Emitter) FailLater(ctx context.Context, err error, ev any) func() {
	return nil
}
func (e *Emitter) Dispatcher() func(context.Context, any) error { return nil }
func (e *Emitter) SetShared(fn func(context.Context, any) error, f *Failures, logger func() contract.Logger) {
}

func DispatchContained(ctx context.Context, dispatch func(context.Context, any) error, ev any) error {
	return nil
}
