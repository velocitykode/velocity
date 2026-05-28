package queue

import (
	"fmt"
	"strings"
	"sync"
)

// Queue is an alias for Driver interface for backward compatibility
type Queue = Driver

// JobRegistry for deserializing jobs
type JobRegistry struct {
	mu       sync.RWMutex
	handlers map[string]func([]byte) (Job, error)
}

var registry = &JobRegistry{
	handlers: make(map[string]func([]byte) (Job, error)),
}

// normalizeJobType reduces a Go type identifier to the bare type name so that
// pointer (`*pkg.Foo`), package-qualified (`pkg.Foo`), and bare (`Foo`) forms
// all resolve to the same registry key. fmt.Sprintf("%T", v) emits the full
// form, while documentation and idiomatic Register calls use the bare name.
// Without normalization, lookups silently miss under non-memory drivers and
// jobs are dropped.
//
// Assumption: job types are named (declared with `type Foo struct{...}`) at
// package scope. Anonymous struct types and dotted type paths beyond
// `pkg.Type` are out of scope. Anonymous types are stringified by %T as
// `struct { ... }` and would round-trip unchanged but are unidiomatic for
// queueable jobs. Two named types whose unqualified names collide across
// packages would also collide in the registry; callers must keep job type
// names unique within a process.
func normalizeJobType(s string) string {
	s = strings.TrimPrefix(s, "*")
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// Register registers a job type for deserialization. The job type may be
// supplied as a bare name ("SendMailJob"), package-qualified ("auth.SendMailJob"),
// or pointer-qualified ("*auth.SendMailJob"). All are normalized to the bare
// name so push-side fmt.Sprintf("%T", &v) and consumer-side Register calls
// converge on the same key.
//
// Deprecated: prefer the generic [RegisterJob], which derives the registry key
// from the job type itself, eliminating typo footguns. Register accepts any
// string and silently succeeds at boot if the name does not match a real job
// type. The mismatch is only surfaced at runtime as ErrJobNotFound when a
// payload arrives. RegisterJob[T] keeps producer (push) and consumer (decode)
// keys symmetric by construction.
func Register(jobType string, handler func([]byte) (Job, error)) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.handlers[normalizeJobType(jobType)] = handler
}

// RegisterJob registers a typed factory for a job type. The registry key is
// derived from T via the same normalization the producer side uses on
// fmt.Sprintf("%T", &v), so producer and consumer keys are guaranteed to
// match by construction, with no string literal and no typo class.
//
// Use this in preference to [Register]. The string-keyed form is retained
// only for backward compatibility.
//
//	queue.RegisterJob(func(data []byte) (*SendMailJob, error) {
//	    j := &SendMailJob{}
//	    return j, json.Unmarshal(data, j)
//	})
//
// T is typically a pointer type (e.g. *SendMailJob), matching how jobs are
// dispatched (`q.PushCtx(ctx, &SendMailJob{...})`). The factory's typed
// return is adapted to the registry's `func([]byte) (Job, error)` shape
// internally.
func RegisterJob[T Job](factory func([]byte) (T, error)) {
	// Derive the key from a zero T. For pointer types this is a typed nil,
	// which is sufficient for fmt's reflection to emit "*pkg.Foo".
	var zero T
	key := normalizeJobType(fmt.Sprintf("%T", zero))

	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.handlers[key] = func(data []byte) (Job, error) {
		j, err := factory(data)
		if err != nil {
			return nil, err
		}
		return j, nil
	}
}

// Deserialize converts a payload back to a Job
func (r *JobRegistry) Deserialize(payload *Payload) (Job, error) {
	key := normalizeJobType(payload.Type)
	r.mu.RLock()
	handler, exists := r.handlers[key]
	r.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("velocity/queue: no handler registered for job type %s: %w", payload.Type, ErrJobNotFound)
	}

	return handler(payload.Data)
}
