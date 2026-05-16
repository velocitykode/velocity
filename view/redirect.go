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

// Location performs an external redirect that forces a full-page reload.
// No-op when no view engine is wired.
func Location(ctx *router.Context, url string) {
	if e := FromContext(ctx); e != nil {
		e.Location(ctx.Response, ctx.Request, url)
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
// context, For returns nil and every chain method is a no-op.
type ReqEngine struct {
	ctx     *router.Context
	e       *Engine
	w       http.ResponseWriter
	r       *http.Request
	sess    auth.Session
	flashed bool
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

// Flash sets a one-shot flash entry on the request's session bag. The bag
// is persisted into the encrypted session cookie when a terminal method
// (Redirect / Location / Back / Render) is invoked on the same chain.
//
// Returns the receiver for chaining. Silently no-ops when no auth manager
// is on the context or the default guard does not back sessions (e.g.
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
	re.flashed = true
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

// Redirect performs an SPA-compatible redirect, persisting any pending
// flash bag onto the session cookie first so the redirect target's render
// can drain it onto Page.Flash.
func (re *ReqEngine) Redirect(url string) {
	if re == nil {
		return
	}
	re.commitSession()
	re.e.Redirect(re.w, re.r, url)
}

// Location performs an external redirect (full-page reload), persisting
// any pending flash bag first.
func (re *ReqEngine) Location(url string) {
	if re == nil {
		return
	}
	re.commitSession()
	re.e.Location(re.w, re.r, url)
}

// Back redirects to the Referer (or "/"), persisting any pending flash
// bag first.
func (re *ReqEngine) Back() {
	if re == nil {
		return
	}
	re.commitSession()
	re.e.Back(re.w, re.r)
}

// Render renders an Inertia component, persisting any pending flash bag
// first so bond.Render's flash provider drains the same bag onto
// Page.Flash on this response.
func (re *ReqEngine) Render(component string, props ...Props) error {
	if re == nil {
		return nil
	}
	re.commitSession()
	return re.e.Render(re.w, re.r, component, props...)
}

// commitSession writes the session cookie when Flash was called on this
// chain. Save errors are swallowed: a failed cookie write is a transport
// problem the handler cannot reasonably recover from, and bond's
// downstream render path will surface a clear error if the response is
// already in a bad state.
func (re *ReqEngine) commitSession() {
	if re == nil || !re.flashed || re.sess == nil {
		return
	}
	_ = re.sess.Save(re.w)
}
