// Package ownctx stands in for the module's internal/ownctx: the checker
// knows its builders by package path and name. Like the real ones, they
// read the caller's context, so calling one under a lock is reach.
package ownctx

import (
	"context"
	"time"
)

func Bridge(ctx context.Context) context.Context {
	_ = ctx.Done()
	return context.Background()
}

func Detached(ctx context.Context) context.Context {
	_ = ctx.Value("id")
	return context.Background()
}

// Held stands in for the module's ownctx.Held.
type Held struct{}

func (h *Held) Release() {}

func Hold(ctx context.Context) (context.Context, *Held) {
	_ = ctx.Done()
	return context.Background(), &Held{}
}

func HoldDetached(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc, *Held) {
	_ = ctx.Value("id")
	c, cancel := context.WithTimeout(context.Background(), d)
	return c, cancel, &Held{}
}
