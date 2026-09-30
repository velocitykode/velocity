package queue

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/panicerr"
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
// dispatched (`q.Push(&SendMailJob{...})`). The factory's typed return is
// adapted to the registry's `func([]byte) (Job, error)` shape internally.
func RegisterJob[T Job](factory func([]byte) (T, error)) {
	// Derive the key from a zero T. For pointer types this is a typed nil,
	// which is sufficient for fmt's reflection to emit "*pkg.Foo".
	var zero T
	key := normalizeJobType(errchain.Sprintf("%T", zero))

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
		return nil, errchain.Errorf("velocity/queue: no handler registered for job type %s: %w", payload.Type, ErrJobNotFound)
	}

	job, err := rebuildContained(handler, payload.Data)
	if err != nil {
		return nil, err
	}
	// A factory is user code: one that returns no job and no error must
	// not reach a driver as a success, or a reserved pop keeps a
	// reservation for a job the worker takes for an empty queue, or runs a
	// nil job.
	if isNilJob(job) {
		return nil, fmt.Errorf("velocity/queue: the factory registered for job type %s returned no job and no error", payload.Type)
	}
	return job, nil
}

// rebuildContained runs a registered factory, user code, inside one
// contained boundary: a panic becomes a hydrationPanic. The Error method
// of the error a factory returns is user code too: a factory error comes
// back as a factoryError whose text was read here, contained (errchain.Text,
// errchain.Unreadable when Error panics), so no later Error call on it
// runs user code. Every
// driver's pop rebuilds jobs through the registry, so this is where a job
// that cannot be rebuilt becomes an error the driver quarantines as poison
// instead of a panic that unwinds the pop and loses the job.
func rebuildContained(handler func([]byte) (Job, error), data []byte) (job Job, err error) {
	defer func() {
		if r := recover(); r != nil {
			job, err = nil, hydrationPanic{cause: panicerr.New(r)}
		}
	}()
	job, err = handler(data)
	if err != nil {
		return nil, factoryError{text: errchain.Text(err), cause: err}
	}
	return job, nil
}

// errHydrationPanicked is the text recorded for a job whose rebuilding
// panicked. It is fixed: the panic value is never formatted into it.
const errHydrationPanicked = "velocity/queue: rebuilding the job panicked"

// hydrationPanic is the error of a job whose rebuilding panicked. Its text
// is fixed; the recovered value is reachable through errors.As as a
// *panicerr.Error, and is never formatted here.
type hydrationPanic struct{ cause *panicerr.Error }

func (e hydrationPanic) Error() string { return errHydrationPanicked }
func (e hydrationPanic) Unwrap() error { return e.cause }

// factoryError is the error a factory returned, with its text read inside
// rebuildContained. The factory's error stays reachable through Unwrap.
type factoryError struct {
	text  string
	cause error
}

func (e factoryError) Error() string { return e.text }
func (e factoryError) Unwrap() error { return e.cause }

// isNilJob reports whether job is nil, as an interface or as a nil value
// of a nillable type (a typed nil pointer).
func isNilJob(job Job) bool {
	if job == nil {
		return true
	}
	switch v := reflect.ValueOf(job); v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return v.IsNil()
	}
	return false
}
