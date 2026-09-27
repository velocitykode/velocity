package scheduler

import (
	"context"
	"regexp"
	"sync"
	"testing"
)

var (
	w3cTraceID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	w3cSpanID  = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// TestRun_IsRootSpan pins that every scheduled run is a root span: a fresh
// trace, a fresh span, no parent, and the same span on its starting and
// finished events.
func TestRun_IsRootSpan(t *testing.T) {
	s := New()
	var mu sync.Mutex
	var starting []*ScheduledTaskStarting
	var finished []*ScheduledTaskFinished
	s.SetEventDispatcher(func(_ context.Context, ev interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case *ScheduledTaskStarting:
			starting = append(starting, e)
		case *ScheduledTaskFinished:
			finished = append(finished, e)
		}
		return nil
	})

	job := s.Call(func() {})
	for i := 0; i < 2; i++ {
		if err := job.Run(); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(starting) != 2 || len(finished) != 2 {
		t.Fatalf("got %d starting and %d finished events, want 2 each", len(starting), len(finished))
	}
	for i := range starting {
		st, fin := starting[i], finished[i]
		if !w3cTraceID.MatchString(st.TraceID) || !w3cSpanID.MatchString(st.SpanID) || st.ParentID != "" {
			t.Errorf("run %d: trace=%q span=%q parent=%q, want a root span", i, st.TraceID, st.SpanID, st.ParentID)
		}
		if fin.TraceID != st.TraceID || fin.SpanID != st.SpanID {
			t.Errorf("run %d: finished event in trace %q span %q, starting in %q %q", i, fin.TraceID, fin.SpanID, st.TraceID, st.SpanID)
		}
	}
	if starting[0].TraceID == starting[1].TraceID {
		t.Errorf("two runs share trace %q, want a new root per run", starting[0].TraceID)
	}
}
