package contract_test

import (
	"net/http"
	"testing"

	. "github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// hostileMethods is an error whose Is, As and Unwrap panic, and that also
// claims a status and headers, so partial facts would show.
type hostileMethods struct{ method string }

func (e hostileMethods) Error() string { return "hostile" }
func (e hostileMethods) Is(error) bool {
	if e.method == "Is" {
		panic("Is broke")
	}
	return false
}
func (e hostileMethods) As(any) bool {
	if e.method == "As" {
		panic("As broke")
	}
	return false
}
func (e hostileMethods) Unwrap() error {
	if e.method == "Unwrap" {
		panic("Unwrap broke")
	}
	return nil
}
func (e hostileMethods) StatusCode() int {
	if e.method == "StatusCode" {
		panic("StatusCode broke")
	}
	return http.StatusTeapot
}
func (e hostileMethods) Headers() http.Header {
	if e.method == "Headers" {
		panic("Headers broke")
	}
	return http.Header{"X-Partial": {"yes"}}
}

// selfLoop unwraps to itself.
type chainLoop struct{}

func (e *chainLoop) Error() string { return "loop" }
func (e *chainLoop) Unwrap() error { return e }

// The status and marker predicates read a hostile error contained and
// bounded, and never panic or hang. A chain that loops, or a status or
// headers method that panics, gives StatusOf's fixed answer (500, no
// headers, not ok); a panicking Is, As or Unwrap StatusOf never needs
// (the error answers both facts itself) leaves its answer alone. No marker
// is found in any of them.
func TestPredicates_HostileErrorGetsTheFixedAnswer(t *testing.T) {
	errs := map[string]error{"loop": &chainLoop{}}
	for _, m := range []string{"Is", "As", "Unwrap", "StatusCode", "Headers"} {
		errs[m+" panics"] = hostileMethods{method: m}
	}
	for name, err := range errs {
		t.Run(name, func(t *testing.T) {
			if p := hostile.Within(t, hostile.Deadline, func() {
				status, headers, ok := StatusOf(err)
				fixed := name == "loop" || name == "StatusCode panics" || name == "Headers panics"
				if fixed && (status != http.StatusInternalServerError || headers != nil || ok) {
					t.Errorf("StatusOf = %d, %v, %v; want 500, nil, false", status, headers, ok)
				}
				if IsReported(err) || IsResponseWritten(err) || HandledCause(err) != nil {
					t.Error("a marker was found in a chain that cannot be read")
				}
				_ = MarkReported(err)
			}); p != nil {
				t.Fatalf("a predicate panicked: %v", p)
			}
		})
	}
}
