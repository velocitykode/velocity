package csrf

import (
	"context"

	"example.com/lockheld/internal/buildonce"
)

// store is a pluggable token store: an interface.
type store interface {
	Get(ctx context.Context, id string) (string, error)
	Set(ctx context.Context, id, token string) error
	LoadOrStore(ctx context.Context, id, token string) (string, error)
}

// memory is a concrete store: its own methods, not a pluggable one.
type memory struct{}

func (memory) Get(context.Context, string) (string, error) { return "", nil }
func (memory) Set(context.Context, string, string) error   { return nil }

type protector struct {
	s     store
	m     memory
	mints buildonce.Group[string]
}

// Read, then blind write of the same key: flagged at the write.
func (p *protector) mint(ctx context.Context, id string) (string, error) {
	if t, err := p.s.Get(ctx, id); err == nil {
		return t, nil
	}
	if err := p.s.Set(ctx, id, "new"); err != nil { // want rmw
		return "", err
	}
	return "new", nil
}

// The write inside a nested literal still follows the read.
func (p *protector) mintLater(ctx context.Context, id string) func() error {
	_, _ = p.s.Get(ctx, id)
	return func() error {
		return p.s.Set(ctx, id, "new") // want rmw
	}
}

// Compare-and-set: not a writer.
func (p *protector) insert(ctx context.Context, id string) (string, error) {
	if t, err := p.s.Get(ctx, id); err == nil {
		return t, nil
	}
	return p.s.LoadOrStore(ctx, id, "new")
}

// Inside the per-key flight: not flagged.
func (p *protector) flight(ctx context.Context, id string) (string, error) {
	if t, err := p.s.Get(ctx, id); err == nil {
		return t, nil
	}
	return p.mints.Do(ctx, id, func() (string, error) {
		return "new", p.s.Set(ctx, id, "new")
	})
}

// A write of another key, a write before the read, a write through
// another receiver: not a read-then-write of one key.
func (p *protector) rotate(ctx context.Context, oldID, newID string, other store) {
	_ = p.s.Set(ctx, oldID, "first")
	_, _ = p.s.Get(ctx, oldID)
	_ = p.s.Set(ctx, newID, "new")
	_ = other.Set(ctx, oldID, "new")
}

// A concrete store: not flagged.
func (p *protector) concrete(ctx context.Context, id string) {
	_, _ = p.m.Get(ctx, id)
	_ = p.m.Set(ctx, id, "new")
}

// A suppressed pair, and a stale marker.
func (p *protector) suppressed(ctx context.Context, id string) {
	_, _ = p.s.Get(ctx, id)
	_ = p.s.Set(ctx, id, "new") //store-rmw-ok: the key is this request's alone
	_ = id                      //store-rmw-ok: nothing on this line any more // want stale
}
