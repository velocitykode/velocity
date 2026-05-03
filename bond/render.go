package bond

import (
	"context"
	"encoding/json"
	"html"
	"html/template"
	"net/http"
	"strings"
)

// Render renders a component with props
func (b *Bond) Render(w http.ResponseWriter, r *http.Request, component string, props Props) error {
	// 1. Merge shared props with component props
	mergedProps := b.mergeSharedProps(r, props)

	// 1.5. Apply flash data (validation errors + old input from cookies).
	// Flash data overrides component props so redirect-back-with-errors wins.
	applyFlashData(w, r, mergedProps)

	// 2. Check if this is a partial reload
	isPartial := b.isPartialReload(r, component)

	// 3. Extract deferred prop groups (before resolution)
	deferredGroups := b.extractDeferredGroups(mergedProps)

	// 4. Resolve props based on request type
	resolvedProps, pageMeta, err := b.resolveProps(r, mergedProps, isPartial)
	if err != nil {
		return err
	}

	// 5. Build page object
	page := Page{
		Component:      component,
		Props:          resolvedProps,
		URL:            r.URL.String(),
		Version:        b.version,
		EncryptHistory: b.encryptHistory,
		DeferredProps:  deferredGroups,
		MergeProps:     pageMeta.mergeProps,
		PrependProps:   pageMeta.prependProps,
		DeepMergeProps: pageMeta.deepMergeProps,
		MatchPropsOn:   pageMeta.matchPropsOn,
		ScrollProps:    pageMeta.scrollProps,
		OnceProps:      pageMeta.onceProps,
	}

	// 6. Clear merge metadata on reset
	if isPartial && r.Header.Get(HeaderReset) != "" {
		resetKeys := splitHeader(r.Header.Get(HeaderReset))
		page.MergeProps = removeKeys(page.MergeProps, resetKeys)
		page.PrependProps = removeKeys(page.PrependProps, resetKeys)
		page.DeepMergeProps = removeKeys(page.DeepMergeProps, resetKeys)
	}

	// 7. Route to appropriate renderer
	if isInertiaRequest(r) {
		return b.renderJSON(w, page)
	}
	return b.renderHTML(r.Context(), w, page)
}

// pageMeta collects merge/once/scroll metadata during prop resolution.
type pageMeta struct {
	mergeProps     []string
	prependProps   []string
	deepMergeProps []string
	matchPropsOn   map[string][]string
	scrollProps    map[string]ScrollMeta
	onceProps      map[string]OnceMeta
}

func (pm *pageMeta) addMerge(key string) {
	pm.mergeProps = appendUnique(pm.mergeProps, key)
}

func (pm *pageMeta) addPrepend(key string) {
	pm.prependProps = appendUnique(pm.prependProps, key)
}

func (pm *pageMeta) addDeepMerge(key string) {
	pm.deepMergeProps = appendUnique(pm.deepMergeProps, key)
}

func (pm *pageMeta) addMatchOn(key string, keys []string) {
	if len(keys) == 0 {
		return
	}
	if pm.matchPropsOn == nil {
		pm.matchPropsOn = make(map[string][]string)
	}
	pm.matchPropsOn[key] = keys
}

func (pm *pageMeta) addScroll(key string, meta ScrollMeta) {
	if pm.scrollProps == nil {
		pm.scrollProps = make(map[string]ScrollMeta)
	}
	pm.scrollProps[key] = meta
}

func (pm *pageMeta) addOnce(key string, meta OnceMeta) {
	if pm.onceProps == nil {
		pm.onceProps = make(map[string]OnceMeta)
	}
	pm.onceProps[key] = meta
}

// templateDataKey is the context key for per-request root-template variables
// (e.g. CSP nonce, CSRF token). Middleware sets entries via WithTemplateData;
// renderHTML merges them into the template Execute map.
type templateDataKey struct{}

// WithTemplateData returns a context that carries an additional root-template
// variable. Calls compose: each WithTemplateData adds its key to a map shared
// across the request. Middleware uses this to publish per-request data
// (cspNonce, csrfToken) so app.go.html can stamp them onto inline tags.
func WithTemplateData(ctx context.Context, key string, value any) context.Context {
	existing, _ := ctx.Value(templateDataKey{}).(map[string]any)
	merged := make(map[string]any, len(existing)+1)
	for k, v := range existing {
		merged[k] = v
	}
	merged[key] = value
	return context.WithValue(ctx, templateDataKey{}, merged)
}

// templateDataFromContext returns the template variables published via
// WithTemplateData on this request's context, or nil when none are set.
func templateDataFromContext(ctx context.Context) map[string]any {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(templateDataKey{}).(map[string]any)
	return m
}

// renderHTML renders a full HTML page with embedded Inertia data.
// When an SSR gateway is configured, the page is pre-rendered and the
// resulting HTML + head tags are spliced into the template. Any SSR
// failure falls back transparently to the CSR path, users never see
// a 500 because the renderer couldn't reach the SSR server.
func (b *Bond) renderHTML(ctx context.Context, w http.ResponseWriter, page Page) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	b.mu.RLock()
	gw := b.ssr
	b.mu.RUnlock()

	if gw != nil {
		ssrResp, ssrErr := gw.Dispatch(ctx, page)
		if ssrErr != nil {
			// ThrowOnError mode: surface the SSR failure instead of
			// silently rendering CSR. Used by E2E tests.
			return ssrErr
		}
		if ssrResp != nil && ssrResp.Body != "" {
			// v3 SSR bodies are self-contained, they include both the
			// `<script data-page>` JSON payload and the `<div id="app">`
			// container. Emit directly without re-wrapping to avoid
			// nested id="app" elements.
			return b.template.Execute(w, buildTemplateData(ctx, map[string]any{
				"inertia":     template.HTML(ssrResp.Body),
				"inertiaHead": template.HTML(strings.Join(ssrResp.Head, "\n")),
			}))
		}
	}

	// CSR: emit the v3 dual-format so both the @inertiajs/vite plugin's
	// auto-setup (reads <script data-page="app">) and the legacy
	// data-page attribute path (reads the div) find the page data.
	pageJSONRaw, err := page.ToJSON()
	if err != nil {
		return err
	}
	pageJSONAttr, err := page.ToHTMLAttr()
	if err != nil {
		return err
	}

	return b.template.Execute(w, buildTemplateData(ctx, map[string]any{
		"inertia":     template.HTML(b.buildInertiaContainer(pageJSONRaw, pageJSONAttr, cspNonceFromContext(ctx))),
		"inertiaHead": template.HTML(""),
	}))
}

// CSPNonceKey is the conventional template-data / context key under which
// callers publish the request's Content-Security-Policy nonce. Bond reads
// it to stamp `nonce="..."` onto the inline page-data script tag so a
// strict, nonce-bound script-src (the CSP3 default for production) does
// not block Inertia's bootstrap data.
//
// Apps that want a different key should set it via WithTemplateData under
// CSPNonceKey explicitly to keep bond and the root template in agreement.
const CSPNonceKey = "cspNonce"

// cspNonceFromContext returns the cspNonce template-data value, or empty.
// Empty means caller did not publish a nonce; bond emits the page-data
// script without a nonce attribute and the policy must not require one.
func cspNonceFromContext(ctx context.Context) string {
	data := templateDataFromContext(ctx)
	if data == nil {
		return ""
	}
	v, _ := data[CSPNonceKey].(string)
	return v
}

// buildTemplateData merges per-request template variables (set via
// WithTemplateData) under the renderer's static keys. Static keys win on
// collision so middleware cannot accidentally clobber `inertia` itself.
func buildTemplateData(ctx context.Context, base map[string]any) map[string]any {
	extras := templateDataFromContext(ctx)
	if len(extras) == 0 {
		return base
	}
	out := make(map[string]any, len(base)+len(extras))
	for k, v := range extras {
		out[k] = v
	}
	for k, v := range base {
		out[k] = v
	}
	return out
}

// renderJSON renders a JSON response for Inertia XHR requests
func (b *Bond) renderJSON(w http.ResponseWriter, page Page) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Inertia", "true")
	w.Header().Set("Vary", "X-Inertia")

	return json.NewEncoder(w).Encode(page)
}

// buildInertiaDiv constructs the Inertia container div in the legacy
// attribute-based format. Kept for tests and callers that need only
// the container. Prefer buildInertiaContainer for renderHTML output.
func (b *Bond) buildInertiaDiv(pageJSON string) string {
	return `<div id="` + html.EscapeString(b.containerID) + `" data-page='` + pageJSON + `'></div>`
}

// buildInertiaContainer emits the v3 dual-format container: a JSON
// script tag (preferred by @inertiajs/vite's auto-setup) plus a div
// with the legacy data-page attribute (preferred by older clients and
// by custom setup callbacks that read from the DOM directly). The
// script goes first so v3 clients find it before touching the div.
//
// pageJSONRaw is the unescaped JSON for embedding in a <script type=
// "application/json"> block. pageJSONAttr is the HTML-attribute-safe
// JSON for the legacy data-page attribute.
func (b *Bond) buildInertiaContainer(pageJSONRaw, pageJSONAttr, nonce string) string {
	id := html.EscapeString(b.containerID)
	// The </script> closing tag inside JSON string values would break
	// out of the script block; escape the forward slash in </script>
	// to </scr\/ipt> as the Inertia protocol expects.
	pageJSONRaw = strings.ReplaceAll(pageJSONRaw, "</script>", `<\/script>`)
	nonceAttr := ""
	if nonce != "" {
		nonceAttr = ` nonce="` + html.EscapeString(nonce) + `"`
	}
	return `<script id="` + id + `-page" type="application/json" data-page="` + id + `"` + nonceAttr + `>` +
		pageJSONRaw +
		`</script>` +
		`<div id="` + id + `" data-page='` + pageJSONAttr + `'></div>`
}

// isPartialReload checks if this is a partial reload for the given component
func (b *Bond) isPartialReload(r *http.Request, component string) bool {
	if r.Header.Get(HeaderPartialComponent) != component {
		return false
	}
	return r.Header.Get(HeaderPartialOnly) != "" ||
		r.Header.Get(HeaderPartialExcept) != ""
}

// getPartialOnly returns the list of props to include in partial reload
func getPartialOnly(r *http.Request) []string {
	data := r.Header.Get(HeaderPartialOnly)
	if data == "" {
		return nil
	}
	return strings.Split(data, ",")
}

// getPartialExcept returns the list of props to exclude in partial reload
func getPartialExcept(r *http.Request) []string {
	data := r.Header.Get(HeaderPartialExcept)
	if data == "" {
		return nil
	}
	return strings.Split(data, ",")
}

// getExceptOnceProps returns once-prop keys the client already has.
func getExceptOnceProps(r *http.Request) []string {
	data := r.Header.Get(HeaderExceptOnceProps)
	if data == "" {
		return nil
	}
	return strings.Split(data, ",")
}

// getScrollIntent returns the infinite scroll merge intent (prepend/append).
func getScrollIntent(r *http.Request) string {
	return r.Header.Get(HeaderInfiniteScrollIntent)
}

// extractDeferredGroups builds the deferredProps map for the Page.
// Handles both *DeferredProp and *ScrollProp with Defer().
func (b *Bond) extractDeferredGroups(props Props) map[string][]string {
	groups := make(map[string][]string)

	for key, value := range props {
		switch v := value.(type) {
		case *DeferredProp:
			groups[v.group] = append(groups[v.group], key)
		case *ScrollProp:
			if v.deferred {
				groups[v.group] = append(groups[v.group], key)
			}
		}
	}

	if len(groups) == 0 {
		return nil
	}
	return groups
}

// resolveProps processes all props based on request type and collects page metadata.
func (b *Bond) resolveProps(r *http.Request, props Props, isPartial bool) (Props, pageMeta, error) {
	resolved := make(Props)
	meta := pageMeta{}

	onlyProps := getPartialOnly(r)
	exceptProps := getPartialExcept(r)
	exceptOnce := getExceptOnceProps(r)
	scrollIntent := getScrollIntent(r)

	for key, value := range props {
		switch v := value.(type) {
		case LazyProp:
			// Lazy: only include if partial reload explicitly requests it
			if isPartial && contains(onlyProps, key) {
				val, err := v.Evaluate()
				if err != nil {
					return nil, meta, err
				}
				resolved[key] = val
			}

		case *DeferredProp:
			// Deferred: only evaluate on explicit partial reload request
			if isPartial && contains(onlyProps, key) {
				// Skip if once and already seen
				if v.once && contains(exceptOnce, key) {
					continue
				}
				val, err := v.Evaluate()
				if err != nil {
					return nil, meta, err
				}
				resolved[key] = val
				// Collect merge metadata
				collectDeferredMergeMeta(key, v, &meta)
				// Track once
				if v.once {
					meta.addOnce(key, OnceMeta{Prop: key})
				}
			}

		case AlwaysProp:
			// Always: always include regardless of partial status
			if !isPartial || !contains(exceptProps, key) {
				resolved[key] = v.Value()
			}

		case *OptionalProp:
			// Optional: only on explicit partial request
			if isPartial && contains(onlyProps, key) {
				// Skip if once and already seen
				trackKey := key
				if v.key != "" {
					trackKey = v.key
				}
				if v.once && contains(exceptOnce, trackKey) {
					continue
				}
				val, err := v.Evaluate()
				if err != nil {
					return nil, meta, err
				}
				resolved[key] = val
				if v.once {
					meta.addOnce(key, OnceMeta{Prop: trackKey})
				}
			}

		case *MergeProp:
			// Merge: always evaluate (it's a regular prop with merge behavior)
			trackKey := key
			if v.key != "" {
				trackKey = v.key
			}
			if v.once && contains(exceptOnce, trackKey) {
				continue
			}
			if !isPartial {
				val, err := v.Evaluate()
				if err != nil {
					return nil, meta, err
				}
				resolved[key] = val
				collectMergePropMeta(key, v, &meta)
				if v.once {
					meta.addOnce(key, OnceMeta{Prop: trackKey})
				}
			} else if shouldIncludeInPartial(key, onlyProps, exceptProps) {
				val, err := v.Evaluate()
				if err != nil {
					return nil, meta, err
				}
				resolved[key] = val
				collectMergePropMeta(key, v, &meta)
				if v.once {
					meta.addOnce(key, OnceMeta{Prop: trackKey})
				}
			}

		case *OnceProp:
			// Once: include on initial load, skip if client already has it
			trackKey := key
			if v.key != "" {
				trackKey = v.key
			}
			if contains(exceptOnce, trackKey) {
				continue
			}
			if !isPartial {
				val, err := v.Evaluate()
				if err != nil {
					return nil, meta, err
				}
				resolved[key] = val
				meta.addOnce(key, OnceMeta{Prop: trackKey})
			} else if shouldIncludeInPartial(key, onlyProps, exceptProps) {
				val, err := v.Evaluate()
				if err != nil {
					return nil, meta, err
				}
				resolved[key] = val
				meta.addOnce(key, OnceMeta{Prop: trackKey})
			}

		case *ScrollProp:
			// ScrollProp: merge behavior + optional defer
			if v.deferred {
				// Behaves like deferred, only evaluate on partial request
				if isPartial && contains(onlyProps, key) {
					val, err := v.Evaluate()
					if err != nil {
						return nil, meta, err
					}
					resolved[key] = val
					collectScrollMeta(key, v, scrollIntent, &meta)
				}
			} else {
				// Not deferred, include like a regular prop with merge
				if !isPartial {
					val, err := v.Evaluate()
					if err != nil {
						return nil, meta, err
					}
					resolved[key] = val
					collectScrollMeta(key, v, scrollIntent, &meta)
				} else if shouldIncludeInPartial(key, onlyProps, exceptProps) {
					val, err := v.Evaluate()
					if err != nil {
						return nil, meta, err
					}
					resolved[key] = val
					collectScrollMeta(key, v, scrollIntent, &meta)
				}
			}

		default:
			// Regular prop: include based on partial rules
			if !isPartial {
				resolved[key] = value
			} else {
				if shouldIncludeInPartial(key, onlyProps, exceptProps) {
					resolved[key] = value
				}
			}
		}
	}

	return resolved, meta, nil
}

// collectDeferredMergeMeta populates merge metadata from a DeferredProp.
func collectDeferredMergeMeta(key string, d *DeferredProp, meta *pageMeta) {
	if !d.merge {
		return
	}
	if d.prepend {
		meta.addPrepend(key)
	} else {
		meta.addMerge(key)
	}
	if d.deepMerge {
		meta.addDeepMerge(key)
	}
	meta.addMatchOn(key, d.matchOn)
}

// collectMergePropMeta populates merge metadata from a MergeProp.
func collectMergePropMeta(key string, m *MergeProp, meta *pageMeta) {
	if m.prepend {
		meta.addPrepend(key)
	} else {
		meta.addMerge(key)
	}
	if m.deepMerge {
		meta.addDeepMerge(key)
	}
	meta.addMatchOn(key, m.matchOn)
}

// collectScrollMeta populates merge and scroll metadata from a ScrollProp.
func collectScrollMeta(key string, s *ScrollProp, scrollIntent string, meta *pageMeta) {
	// Determine merge direction: header override > prop default > append
	shouldPrepend := s.prepend
	if scrollIntent == "prepend" {
		shouldPrepend = true
	} else if scrollIntent == "append" {
		shouldPrepend = false
	}

	if shouldPrepend {
		meta.addPrepend(key)
	} else {
		meta.addMerge(key)
	}

	// Collect scroll metadata
	if sm := s.Metadata(); sm != nil {
		meta.addScroll(key, *sm)
	}
}

// shouldIncludeInPartial determines if a prop should be included in partial reload
func shouldIncludeInPartial(key string, only, except []string) bool {
	if contains(except, key) {
		return false
	}
	if len(only) == 0 {
		return true
	}
	return contains(only, key)
}

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// splitHeader splits a comma-separated header value into a string slice.
func splitHeader(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

// appendUnique appends item to slice only if not already present.
func appendUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}

// removeKeys returns a new slice with the given keys removed.
func removeKeys(slice []string, keys []string) []string {
	if len(keys) == 0 {
		return slice
	}
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if !contains(keys, s) {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
