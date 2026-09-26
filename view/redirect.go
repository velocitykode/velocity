package view

import (
	"net/http"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/router"
)

// Redirect performs an SPA-compatible redirect using the engine on ctx.
// No-op when no view engine is wired on the context.
func Redirect(ctx *router.Context, url string) {
	if e := FromContext(ctx); e != nil {
		e.Redirect(ctx.Response, ctx.Request, url)
	}
}

// Location performs a same-origin full-page reload. The target is validated
// against the redirect host allowlist (safe for user-controlled input; an
// external host collapses to "/"). No-op when no view engine is wired.
func Location(ctx *router.Context, url string) {
	if e := FromContext(ctx); e != nil {
		e.Location(ctx.Response, ctx.Request, url)
	}
}

// LocationExternal performs a full-page reload to an arbitrary external host
// (the explicit opt-out of Location's allowlist). SECURITY: only pass trusted
// or statically-known URLs. No-op when no view engine is wired.
func LocationExternal(ctx *router.Context, url string) {
	if e := FromContext(ctx); e != nil {
		e.LocationExternal(ctx.Response, ctx.Request, url)
	}
}

// Back redirects to the Referer (or "/" when missing). No-op when no
// view engine is wired.
func Back(ctx *router.Context) {
	if e := FromContext(ctx); e != nil {
		e.Back(ctx.Response, ctx.Request)
	}
}

// ReqEngine binds the view engine to a single request so handlers can
// chain flash and terminal calls:
//
//	view.For(ctx).Flash("error", msg).Redirect("/path")
//
// All methods are nil-safe: when no view engine is wired on the request
// context, For returns nil and every chain method is a no-op, except
// Render, which returns ErrNoEngine so the handler's error reaches the
// error pipeline instead of an empty response.
type ReqEngine struct {
	ctx  *router.Context
	e    *Engine
	w    http.ResponseWriter
	r    *http.Request
	sess auth.Session
}

// For returns a request-bound view handle for chainable handler calls,
// or nil when no view engine is wired on the context.
func For(ctx *router.Context) *ReqEngine {
	e := FromContext(ctx)
	if e == nil {
		return nil
	}
	return &ReqEngine{ctx: ctx, e: e, w: ctx.Response, r: ctx.Request}
}

// Flash sets a one-shot flash entry on the request's session bag. The
// session middleware persists the bag into the encrypted session cookie
// before the response a terminal method (Redirect / Location / Back /
// Render) writes, so the next request, or this Render, drains it.
//
// Returns the receiver for chaining. Silently no-ops when no auth manager
// is on the context or the default scheme does not back sessions (e.g.
// JWT-only deployments).
func (re *ReqEngine) Flash(key string, value any) *ReqEngine {
	if re == nil {
		return nil
	}
	if re.sess == nil {
		mgr := auth.FromContext(re.ctx)
		if mgr == nil {
			return re
		}
		sess := mgr.Session(re.r)
		if sess == nil {
			return re
		}
		re.sess = sess
	}
	re.sess.Flash(key, value)
	return re
}

// FlashMany sets multiple flash entries in one call. Returns the receiver
// for chaining. See Flash for nil semantics.
func (re *ReqEngine) FlashMany(values map[string]any) *ReqEngine {
	if re == nil {
		return nil
	}
	for k, v := range values {
		re.Flash(k, v)
	}
	return re
}

// Redirect performs an SPA-compatible redirect. The session middleware
// saves any pending flash bag with it, so the redirect target's render
// can drain it onto Page.Flash.
func (re *ReqEngine) Redirect(url string) {
	if re == nil {
		return
	}
	re.e.Redirect(re.w, re.r, url)
}

// Location performs a same-origin full-page reload (allowlist-validated,
// safe for user-controlled input). Any pending flash bag is saved with it.
func (re *ReqEngine) Location(url string) {
	if re == nil {
		return
	}
	re.e.Location(re.w, re.r, url)
}

// LocationExternal performs a full-page reload to an arbitrary external host
// (the explicit opt-out of Location's allowlist). Any pending flash bag is
// saved with it. SECURITY: only pass trusted or statically-known URLs.
func (re *ReqEngine) LocationExternal(url string) {
	if re == nil {
		return
	}
	re.e.LocationExternal(re.w, re.r, url)
}

// Back redirects to the Referer (or "/"). Any pending flash bag is saved
// with it.
func (re *ReqEngine) Back() {
	if re == nil {
		return
	}
	re.e.Back(re.w, re.r)
}

// Render renders an Inertia component. bond.Render's flash reader drains
// any pending flash bag onto Page.Flash on this response, and the session
// middleware saves the drained session with it. On a nil receiver (no
// view engine wired) it returns ErrNoEngine and writes nothing.
func (re *ReqEngine) Render(component string, props ...Props) error {
	if re == nil {
		return ErrNoEngine
	}
	return re.e.Render(re.w, re.r, component, props...)
}
