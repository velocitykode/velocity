package csrf

import (
	"context"

	"example.com/lockheld/internal/buildonce"
)

// records is a pluggable record store: an interface.
type records interface {
	Get(ctx context.Context, id string) (string, error)
	Delete(ctx context.Context, id string) error
	Forget(key string) error
	ForgetCtx(ctx context.Context, key string) error
	CompareAndDeleteCtx(ctx context.Context, id string, expected any) (bool, error)
	UpdateData(ctx context.Context, id string, update func(string) (string, error)) error
}

// table is a concrete store: its own methods, not a pluggable one.
type table struct{}

func (table) Delete(context.Context, string) error { return nil }

type reaper struct {
	r       records
	t       table
	flights buildonce.Group[string]
}

// Read, decide, delete: the record removed may be one renewed since the
// read. Flagged at the delete.
func (p *reaper) expire(ctx context.Context, id string) error {
	rec, err := p.r.Get(ctx, id)
	if err != nil || rec != "expired" {
		return err
	}
	return p.r.Delete(ctx, id) // want rmw
}

// The read may be the caller's: a delete with no read in sight is flagged
// too, by every delete name, also inside a nested literal.
func (p *reaper) evict(ctx context.Context, id string) func() {
	_ = p.r.Forget(id)         // want rmw
	_ = p.r.ForgetCtx(ctx, id) // want rmw
	return func() {
		_ = p.r.Delete(ctx, id) // want rmw
	}
}

// The store's conditional removals: not flagged.
func (p *reaper) conditional(ctx context.Context, id, read string) error {
	if _, err := p.r.CompareAndDeleteCtx(ctx, id, read); err != nil {
		return err
	}
	return p.r.UpdateData(ctx, id, func(string) (string, error) { return "", nil })
}

// Inside the per-key flight: not flagged.
func (p *reaper) flight(ctx context.Context, id string) (string, error) {
	return p.flights.Do(ctx, id, func() (string, error) {
		return "", p.r.Delete(ctx, id)
	})
}

// A concrete store: not flagged.
func (p *reaper) concrete(ctx context.Context, id string) {
	_ = p.t.Delete(ctx, id)
}

// A delete that is right whatever the record holds carries the marker; a
// marker on a line with no delete is stale.
func (p *reaper) destroy(ctx context.Context, id string) {
	_ = p.r.Delete(ctx, id) //store-rmw-ok: a destroy ends the session whatever its record holds
	_ = id                  //store-rmw-ok: no delete on this line any more // want stale
}
