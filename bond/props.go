package bond

import "net/http"

// Props holds component properties
type Props map[string]any

// SharedPropFunc is evaluated per-request for dynamic shared props
type SharedPropFunc func(r *http.Request) (any, error)

// Deprecated: LazyProp is evaluated only when explicitly requested via partial reload.
// Use OptionalProp instead, which provides the same behavior with additional capabilities.
type LazyProp struct {
	fn func() (any, error)
}

// Deprecated: Lazy creates a lazy prop that is only evaluated when explicitly requested.
// Use Optional instead.
func Lazy(fn func() (any, error)) LazyProp {
	return LazyProp{fn: fn}
}

// Evaluate resolves the lazy prop value
func (l LazyProp) Evaluate() (any, error) {
	return l.fn()
}

// DeferredProp is loaded after the initial page render by the client.
// Supports optional merge and once behaviors via builder methods.
type DeferredProp struct {
	fn    func() (any, error)
	group string
	// merge behavior
	merge        bool
	deepMerge    bool
	prepend      bool
	appendPaths  []string
	prependPaths []string
	matchOn      []string
	// once behavior
	once bool
}

// Defer creates a deferred prop with an optional group name.
// Deferred props are not included in the initial response but are
// fetched by the client after the page renders.
func Defer(fn func() (any, error), group ...string) *DeferredProp {
	g := "default"
	if len(group) > 0 && group[0] != "" {
		g = group[0]
	}
	return &DeferredProp{fn: fn, group: g}
}

// Evaluate resolves the deferred prop value
func (d *DeferredProp) Evaluate() (any, error) {
	return d.fn()
}

// Group returns the deferred prop's group name
func (d *DeferredProp) Group() string {
	return d.group
}

// Merge marks this deferred prop for append-merge on the client.
func (d *DeferredProp) Merge() *DeferredProp {
	d.merge = true
	return d
}

// DeepMerge marks this deferred prop for deep-merge on the client.
func (d *DeferredProp) DeepMerge() *DeferredProp {
	d.merge = true
	d.deepMerge = true
	return d
}

// Append specifies paths within the prop value that should be appended.
// If no paths are provided, the root value is appended.
func (d *DeferredProp) Append(paths ...string) *DeferredProp {
	d.merge = true
	d.appendPaths = append(d.appendPaths, paths...)
	return d
}

// Prepend specifies paths within the prop value that should be prepended.
// If no paths are provided, the root value is prepended.
func (d *DeferredProp) Prepend(paths ...string) *DeferredProp {
	d.merge = true
	d.prepend = true
	d.prependPaths = append(d.prependPaths, paths...)
	return d
}

// MatchOn specifies keys used for deduplication during merge.
func (d *DeferredProp) MatchOn(keys ...string) *DeferredProp {
	d.matchOn = append(d.matchOn, keys...)
	return d
}

// Once marks this deferred prop to be resolved only once.
// Subsequent requests will skip it unless the client resets.
func (d *DeferredProp) Once() *DeferredProp {
	d.once = true
	return d
}

// ShouldMerge returns whether this prop has merge behavior.
func (d *DeferredProp) ShouldMerge() bool { return d.merge }

// ShouldDeepMerge returns whether this prop uses deep-merge.
func (d *DeferredProp) ShouldDeepMerge() bool { return d.deepMerge }

// ShouldPrepend returns whether this prop prepends at root.
func (d *DeferredProp) ShouldPrepend() bool { return d.prepend }

// AppendPaths returns the paths that should be appended.
func (d *DeferredProp) AppendPaths() []string { return d.appendPaths }

// PrependPaths returns the paths that should be prepended.
func (d *DeferredProp) PrependPaths() []string { return d.prependPaths }

// MatchesOn returns the deduplication keys.
func (d *DeferredProp) MatchesOn() []string { return d.matchOn }

// IsOnce returns whether this prop should only be resolved once.
func (d *DeferredProp) IsOnce() bool { return d.once }

// AlwaysProp is always included in responses, even during partial reloads
type AlwaysProp struct {
	value any
}

// Always creates a prop that is always included, even in partial reloads
func Always(value any) AlwaysProp {
	return AlwaysProp{value: value}
}

// Value returns the always prop's value
func (a AlwaysProp) Value() any {
	return a.value
}

// OptionalProp is excluded from the first visit unless explicitly requested.
// Supports once behavior via builder methods.
type OptionalProp struct {
	fn   func() (any, error)
	once bool
	key  string
}

// Optional creates an optional prop
func Optional(fn func() (any, error)) *OptionalProp {
	return &OptionalProp{fn: fn}
}

// Evaluate resolves the optional prop value
func (o *OptionalProp) Evaluate() (any, error) {
	return o.fn()
}

// Once marks this optional prop to be resolved only once per session.
func (o *OptionalProp) Once() *OptionalProp {
	o.once = true
	return o
}

// As sets a custom key for once-prop tracking.
func (o *OptionalProp) As(key string) *OptionalProp {
	o.key = key
	return o
}

// IsOnce returns whether this prop should only be resolved once.
func (o *OptionalProp) IsOnce() bool { return o.once }

// OnceKey returns the custom tracking key, or empty string for default.
func (o *OptionalProp) OnceKey() string { return o.key }

// MergeProp tells the client to merge this prop's value with existing data
// instead of replacing it. Supports append, prepend, deep-merge, and deduplication.
type MergeProp struct {
	value        any
	fn           func() (any, error)
	merge        bool
	deepMerge    bool
	prepend      bool
	appendPaths  []string
	prependPaths []string
	matchOn      []string
	// once behavior
	once bool
	key  string
}

// Merge creates a merge prop from a static value.
func Merge(value any) *MergeProp {
	return &MergeProp{value: value, merge: true}
}

// MergeFunc creates a merge prop from a lazy-evaluated function.
func MergeFunc(fn func() (any, error)) *MergeProp {
	return &MergeProp{fn: fn, merge: true}
}

// Evaluate resolves the merge prop value.
func (m *MergeProp) Evaluate() (any, error) {
	if m.fn != nil {
		return m.fn()
	}
	return m.value, nil
}

// DeepMerge enables deep-merge behavior.
func (m *MergeProp) DeepMerge() *MergeProp {
	m.deepMerge = true
	return m
}

// Append specifies paths within the prop value that should be appended.
func (m *MergeProp) Append(paths ...string) *MergeProp {
	m.appendPaths = append(m.appendPaths, paths...)
	return m
}

// Prepend specifies paths within the prop value that should be prepended.
func (m *MergeProp) Prepend(paths ...string) *MergeProp {
	m.prepend = true
	m.prependPaths = append(m.prependPaths, paths...)
	return m
}

// MatchOn specifies keys used for deduplication during merge.
func (m *MergeProp) MatchOn(keys ...string) *MergeProp {
	m.matchOn = append(m.matchOn, keys...)
	return m
}

// Once marks this merge prop to be resolved only once.
func (m *MergeProp) Once() *MergeProp {
	m.once = true
	return m
}

// As sets a custom key for once-prop tracking.
func (m *MergeProp) As(key string) *MergeProp {
	m.key = key
	return m
}

// ShouldMerge returns whether this prop has merge behavior.
func (m *MergeProp) ShouldMerge() bool { return m.merge }

// ShouldDeepMerge returns whether this prop uses deep-merge.
func (m *MergeProp) ShouldDeepMerge() bool { return m.deepMerge }

// ShouldPrepend returns whether this prop prepends at root.
func (m *MergeProp) ShouldPrepend() bool { return m.prepend }

// AppendPaths returns the paths that should be appended.
func (m *MergeProp) AppendPaths() []string { return m.appendPaths }

// PrependPaths returns the paths that should be prepended.
func (m *MergeProp) PrependPaths() []string { return m.prependPaths }

// MatchesOn returns the deduplication keys.
func (m *MergeProp) MatchesOn() []string { return m.matchOn }

// IsOnce returns whether this prop should only be resolved once.
func (m *MergeProp) IsOnce() bool { return m.once }

// OnceKey returns the custom tracking key, or empty string for default.
func (m *MergeProp) OnceKey() string { return m.key }

// OnceProp is resolved on the first request and then remembered.
// Subsequent requests skip it unless the client explicitly resets.
type OnceProp struct {
	fn  func() (any, error)
	key string
}

// Once creates a once prop that is evaluated only on first load.
func Once(fn func() (any, error)) *OnceProp {
	return &OnceProp{fn: fn}
}

// Evaluate resolves the once prop value.
func (o *OnceProp) Evaluate() (any, error) {
	return o.fn()
}

// As sets a custom key for tracking this once prop.
func (o *OnceProp) As(key string) *OnceProp {
	o.key = key
	return o
}

// OnceKey returns the custom tracking key, or empty string for default.
func (o *OnceProp) OnceKey() string { return o.key }

// ScrollMeta holds pagination metadata for infinite scroll props.
type ScrollMeta struct {
	PageName     string `json:"pageName"`
	PreviousPage any    `json:"previousPage,omitempty"`
	NextPage     any    `json:"nextPage,omitempty"`
	CurrentPage  any    `json:"currentPage"`
}

// ScrollProp supports infinite scroll with automatic merge behavior.
// Data is merged (appended or prepended) on the client and can be deferred.
type ScrollProp struct {
	value    any
	fn       func() (any, error)
	wrapper  string
	metadata func() ScrollMeta
	// merge behavior (always enabled for scroll)
	prepend      bool
	appendPaths  []string
	prependPaths []string
	// defer behavior
	deferred bool
	group    string
}

// Scroll creates a scroll prop. The wrapper parameter names the key that
// wraps the scrollable data (e.g. "data" for paginated responses).
func Scroll(value any, wrapper string) *ScrollProp {
	return &ScrollProp{value: value, wrapper: wrapper}
}

// ScrollFunc creates a scroll prop from a lazy-evaluated function.
func ScrollFunc(fn func() (any, error), wrapper string) *ScrollProp {
	return &ScrollProp{fn: fn, wrapper: wrapper}
}

// Evaluate resolves the scroll prop value.
func (s *ScrollProp) Evaluate() (any, error) {
	if s.fn != nil {
		return s.fn()
	}
	return s.value, nil
}

// WithMetadata attaches pagination metadata to the scroll prop.
func (s *ScrollProp) WithMetadata(fn func() ScrollMeta) *ScrollProp {
	s.metadata = fn
	return s
}

// Defer marks this scroll prop to be loaded after the initial render.
func (s *ScrollProp) Defer(group ...string) *ScrollProp {
	s.deferred = true
	s.group = "default"
	if len(group) > 0 && group[0] != "" {
		s.group = group[0]
	}
	return s
}

// Prepend configures the scroll prop to prepend new data instead of appending.
func (s *ScrollProp) PrependData(paths ...string) *ScrollProp {
	s.prepend = true
	s.prependPaths = append(s.prependPaths, paths...)
	return s
}

// AppendData specifies paths within the scroll data that should be appended.
func (s *ScrollProp) AppendData(paths ...string) *ScrollProp {
	s.appendPaths = append(s.appendPaths, paths...)
	return s
}

// Wrapper returns the wrapper key name.
func (s *ScrollProp) Wrapper() string { return s.wrapper }

// IsDeferred returns whether this scroll prop should be deferred.
func (s *ScrollProp) IsDeferred() bool { return s.deferred }

// Group returns the deferred group name, or empty if not deferred.
func (s *ScrollProp) Group() string { return s.group }

// ShouldPrepend returns whether data should be prepended.
func (s *ScrollProp) ShouldPrepend() bool { return s.prepend }

// Metadata returns the scroll metadata, if a metadata function is set.
func (s *ScrollProp) Metadata() *ScrollMeta {
	if s.metadata == nil {
		return nil
	}
	meta := s.metadata()
	return &meta
}
