package bond

import (
	"net/http"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
)

// Flash reaches the page through one channel, the session flash bag
// (app.Services.FlashBag), and every entry is removed from the bag only on
// the render that delivers it:
//
//   - validation errors (router.FlashErrorsKey) and old input
//     (router.FlashInputKey) are drained on every render and delivered as
//     the "errors" and "old" props as always props, so a partial reload
//     whose only list does not name them still receives them;
//   - every other entry is a message for Page.Flash and is drained only
//     on a full render: Inertia clients skip the flash event on partial
//     and deferred-prop responses, so a partial reload leaves the messages
//     in the bag for the next full render.

// flashBagFor returns the session flash bag of r, or nil when r was not
// routed through an app with sessions (a Bond built directly in a unit
// test, an app whose default scheme keeps no session).
func flashBagFor(r *http.Request) contract.FlashBag {
	services := router.ServicesFromRequest(r)
	if services == nil || services.FlashBag == nil {
		return nil
	}
	return services.FlashBag(r)
}

// applyFlashData drains the flashed validation errors and old input from
// the request's session flash bag into props as the "errors" and "old"
// always props. Flashed values override props of the same name so a
// redirect back with errors always wins.
func applyFlashData(r *http.Request, props Props) {
	bag := flashBagFor(r)
	if bag == nil {
		return
	}
	if errs := bag.GetFlash(router.FlashErrorsKey); errs != nil {
		props["errors"] = Always(errs)
	}
	if old := bag.GetFlash(router.FlashInputKey); old != nil {
		props["old"] = Always(old)
	}
}

// flashFor drains the messages left in the request's session flash bag for
// Page.Flash, or returns nil when there are none. Render calls it on full
// renders only, after applyFlashData took the errors and old input out.
func flashFor(r *http.Request) map[string]any {
	bag := flashBagFor(r)
	if bag == nil {
		return nil
	}
	messages := bag.FlushFlash()
	if len(messages) == 0 {
		return nil
	}
	return messages
}
