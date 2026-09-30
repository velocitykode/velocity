package eventemittest

import (
	"context"

	"github.com/velocitykode/velocity/internal/eventemit"
)

// Receiving returns an emitter whose dispatcher hands every event, with
// its context, to receive and reports success.
func Receiving(receive func(ctx context.Context, event any)) *eventemit.Emitter {
	e := &eventemit.Emitter{}
	e.Set(func(ctx context.Context, event any) error {
		receive(ctx, event)
		return nil
	})
	return e
}
