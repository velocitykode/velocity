// Package ownctx stands in for the module's internal/ownctx: the checker
// knows its builders by package path and name. Like the real ones, they
// read the caller's context, so calling one under a lock is reach.
package ownctx

import "context"

func Bridge(ctx context.Context) context.Context {
	_ = ctx.Done()
	return context.Background()
}

func Detached(ctx context.Context) context.Context {
	_ = ctx.Value("id")
	return context.Background()
}
