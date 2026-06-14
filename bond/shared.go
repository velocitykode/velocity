package bond

import (
	"net/http"
)

// Share adds a static prop to all responses
func (b *Bond) Share(key string, value any) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.sharedProps == nil {
		b.sharedProps = make(Props)
	}
	b.sharedProps[key] = value
}

// ShareFunc adds a dynamic prop evaluated per-request
func (b *Bond) ShareFunc(key string, fn SharedPropFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.sharedFuncs == nil {
		b.sharedFuncs = make(map[string]SharedPropFunc)
	}
	b.sharedFuncs[key] = fn
}

// ShareMultiple adds multiple static props at once
func (b *Bond) ShareMultiple(props Props) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.sharedProps == nil {
		b.sharedProps = make(Props)
	}
	for k, v := range props {
		b.sharedProps[k] = v
	}
}

// ClearShared removes all shared props (useful for testing)
func (b *Bond) ClearShared() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sharedProps = make(Props)
	b.sharedFuncs = make(map[string]SharedPropFunc)
}

// mergeSharedProps combines shared props with component props
// Order: static shared -> dynamic shared -> sharePropsFunc -> component (component wins)
//
// Snapshot pattern: copy sharedProps/sharedFuncs into local variables under a
// brief RLock, release the lock, then invoke user-supplied SharedPropFunc
// callbacks OUTSIDE any lock. A panic inside a user callback would otherwise
// leak the RLock permanently (sync.RWMutex is not goroutine-attached and does
// not unwind on panic), wedging every future writer and every reader queued
// behind it. Mirrors the safeInvokeForUntil pattern in events/dispatcher.go
// and flashFor in bond/flash_v2.go.
func (b *Bond) mergeSharedProps(r *http.Request, componentProps Props) Props {
	b.mu.RLock()
	sharePropsFunc := b.sharePropsFunc

	// Fast path: no shared static props, no shared funcs, and no
	// SharePropsFunc means the merge contributes nothing, so the result is
	// just the component props. Copy them into a fresh map rather than
	// returning componentProps directly: Render mutates the returned map in
	// place (applyFlashData writes "errors"/"old"), and the caller's props
	// map must not be written through. Skips the staticProps/dynamicFuncs
	// snapshot maps and their copy loops entirely.
	if len(b.sharedProps) == 0 && len(b.sharedFuncs) == 0 && sharePropsFunc == nil {
		b.mu.RUnlock()
		merged := make(Props, len(componentProps))
		for k, v := range componentProps {
			merged[k] = v
		}
		return merged
	}

	// Snapshot static shared props.
	staticProps := make(Props, len(b.sharedProps))
	for k, v := range b.sharedProps {
		staticProps[k] = v
	}
	// Snapshot dynamic shared func references. Closures themselves are not
	// copied (they're function values) but the map entries are, so a
	// concurrent ShareFunc/ClearShared will not race with our iteration.
	dynamicFuncs := make(map[string]SharedPropFunc, len(b.sharedFuncs))
	for k, fn := range b.sharedFuncs {
		dynamicFuncs[k] = fn
	}
	b.mu.RUnlock()

	merged := make(Props, len(staticProps)+len(dynamicFuncs)+len(componentProps))

	// 1. Add static shared props.
	for k, v := range staticProps {
		merged[k] = v
	}

	// 2. Evaluate and add dynamic shared props OUTSIDE the lock. A panic in
	// fn here can be recovered by an upstream defer without leaving the
	// Bond's RWMutex in a wedged state.
	for k, fn := range dynamicFuncs {
		if val, err := fn(r); err == nil {
			merged[k] = val
		}
	}

	// 3. Evaluate SharePropsFunc if set (already outside lock).
	if sharePropsFunc != nil {
		if props, err := sharePropsFunc(r); err == nil && props != nil {
			for k, v := range props {
				merged[k] = v
			}
		}
	}

	// 4. Component props override shared props.
	for k, v := range componentProps {
		merged[k] = v
	}

	return merged
}
