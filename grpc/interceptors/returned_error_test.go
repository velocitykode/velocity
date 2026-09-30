package interceptors_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/internal/hostile"
)

// statusPanics is an error whose GRPCStatus panics.
type statusPanics struct{}

func (statusPanics) Error() string              { return "status panics" }
func (statusPanics) GRPCStatus() *status.Status { panic("GRPCStatus broke") }

// textPanics is an error without a status whose Error panics.
type textPanics struct{}

func (textPanics) Error() string { panic("Error broke") }

// wrapTextPanics wraps a status error, and its own Error panics: grpc-go
// reads the wrapper's text as the status message.
type wrapTextPanics struct{}

func (wrapTextPanics) Error() string { panic("Error broke") }
func (wrapTextPanics) Unwrap() error { return status.Error(codes.NotFound, "missing") }

// isPanics is an error without a status whose Is panics.
type isPanics struct{}

func (isPanics) Error() string   { return "is panics" }
func (isPanics) Is(error) bool   { panic("Is broke") }
func (isPanics) Unwrap() []error { return nil }

// unwrapLoop unwraps to itself.
type unwrapLoop struct{}

func (e *unwrapLoop) Error() string { return "loop" }
func (e *unwrapLoop) Unwrap() error { return e }

// reportedErrs records the errors reported.
type reportedErrs struct {
	mu   sync.Mutex
	errs []error
	ecs  []*contract.ErrorContext
}

func (r *reportedErrs) Report(err error, ec *contract.ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
	r.ecs = append(r.ecs, ec)
}

func (r *reportedErrs) all() ([]error, []*contract.ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...), append([]*contract.ErrorContext(nil), r.ecs...)
}

// grpcStatusType is the dynamic type of grpc-go's own status error.
var grpcStatusType = reflect.TypeOf(status.Error(codes.Unknown, ""))

// A handler's returned error whose methods panic, or whose chain loops
// back on itself, is classified once, contained: no panic escapes the
// call, the call ends with codes.Internal and its terminal events, it is
// reported once with the handler's own error and the reason, the request
// line is written at error level with the reason, and grpc-go gets an
// error of its own status type, so reading its status runs no user code.
func TestCallLifecycle_ReturnedErrorMethodsAreContained(t *testing.T) {
	for _, kind := range []string{"unary", "stream"} {
		for _, tc := range []struct {
			name, reason string
			err          error
		}{
			{"GRPCStatus panics", "classification_panicked", statusPanics{}},
			{"Error panics", "classification_panicked", textPanics{}},
			{"wrapped status, Error panics", "classification_panicked", wrapTextPanics{}},
			{"Is panics", "classification_panicked", isPanics{}},
			{"chain loops", "classification_budget_exhausted", &unwrapLoop{}},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				evs := &callEvents{}
				reports := &reportedErrs{}
				lines := hostile.NewLogger(nil)
				calls := interceptors.CallLifecycle(
					interceptors.WithRequestLine(),
					interceptors.WithLogger(lines),
					interceptors.WithEventDispatcher(evs.dispatch),
					interceptors.WithReporter(reports),
				)
				var got error
				p := hostile.Within(t, hostile.Deadline, func() {
					got = runChain(kind, calls, passThrough, func(context.Context) error { return tc.err })
				})
				if p != nil {
					t.Fatalf("a panic escaped the call: %v", p)
				}
				if reflect.TypeOf(got) != grpcStatusType {
					t.Fatalf("the call returned %T, want grpc-go's status error", got)
				}
				if c := status.Code(got); c != codes.Internal {
					t.Errorf("code = %v, want Internal", c)
				}
				kinds, cs, _ := evs.snapshot()
				if !equalKinds(kinds, []string{"started", "failed", "completed"}) || cs[1] != codes.Internal || cs[2] != codes.Internal {
					t.Errorf("events = %v %v, want started, failed and completed with Internal", kinds, cs)
				}
				errs, ecs := reports.all()
				if len(errs) != 1 || !sameError(errs[0], tc.err) {
					t.Fatalf("reports = %d, want one carrying the handler's error", len(errs))
				}
				if ecs[0].Extra["reason"] != tc.reason || ecs[0].Recovered {
					t.Errorf("report reason = %v, recovered = %v; want %q, not a panic", ecs[0].Extra["reason"], ecs[0].Recovered, tc.reason)
				}
				ls := lines.Lines()
				if len(ls) != 1 || ls[0].Level != hostile.Error || !hasPair(ls[0].KVs, "reason", tc.reason) || !hasPair(ls[0].KVs, "code", "Internal") {
					t.Errorf("request lines = %+v, want one error line with code Internal and reason %q", ls, tc.reason)
				}
			})
		}
	}
}

// hasPair reports whether kvs holds key with value.
func hasPair(kvs []any, key string, value any) bool {
	for i := 0; i+1 < len(kvs); i += 2 {
		if kvs[i] == key && kvs[i+1] == value {
			return true
		}
	}
	return false
}

// sameError compares by identity without calling either error's methods.
func sameError(a, b error) bool {
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		return false
	}
	if reflect.TypeOf(a).Comparable() {
		return a == b
	}
	return true
}

// nilStatus has a GRPCStatus that returns nil and wraps a context error.
type nilStatus struct{ inner error }

func (e nilStatus) Error() string              { return "nil status: " + e.inner.Error() }
func (e nilStatus) GRPCStatus() *status.Status { return nil }
func (e nilStatus) Unwrap() error              { return e.inner }

// okStatus carries a status whose code is OK.
type okStatus struct{}

func (okStatus) Error() string              { return "ok status" }
func (okStatus) GRPCStatus() *status.Status { return status.New(codes.OK, "fine") }

// asStatus gives a status only through its As method.
type asStatus struct{}

func (asStatus) Error() string { return "as status" }
func (asStatus) As(target any) bool {
	v := reflect.ValueOf(target).Elem()
	s := status.Error(codes.PermissionDenied, "denied")
	if reflect.TypeOf(s).AssignableTo(v.Type()) {
		v.Set(reflect.ValueOf(s))
		return true
	}
	return false
}

// grpcDerived is the status grpc-go derives from a handler's error:
// status.FromError, then status.FromContextError when it carries none.
func grpcDerived(err error) *status.Status {
	if st, ok := status.FromError(err); ok {
		return st
	}
	return status.FromContextError(err)
}

// For every error whose methods behave, the client gets the status grpc-go
// derives from the handler's own error, and the events carry its code.
func TestCallLifecycle_ReturnedErrorKeepsItsStatus(t *testing.T) {
	notFound := status.Error(codes.NotFound, "missing")
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"status error", notFound},
		{"wrapped status error", fmt.Errorf("lookup: %w", notFound)},
		{"status beside another error", errors.Join(errors.New("first"), notFound)},
		{"plain error", errors.New("boom")},
		{"wrapped deadline", fmt.Errorf("query: %w", context.DeadlineExceeded)},
		{"wrapped canceled", fmt.Errorf("query: %w", context.Canceled)},
		{"deadline beats canceled", errors.Join(context.Canceled, context.DeadlineExceeded)},
		{"nil status wrapping canceled", nilStatus{inner: context.Canceled}},
		{"status with code OK", okStatus{}},
		{"status through As", asStatus{}},
	} {
		for _, kind := range []string{"unary", "stream"} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				evs := &callEvents{}
				calls := interceptors.CallLifecycle(interceptors.WithEventDispatcher(evs.dispatch))
				got := runChain(kind, calls, passThrough, func(context.Context) error { return tc.err })
				want := grpcDerived(tc.err)
				gotSt, ok := status.FromError(got)
				if !ok {
					t.Fatalf("the call returned %v, which carries no status", got)
				}
				if gotSt.Code() != want.Code() || gotSt.Message() != want.Message() {
					t.Errorf("status = %v %q, want %v %q", gotSt.Code(), gotSt.Message(), want.Code(), want.Message())
				}
				kinds, cs, _ := evs.snapshot()
				if n := len(cs); n == 0 || cs[n-1] != want.Code() || kinds[n-1] != "completed" {
					t.Errorf("events = %v %v, want completed with %v", kinds, cs, want.Code())
				}
			})
		}
	}
}
