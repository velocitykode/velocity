package router

import (
	"context"
	"net/http"
	"sync"

	"github.com/velocitykode/velocity/app"
)

// paramsKey is the context key for route parameters
type paramsKey struct{}

// routeNameKey is the context key for current route name
type routeNameKey struct{}

// routeDataKey is the context key for the bundled per-route metadata.
type routeDataKey struct{}

// routeData bundles the per-matched-route context values (params, route
// name, route pattern, services) so a matched request clones its
// context exactly once for all of them. The matcher's slice/values form
// is kept on result; the map() form is built lazily by paramsMap and
// cached here per request, so repeat consumers (event population plus any
// Params/GetParams calls) share one map instead of rebuilding it. The map
// is cached on this per-request bundle, never on the shared compiled or
// static MatchResult.
type routeData struct {
	result     *MatchResult
	services   *app.Services
	params     map[string]string
	paramsOnce sync.Once
}

// routeDataContext wraps a parent context so the bundled routeData is
// reachable both through the private routeDataKey fast path and through
// the exported RoutePatternKey, preserving the public context-key
// behavior without cloning the context a second time.
type routeDataContext struct {
	context.Context
	rd *routeData
}

// Value returns the bundled routeData for routeDataKey and the matched
// route pattern for the exported RoutePatternKey, so callers reading the
// documented context key directly still see the pattern.
func (c routeDataContext) Value(key any) any {
	switch key.(type) {
	case routeDataKey:
		return c.rd
	}
	if key == RoutePatternKey && c.rd.result != nil {
		return c.rd.result.Path
	}
	return c.Context.Value(key)
}

// paramsMap returns the route params map, materializing it from the
// bundled result on first call and caching it for subsequent reads.
// Safe to cache here because routeData is per request; the underlying
// MatchResult may be a shared compiled/static instance. A single
// matched request can be read by multiple goroutines, so the lazy build
// is guarded by sync.Once to keep concurrent first readers race-free.
func (rd *routeData) paramsMap() map[string]string {
	rd.paramsOnce.Do(func() {
		// Per-request tree results (buildMatchResult) materialize the map,
		// which is a non-nil empty map for no-capture static/root matches,
		// preserving Tree.Match's prior Params/GetParams/RequestRouted.Params
		// contract. Shared compiled/static results carry no matchedValues and
		// are not treeMatched; the old fast path stored a nil Params for them,
		// so keep returning nil here.
		if rd.result.treeMatched {
			rd.params = rd.result.paramMap()
		}
	})
	return rd.params
}

// SetParams stores route parameters in the request context
// Returns a new request with the parameters stored
func SetParams(r *http.Request, params map[string]string) *http.Request {
	ctx := context.WithValue(r.Context(), paramsKey{}, params)
	return r.WithContext(ctx)
}

// GetParams retrieves route parameters from the request context. For a
// matched route the map is materialized lazily from the bundled
// routeData; SetParams-stored maps (used outside the matched path) are
// returned as-is.
func GetParams(r *http.Request) map[string]string {
	// A SetParams override layered above the matched route wins
	// (last-writer-wins, matching the unbundled path); fall back to the
	// bundled routeData for the common no-override case.
	if params, ok := r.Context().Value(paramsKey{}).(map[string]string); ok {
		return params
	}
	if rd, ok := r.Context().Value(routeDataKey{}).(*routeData); ok && rd.result != nil {
		return rd.paramsMap()
	}
	return nil
}

// SetRouteName stores the current route name in the request context
func SetRouteName(r *http.Request, name string) *http.Request {
	ctx := context.WithValue(r.Context(), routeNameKey{}, name)
	return r.WithContext(ctx)
}

// GetRouteName retrieves the current route name from the request context
func GetRouteName(r *http.Request) string {
	// A SetRouteName override layered above the matched route wins
	// (last-writer-wins); fall back to the bundled routeData otherwise.
	if name, ok := r.Context().Value(routeNameKey{}).(string); ok {
		return name
	}
	if rd, ok := r.Context().Value(routeDataKey{}).(*routeData); ok && rd.result != nil {
		return rd.result.Name
	}
	return ""
}
