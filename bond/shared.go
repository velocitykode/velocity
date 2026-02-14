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
func (b *Bond) mergeSharedProps(r *http.Request, componentProps Props) Props {
	b.mu.RLock()
	sharePropsFunc := b.sharePropsFunc
	b.mu.RUnlock()

	merged := make(Props)

	// 1. Add static shared props (need lock)
	b.mu.RLock()
	for k, v := range b.sharedProps {
		merged[k] = v
	}

	// 2. Evaluate and add dynamic shared props
	for k, fn := range b.sharedFuncs {
		if val, err := fn(r); err == nil {
			merged[k] = val
		}
	}
	b.mu.RUnlock()

	// 3. Evaluate SharePropsFunc if set (outside lock to avoid deadlock)
	if sharePropsFunc != nil {
		if props, err := sharePropsFunc(r); err == nil && props != nil {
			for k, v := range props {
				merged[k] = v
			}
		}
	}

	// 4. Component props override shared props
	for k, v := range componentProps {
		merged[k] = v
	}

	return merged
}
