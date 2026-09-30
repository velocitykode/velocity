package sqlerr

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/errchain"
)

type codedError struct{ text string }

func (e *codedError) Error() string { return e.text }

func TestKind(t *testing.T) {
	coded := &codedError{text: "Duplicate entry 'alice@example.com' for key 'email'"}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"unlisted type", coded, Other},
		{"wrapped unlisted type", fmt.Errorf("insert: %w", fmt.Errorf("exec: %w", coded)), Other},
		{"empty join", emptyJoin{}, Other},
		{"plain", errors.New("secret"), Other},
		{"canceled", fmt.Errorf("q: %w", context.Canceled), "context.Canceled"},
		{"deadline", context.DeadlineExceeded, "context.DeadlineExceeded"},
		{"tx done", sql.ErrTxDone, "sql.ErrTxDone"},
		{"conn done", sql.ErrConnDone, "sql.ErrConnDone"},
		{"no rows", sql.ErrNoRows, "sql.ErrNoRows"},
		{"bad conn", driver.ErrBadConn, "driver.ErrBadConn"},
		{"sentinel in a later join branch", errors.Join(coded, sql.ErrNoRows), "sql.ErrNoRows"},
		{"sentinel priority follows the list", errors.Join(sql.ErrNoRows, context.Canceled), "context.Canceled"},
		{"sentinel through an Is method", isCanceled{}, "context.Canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Kind(tc.err); got != tc.want {
				t.Errorf("Kind = %q, want %q", got, tc.want)
			}
		})
	}
}

type emptyJoin struct{}

func (emptyJoin) Error() string   { return "empty" }
func (emptyJoin) Unwrap() []error { return nil }

// isCanceled matches context.Canceled through its own Is method.
type isCanceled struct{}

func (isCanceled) Error() string        { return "canceled by the driver" }
func (isCanceled) Is(target error) bool { return target == context.Canceled }

// selfLoop unwraps to itself: a chain with a cycle.
type selfLoop struct{}

func (e *selfLoop) Error() string { return "loop" }
func (e *selfLoop) Unwrap() error { return e }

// joinLoop joins itself: a multi-branch chain with a cycle.
type joinLoop struct{}

func (e *joinLoop) Error() string   { return "join loop" }
func (e *joinLoop) Unwrap() []error { return []error{errors.New("x"), e} }

// panicUnwrap panics when its chain is walked.
type panicUnwrap struct{}

func (panicUnwrap) Error() string { return "panics" }
func (panicUnwrap) Unwrap() error { panic("unwrap broke") }

// Kind finishes on a chain that loops back on itself, in one branch or in
// a join, and answers a kind from the fixed set. A classifier that walks
// the chain without a bound never returns, and the failure line of the
// statement it describes never gets written.
func TestKind_CyclicChainsFinish(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"self unwrap", &selfLoop{}, Other},
		{"wrapped self unwrap", fmt.Errorf("callback: %w", &selfLoop{}), Other},
		{"join loop", &joinLoop{}, Other},
		{"loop beside a sentinel branch", errors.Join(&selfLoop{}, sql.ErrTxDone), "sql.ErrTxDone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan string, 1)
			go func() { done <- Kind(tc.err) }()
			select {
			case got := <-done:
				if got != tc.want {
					t.Errorf("Kind = %q, want %q", got, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Kind did not return: it walks a cyclic chain without a bound")
			}
		})
	}
}

// Kind never repeats anything an error's type carries: an unnamed type
// built at run time can carry a value in a struct tag, and its type string
// would put that value in the log line.
func TestKind_DynamicTypeCarriesNoValue(t *testing.T) {
	typ := reflect.StructOf([]reflect.StructField{{
		Name:      "Inner",
		Type:      reflect.TypeOf(&codedError{}),
		Tag:       `rejected:"hunter2@example.com"`,
		Anonymous: true,
	}})
	v := reflect.New(typ).Elem()
	v.Field(0).Set(reflect.ValueOf(&codedError{text: "x"}))
	err, ok := v.Interface().(error)
	if !ok {
		t.Fatal("dynamic struct does not implement error")
	}
	got := Kind(err)
	if strings.Contains(got, "hunter2") {
		t.Fatalf("Kind = %q, carries the value in the type's tag", got)
	}
	if got != Other {
		t.Errorf("Kind = %q, want %q", got, Other)
	}
}

// A panicking Unwrap does not escape the classifier: Kind runs on failure
// lines inside rollback cleanup, which must still finish.
func TestKind_PanickingChainIsOther(t *testing.T) {
	if got := Kind(panicUnwrap{}); got != Other {
		t.Errorf("Kind = %q, want %q", got, Other)
	}
}

// Every answer comes from the fixed set: a sentinel name, a listed driver
// error kind, Other, or "" for nil.
func TestKind_AnswersFromTheFixedSet(t *testing.T) {
	allowed := map[string]bool{"": true, Other: true}
	for _, s := range sentinels {
		allowed[s.name] = true
	}
	for _, d := range driverErrors {
		allowed[d.kind] = true
	}
	for _, err := range []error{nil, &codedError{}, &selfLoop{}, &joinLoop{}, panicUnwrap{}, emptyJoin{}, errors.New("a"), sql.ErrNoRows} {
		if got := Kind(err); !allowed[got] {
			t.Errorf("Kind(%T) = %q, not in the fixed set", err, got)
		}
	}
}

// wideJoin joins more errors than Kind looks at, the sentinel last.
type wideJoin struct{}

func (wideJoin) Error() string { return "wide" }
func (wideJoin) Unwrap() []error {
	errs := make([]error, 0, 2*errchain.Max)
	for i := 0; i < 2*errchain.Max; i++ {
		errs = append(errs, &selfLoop{})
	}
	return append(errs, sql.ErrNoRows)
}

// A join wider than the bound is cut at the bound: Kind answers from what
// it looked at and never panics slicing it.
func TestKind_WideJoinIsBounded(t *testing.T) {
	if got := Kind(wideJoin{}); got != Other {
		t.Errorf("Kind = %q, want %q", got, Other)
	}
	if got := Kind(fmt.Errorf("a: %w", fmt.Errorf("b: %w", wideJoin{}))); got != Other {
		t.Errorf("wrapped Kind = %q, want %q", got, Other)
	}
}
