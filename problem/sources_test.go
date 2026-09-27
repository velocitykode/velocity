package problem

import (
	"context"
	"fmt"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// staleRecordError is an application error type with rules written for
// requests.
type staleRecordError struct{ id string }

func (e *staleRecordError) Error() string { return "stale record " + e.id }

// sourcesHandler returns a handler whose only reporter is rec.
func sourcesHandler(rec *recReporter) *Handler {
	return NewHandler(WithHandlerLogger(&recLogger{}), WithReporters(rec))
}

// reportFrom reports err from source through h.
func reportFrom(h *Handler, err error, source contract.ErrorSource) {
	h.Report(err, &ErrorContext{Source: source})
}

// TestReport_RequestRulesSkipBackgroundSources asserts the rules written for
// requests (the default for every rule, the framework's own included) do
// not apply to a report from background work: a job error of a type a
// request ignore, map, level and throttle rule match is reported as is,
// once per failure, at error level; the same error reported for a request
// is ignored.
func TestReport_RequestRulesSkipBackgroundSources(t *testing.T) {
	for _, source := range []contract.ErrorSource{
		contract.ErrorSourceJob,
		contract.ErrorSourceListener,
		contract.ErrorSourceTask,
		contract.ErrorSourceGoroutine,
	} {
		rec := &recReporter{}
		h := sourcesHandler(rec)
		Ignore[*staleRecordError](h)
		MapFor(h, func(e *staleRecordError) error { return fmt.Errorf("mapped for requests: %w", e) })
		LevelFor[*staleRecordError](h, contract.LogLevelInfo)
		ThrottleFor[*staleRecordError](h, contract.Throttle{MaxPerWindow: 1})

		cause := &staleRecordError{id: "7"}
		reportFrom(h, cause, source)
		reportFrom(h, cause, source)
		if rec.count() != 2 {
			t.Fatalf("source %v: reported %d times, want 2 (request rules must not ignore or throttle it)", source, rec.count())
		}
		if rec.errs[0] != error(cause) {
			t.Errorf("source %v: reported %#v, want the original error (no request map rule)", source, rec.errs[0])
		}
		if got := rec.ctxs[0].Level; got != contract.LogLevelError {
			t.Errorf("source %v: level = %v, want error (no request level rule)", source, got)
		}

		reportFrom(h, cause, 0)
		reportFrom(h, cause, contract.ErrorSourceRequest)
		if rec.count() != 2 {
			t.Errorf("source %v: request reports passed the request ignore rule: %d reports", source, rec.count())
		}
	}
}

// TestReport_FrameworkRulesAndOwnDecisionAreForRequests asserts the
// framework's request rules (a status below 500 is the client's, a
// deadline is logged at warn) and the error's own ShouldReport, whose
// answer is about a client's request, do not drop or reshape a report from
// background work.
func TestReport_FrameworkRulesAndOwnDecisionAreForRequests(t *testing.T) {
	rec := &recReporter{}
	h := sourcesHandler(rec)

	notFound := contract.NewHTTPError(404, "upstream record gone")
	reportFrom(h, notFound, contract.ErrorSourceJob)
	reportFrom(h, context.DeadlineExceeded, contract.ErrorSourceTask)
	if rec.count() != 2 {
		t.Fatalf("background reports = %d, want 2", rec.count())
	}
	if rec.errs[0] != error(notFound) {
		t.Errorf("reported %#v, want the job's HTTPError", rec.errs[0])
	}
	if got := rec.ctxs[1].Level; got != contract.LogLevelError {
		t.Errorf("deadline from a task logged at %v, want error", got)
	}

	reportFrom(h, notFound, contract.ErrorSourceRequest)
	if rec.count() != 2 {
		t.Errorf("a 404 reported for a request passed the gate")
	}
	if h.ShouldReport(notFound) {
		t.Error("ShouldReport(404) = true, want false for a request")
	}
}

// TestReport_RulesWrittenForASourceApply asserts a rule that names a
// background source sees the original error of a report from that source,
// and only there; it sits beside the request rule under the same Key
// instead of replacing it.
func TestReport_RulesWrittenForASourceApply(t *testing.T) {
	rec := &recReporter{}
	h := sourcesHandler(rec)
	Ignore[*staleRecordError](h)
	h.AddLevelRule(contract.LevelRule{
		Key:     typeKey[*staleRecordError](),
		Match:   matchAs[*staleRecordError](),
		Level:   contract.LogLevelWarn,
		Sources: contract.ErrorSourceJob | contract.ErrorSourceListener,
	})

	cause := &staleRecordError{id: "9"}
	reportFrom(h, cause, contract.ErrorSourceJob)
	reportFrom(h, cause, contract.ErrorSourceTask)
	reportFrom(h, cause, contract.ErrorSourceRequest)
	if rec.count() != 2 {
		t.Fatalf("reports = %d, want 2 (job and task; the request one ignored)", rec.count())
	}
	if got := rec.ctxs[0].Level; got != contract.LogLevelWarn {
		t.Errorf("job report level = %v, want warn from the rule written for jobs", got)
	}
	if got := rec.ctxs[1].Level; got != contract.LogLevelError {
		t.Errorf("task report level = %v, want error (the rule names jobs and listeners)", got)
	}

	// A job ignore under the request ignore's Key keeps both; unignoring
	// for requests removes only the request ignore.
	h.AddIgnoreRule(contract.IgnoreRule{Key: typeKey[*staleRecordError](), Match: matchAs[*staleRecordError](), Sources: contract.ErrorSourceJob})
	reportFrom(h, cause, contract.ErrorSourceJob)
	reportFrom(h, cause, contract.ErrorSourceRequest)
	if rec.count() != 2 {
		t.Fatalf("reports = %d, want still 2 (ignored for jobs and requests)", rec.count())
	}
	Unignore[*staleRecordError](h)
	reportFrom(h, cause, contract.ErrorSourceRequest)
	reportFrom(h, cause, contract.ErrorSourceJob)
	if rec.count() != 3 {
		t.Fatalf("reports = %d, want 3 (the request report unignored, the job one still ignored)", rec.count())
	}
}

// TestThrottle_SourcesKeepBucketsApart asserts a throttle rule written for
// jobs under a request throttle rule's Key counts in buckets of its own.
func TestThrottle_SourcesKeepBucketsApart(t *testing.T) {
	rec := &recReporter{}
	h := sourcesHandler(rec)
	ThrottleFor[*staleRecordError](h, contract.Throttle{MaxPerWindow: 1})
	h.AddThrottleRule(contract.ThrottleRule{
		Key:      typeKey[*staleRecordError](),
		Match:    matchAs[*staleRecordError](),
		Throttle: contract.Throttle{MaxPerWindow: 1},
		Sources:  contract.ErrorSourceJob,
	})

	cause := &staleRecordError{id: "3"}
	reportFrom(h, cause, contract.ErrorSourceRequest)
	reportFrom(h, cause, contract.ErrorSourceJob)
	reportFrom(h, cause, contract.ErrorSourceRequest)
	reportFrom(h, cause, contract.ErrorSourceJob)
	if rec.count() != 2 {
		t.Fatalf("reports = %d, want 2 (one per source's bucket)", rec.count())
	}
}

// TestErrorSource_Includes asserts the zero value stands for requests on
// either side and sets match by any shared source.
func TestErrorSource_Includes(t *testing.T) {
	tests := []struct {
		set, source contract.ErrorSource
		want        bool
	}{
		{0, 0, true},
		{0, contract.ErrorSourceRequest, true},
		{contract.ErrorSourceRequest, 0, true},
		{0, contract.ErrorSourceJob, false},
		{contract.ErrorSourceJob, 0, false},
		{contract.ErrorSourceJob | contract.ErrorSourceTask, contract.ErrorSourceTask, true},
		{contract.ErrorSourceJob | contract.ErrorSourceTask, contract.ErrorSourceGoroutine, false},
	}
	for _, tt := range tests {
		if got := tt.set.Includes(tt.source); got != tt.want {
			t.Errorf("%v.Includes(%v) = %v, want %v", tt.set, tt.source, got, tt.want)
		}
	}
}
