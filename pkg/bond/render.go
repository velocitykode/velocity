package bond

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
)

// Render renders a component with props
func (b *Bond) Render(w http.ResponseWriter, r *http.Request, component string, props Props) error {
	// 1. Merge shared props with component props
	mergedProps := b.mergeSharedProps(r, props)

	// 2. Check if this is a partial reload
	isPartial := b.isPartialReload(r, component)

	// 3. Extract deferred prop groups (before resolution)
	deferredGroups := b.extractDeferredGroups(mergedProps)

	// 4. Resolve props based on request type
	resolvedProps, err := b.resolveProps(r, mergedProps, isPartial)
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
	}

	// 6. Route to appropriate renderer
	if isInertiaRequest(r) {
		return b.renderJSON(w, page)
	}
	return b.renderHTML(w, page)
}

// renderHTML renders a full HTML page with embedded Inertia data
func (b *Bond) renderHTML(w http.ResponseWriter, page Page) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	pageJSON, err := page.ToHTMLAttr()
	if err != nil {
		return err
	}

	data := map[string]any{
		"inertia":     template.HTML(b.buildInertiaDiv(pageJSON)),
		"inertiaHead": template.HTML(""), // Empty for CSR, populated for SSR
	}

	return b.template.Execute(w, data)
}

// renderJSON renders a JSON response for Inertia XHR requests
func (b *Bond) renderJSON(w http.ResponseWriter, page Page) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Inertia", "true")
	w.Header().Set("Vary", "X-Inertia")

	return json.NewEncoder(w).Encode(page)
}

// buildInertiaDiv constructs the Inertia container div
func (b *Bond) buildInertiaDiv(pageJSON string) string {
	return `<div id="` + b.containerID + `" data-page='` + pageJSON + `'></div>`
}

// isPartialReload checks if this is a partial reload for the given component
func (b *Bond) isPartialReload(r *http.Request, component string) bool {
	if r.Header.Get("X-Inertia-Partial-Component") != component {
		return false
	}
	return r.Header.Get("X-Inertia-Partial-Data") != "" ||
		r.Header.Get("X-Inertia-Partial-Except") != ""
}

// getPartialOnly returns the list of props to include in partial reload
func getPartialOnly(r *http.Request) []string {
	data := r.Header.Get("X-Inertia-Partial-Data")
	if data == "" {
		return nil
	}
	return strings.Split(data, ",")
}

// getPartialExcept returns the list of props to exclude in partial reload
func getPartialExcept(r *http.Request) []string {
	data := r.Header.Get("X-Inertia-Partial-Except")
	if data == "" {
		return nil
	}
	return strings.Split(data, ",")
}

// extractDeferredGroups builds the deferredProps map for the Page
func (b *Bond) extractDeferredGroups(props Props) map[string][]string {
	groups := make(map[string][]string)

	for key, value := range props {
		if dp, ok := value.(DeferredProp); ok {
			groups[dp.group] = append(groups[dp.group], key)
		}
	}

	if len(groups) == 0 {
		return nil
	}
	return groups
}

// resolveProps processes all props based on request type
func (b *Bond) resolveProps(r *http.Request, props Props, isPartial bool) (Props, error) {
	resolved := make(Props)

	onlyProps := getPartialOnly(r)
	exceptProps := getPartialExcept(r)

	for key, value := range props {
		switch v := value.(type) {
		case LazyProp:
			// Lazy: only include if partial reload explicitly requests it
			if isPartial && contains(onlyProps, key) {
				val, err := v.Evaluate()
				if err != nil {
					return nil, err
				}
				resolved[key] = val
			}
			// Skip on initial load (not included unless requested)

		case DeferredProp:
			// Deferred: only evaluate on explicit partial reload request
			if isPartial && contains(onlyProps, key) {
				val, err := v.Evaluate()
				if err != nil {
					return nil, err
				}
				resolved[key] = val
			}
			// Otherwise skip - client will fetch later via deferred reload

		case AlwaysProp:
			// Always: always include regardless of partial status
			if !isPartial || !contains(exceptProps, key) {
				resolved[key] = v.Value()
			}

		case OptionalProp:
			// Optional: same as Lazy - only on explicit partial request
			if isPartial && contains(onlyProps, key) {
				val, err := v.Evaluate()
				if err != nil {
					return nil, err
				}
				resolved[key] = val
			}

		default:
			// Regular prop: include based on partial rules
			if !isPartial {
				// Initial load: include all regular props
				resolved[key] = value
			} else {
				// Partial reload: filter based on only/except
				if shouldIncludeInPartial(key, onlyProps, exceptProps) {
					resolved[key] = value
				}
			}
		}
	}

	return resolved, nil
}

// shouldIncludeInPartial determines if a prop should be included in partial reload
func shouldIncludeInPartial(key string, only, except []string) bool {
	// If except list contains this key, exclude it
	if contains(except, key) {
		return false
	}

	// If only list is empty, include all (that aren't excepted)
	if len(only) == 0 {
		return true
	}

	// If only list is specified, only include if in list
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
