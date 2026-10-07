package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// Timeout hands its buffered header values to the real writer as they
// are: no second copy of them is made on the way.
func TestTimeoutWriter_FlushDoesNotCopyHeaderValues(t *testing.T) {
	rec := httptest.NewRecorder()
	tw := newTimeoutWriter(rec)
	tw.Header().Set("X-A", "1")
	buffered := tw.Header()["X-A"]
	tw.WriteHeader(http.StatusOK)
	if !tw.flushBuffered() {
		t.Fatal("flushBuffered refused")
	}
	got := rec.Header()["X-A"]
	if len(got) != 1 || &got[0] != &buffered[0] {
		t.Fatal("the flushed header values are a copy of the buffered ones")
	}
}

// headerPanicsOnce is a writer whose Header panics the first time.
type headerPanicsOnce struct {
	*httptest.ResponseRecorder
	panicked bool
}

func (w *headerPanicsOnce) Header() http.Header {
	if !w.panicked {
		w.panicked = true
		panic("Header exploded")
	}
	return w.ResponseRecorder.Header()
}

// A destination writer whose Header panics during Timeout's flush does not
// strand the buffered writer's lock: a later write to the buffered writer
// returns.
func TestTimeoutWriter_FlushReleasesItsLockWhenTheDestinationPanics(t *testing.T) {
	tw := newTimeoutWriter(&headerPanicsOnce{ResponseRecorder: httptest.NewRecorder()})
	tw.Header().Set("X-A", "1")
	tw.WriteHeader(http.StatusOK)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the destination's panic did not come out of flushBuffered")
			}
		}()
		tw.flushBuffered()
	}()
	if p := hostile.Within(t, hostile.Deadline, func() { _, _ = tw.Write([]byte("late")) }); p != nil {
		t.Fatalf("panicked: %v", p)
	}
}
