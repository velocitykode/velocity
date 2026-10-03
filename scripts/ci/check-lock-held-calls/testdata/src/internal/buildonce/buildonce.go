package buildonce

import "context"

// Group stands in for the module's internal/buildonce Group.
type Group[V any] struct{}

func (g *Group[V]) Do(ctx context.Context, key string, build func() (V, error)) (V, error) {
	return build()
}

// Serial stands in for the module's internal/buildonce Serial.
type Serial struct{}

func (s *Serial) Do(ctx context.Context, fn func()) error {
	fn()
	return nil
}
