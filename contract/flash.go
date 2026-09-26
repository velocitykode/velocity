package contract

// FlashBag is the one-shot flash storage of a visitor's session: entries a
// request sets are delivered to, and removed by, a later render. Every
// flash the framework carries across a redirect lives here, the messages a
// handler flashes for the page and the validation errors and old input a
// failed form flashes, so it all rides in the session and follows one
// lifetime.
//
// The session types of the auth package satisfy it.
type FlashBag interface {
	// Flash sets the entry key to value.
	Flash(key string, value any)
	// GetFlash returns the entry key and removes it, or nil when the bag
	// has no such entry.
	GetFlash(key string) any
	// FlushFlash returns every entry and empties the bag, or nil when the
	// bag is empty.
	FlushFlash() map[string]any
}
