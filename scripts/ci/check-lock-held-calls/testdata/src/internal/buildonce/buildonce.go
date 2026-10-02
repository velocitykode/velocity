package buildonce

import "context"

// Group stands in for the module's internal/buildonce Group.
type Group[V any] struct{}

func (g *Group[V]) Do(ctx context.Context, key string, build func() (V, error)) (V, error) {
	return build()
}
