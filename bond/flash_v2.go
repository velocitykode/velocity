package bond

import "net/http"

// FlashProvider returns one-shot flash data for the request. Bond calls
// the configured provider during Render and merges the result onto
// Page.Flash per the Inertia v2 flash protocol. Returning nil or an
// empty map omits the field from the rendered payload.
//
// The provider is expected to clear the flash bag as part of the read
// (one-shot semantics). Bond does not cache the result; it calls the
// provider exactly once per Render.
type FlashProvider func(w http.ResponseWriter, r *http.Request) map[string]any

// SetFlashProvider wires the per-request flash provider. Bond stays
// decoupled from auth/session storage; the framework (or consumer)
// installs an adapter that forwards to its session bag.
//
// Typical wiring forwards to auth.Manager.Session(r).FlushFlash().
func (b *Bond) SetFlashProvider(fn FlashProvider) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flashProvider = fn
}

// flashFor returns the flash bag from the configured provider, or nil
// when no provider is wired or the bag is empty.
func (b *Bond) flashFor(w http.ResponseWriter, r *http.Request) map[string]any {
	b.mu.RLock()
	fn := b.flashProvider
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
