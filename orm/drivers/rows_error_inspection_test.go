package drivers

import (
	"context"
	"io"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// eofIsPanics is a driver error whose Is panics when asked whether it is
// io.EOF: database/sql compares io.EOF by identity, so only the
// instrumentation's own check asks.
type eofIsPanics struct{}

func (eofIsPanics) Error() string { return "read failed" }
func (eofIsPanics) Is(target error) bool {
	if target == io.EOF {
		panic("Is broke")
	}
	return false
}

// A read that fails with an error whose Is panics, or whose chain loops
// back on itself, returns the error to the caller and records the
// statement as failed: the instrumentation's end-of-rows check is bounded
// and contained, as database/sql's identity comparison is.
func TestRowsReadFailure_HostileErrorIsRecorded(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"Is panics", eofIsPanics{}},
		{"chain loops", &selfLoop{}},
	} {
		for _, route := range []string{"Next", "NextResultSet"} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				s := &script{nextErr: tc.err}
				if route == "NextResultSet" {
					s = &script{nextResultSetErr: tc.err}
				}
				db, _, rec, _ := openScripted(t, s)
				p := hostile.Within(t, hostile.Deadline, func() {
					rows, err := db.QueryContext(context.Background(), "SELECT n FROM t")
					if err != nil {
						t.Errorf("QueryContext: %v", err)
						return
					}
					defer func() { _ = rows.Close() }()
					for rows.Next() {
					}
					if route == "NextResultSet" {
						rows.NextResultSet()
					}
					_ = rows.Close()
				})
				if p != nil {
					t.Fatalf("the read panicked: %v", p)
				}
				evs := rec.all()
				if len(evs) != 1 || evs[0].Err != tc.err {
					t.Fatalf("events = %+v, want one failure carrying the driver's error", evs)
				}
			})
		}
	}
}
