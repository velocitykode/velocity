package bond

import "net/http"

// FlashReader returns one-shot flash data for the request. Bond calls
// the configured reader during Render and merges the result onto
// Page.Flash per the Inertia v2 flash protocol. Returning nil or an
// empty map omits the field from the rendered payload.
//
// The reader is expected to clear the flash bag as part of the read
// (one-shot semantics). Bond does not cache the result; it calls the
// reader exactly once per Render.
type FlashReader func(w http.ResponseWriter, r *http.Request) map[string]any

// SetFlashReader wires the per-request flash reader. Bond stays
// decoupled from auth/session storage; the framework (or consumer)
// installs an adapter that forwards to its session bag.
//
// Typical wiring forwards to auth.Manager.Session(r).FlushFlash().
func (b *Bond) SetFlashReader(fn FlashReader) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flashReader = fn
}

// flashFor returns the flash bag from the configured reader, or nil
// when no reader is wired or the bag is empty.
func (b *Bond) flashFor(w http.ResponseWriter, r *http.Request) map[string]any {
	b.mu.RLock()
	fn := b.flashReader
	b.mu.RUnlock()
	if fn == nil {
		return nil
	}
	bag := fn(w, r)
	if len(bag) == 0 {
		return nil
	}
	return bag
}
