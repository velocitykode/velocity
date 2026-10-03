package view

import (
	"net/http"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
)

// Redirect performs an SPA-compatible redirect using the view engine on
// ctx, whatever its type: the method is on contract.ViewEngine. With no
// view engine wired it writes nothing and returns the
// *contract.ServiceNotConfiguredError ctx.View reports.
func Redirect(ctx *router.Context, url string) error {
	v, err := ctx.View()
	if err != nil {
		return err
	}
	v.Redirect(ctx.Response, ctx.Request, url)
	return nil
}

// Location performs a same-origin full-page reload. The target is validated
// against the redirect host allowlist (safe for user-controlled input; an
// external host collapses to "/"). With no view engine wired it writes
// nothing and returns the missing-service error; with one that is not an
// *Engine, an error naming its type (see engineOf).
func Location(ctx *router.Context, url string) error {
	e, err := engineOf(ctx)
	if err != nil {
		return err
	}
	e.Location(ctx.Response, ctx.Request, url)
	return nil
}

// LocationExternal performs a full-page reload to an arbitrary external host
// (the explicit opt-out of Location's allowlist) using the view engine on
// ctx, whatever its type. SECURITY: only pass trusted or statically-known
// URLs. With no view engine wired it writes nothing and returns the
// missing-service error.
func LocationExternal(ctx *router.Context, url string) error {
	v, err := ctx.View()
	if err != nil {
		return err
	}
	v.LocationExternal(ctx.Response, ctx.Request, url)
	return nil
}

// Back redirects to the Referer (or "/" when missing) using the view
// engine on ctx, whatever its type. With no view engine wired it writes
// nothing and returns the missing-service error.
func Back(ctx *router.Context) error {
	v, err := ctx.View()
	if err != nil {
		return err
	}
	v.Back(ctx.Response, ctx.Request)
	return nil
}

// ReqEngine binds the view engine to a single request so handlers can
// chain flash and terminal calls:
//
//	re, err := view.For(ctx)
//	if err != nil {
//	    return err
//	}
//	re.Flash("error", msg).Redirect("/path")
//
// For reports a missing view engine, so a ReqEngine always has one; For's
// error is the one absence path.
type ReqEngine struct {
	ctx *router.Context
	e   *Engine
	w   http.ResponseWriter
	r   *http.Request
	bag contract.FlashBag
}

// For returns a request-bound view handle for chainable handler calls, or
// nil and the error engineOf reports: a *contract.ServiceNotConfiguredError
// when no view engine is wired on the context, an error naming the
// engine's type when it is not an *Engine.
func For(ctx *router.Context) (*ReqEngine, error) {
	e, err := engineOf(ctx)
	if err != nil {
		return nil, err
	}
	return &ReqEngine{ctx: ctx, e: e, w: ctx.Response, r: ctx.Request}, nil
}

// Flash sets a one-shot flash entry in the session flash bag
// (app.Services.FlashBag), the one channel every flash rides. The session
// middleware saves it with the response the terminal method (Redirect /
// Location / Back / Render) writes, and the next full render, or this
// Render, drains it onto Page.Flash.
//
// Returns the receiver for chaining. Silently no-ops when the request
// carries no session (the default scheme keeps none, e.g. JWT-only
// deployments).
func (re *ReqEngine) Flash(key string, value any) *ReqEngine {
	if re.bag == nil {
		services := re.ctx.ServicesIfSet()
		if services == nil || services.FlashBag == nil {
			return re
		}
		bag := services.FlashBag(re.r)
		if bag == nil {
			return re
		}
		re.bag = bag
	}
	re.bag.Flash(key, value)
	return re
}

// FlashMany sets multiple flash entries in one call. Returns the receiver
// for chaining. See Flash for a request without a session.
func (re *ReqEngine) FlashMany(values map[string]any) *ReqEngine {
	for k, v := range values {
		re.Flash(k, v)
	}
	return re
}

// Redirect performs an SPA-compatible redirect. The session middleware
// saves any pending flash bag with it, so the redirect target's render
// can drain it onto Page.Flash.
func (re *ReqEngine) Redirect(url string) {
	re.e.Redirect(re.w, re.r, url)
}

// Location performs a same-origin full-page reload (allowlist-validated,
// safe for user-controlled input). Any pending flash bag is saved with it.
func (re *ReqEngine) Location(url string) {
	re.e.Location(re.w, re.r, url)
}

// LocationExternal performs a full-page reload to an arbitrary external host
// (the explicit opt-out of Location's allowlist). Any pending flash bag is
// saved with it. SECURITY: only pass trusted or statically-known URLs.
func (re *ReqEngine) LocationExternal(url string) {
	re.e.LocationExternal(re.w, re.r, url)
}

// Back redirects to the Referer (or "/"). Any pending flash bag is saved
// with it.
func (re *ReqEngine) Back() {
	re.e.Back(re.w, re.r)
}

// Render renders an Inertia component. bond.Render drains any pending
// flash bag onto Page.Flash on this (full) response, and the session
// middleware saves the drained session with it.
func (re *ReqEngine) Render(component string, props ...Props) error {
	return re.e.Render(re.w, re.r, component, props...)
}
