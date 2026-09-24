package problem

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

var errSentinel = errors.New("sentinel")

func TestReportGate_Order(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name  string
		setup func(h *Handler)
		err   error
		ctx   *ErrorContext
		reqCx context.Context
		want  bool
	}{
		{name: "PlainErrorReported", err: errors.New("boom"), want: true},
		{name: "MarkedNotReportedAgain", err: contract.MarkReported(errors.New("boom")), want: false},
		{
			name: "PanicAlwaysReports_IgnoredType",
			setup: func(h *Handler) {
				Ignore[*panicerr.Error](h)
				h.IgnoreIf(func(error, *ErrorContext) bool { return true })
			},
			err:  panicerr.FromRecovered("boom"),
			want: true,
		},
		{
			name:  "PanicAlwaysReports_RecoveredContextOverridesShouldReport",
			err:   &reportableErr{report: false, code: 404},
			ctx:   &ErrorContext{Recovered: true},
			want:  true,
			setup: func(h *Handler) { Ignore[*reportableErr](h) },
		},
		{name: "PanicMarkedStillOnce", err: contract.MarkReported(panicerr.FromRecovered("boom")), want: false},
		{name: "ReportableFalseDropped", err: &reportableErr{report: false, code: 500}, want: false},
		{name: "ReportableTrueSkipsFrameworkIgnore", err: &reportableErr{report: true, code: 404}, want: true},
		{
			name:  "ReportableTrueStillUserIgnored",
			setup: func(h *Handler) { Ignore[*reportableErr](h) },
			err:   &reportableErr{report: true, code: 500},
			want:  false,
		},
		{name: "FrameworkIgnore_StatusBelow500", err: &statusErr{code: 404}, want: false},
		{name: "FrameworkIgnore_Status500Reported", err: &statusErr{code: 500}, want: true},
		{name: "FrameworkIgnore_HTTPError4xx", err: NotFound(), want: false},
		{name: "FrameworkIgnore_MaxBytes", err: fmt.Errorf("read: %w", &http.MaxBytesError{Limit: 1}), want: false},
		{name: "FrameworkIgnore_CanceledDeadRequest", err: context.Canceled, reqCx: canceled, want: false},
		{name: "CanceledLiveRequestReported", err: context.Canceled, want: true},
		{name: "DeadCancelIgnoredDespiteShouldReport", err: contract.NewHTTPError(http.StatusServiceUnavailable).WithCause(context.Canceled), reqCx: canceled, want: false},
		{name: "LiveCancelUnderServerErrorReported", err: contract.NewHTTPError(http.StatusServiceUnavailable).WithCause(context.Canceled), want: true},
		{name: "DeadlineReported", err: context.DeadlineExceeded, want: true},
		{
			name:  "UserIgnoreType",
			setup: func(h *Handler) { Ignore[*contextualErr](h) },
			err:   fmt.Errorf("wrapped: %w", &contextualErr{}),
			want:  false,
		},
		{
			name:  "UserIgnoreIs",
			setup: func(h *Handler) { IgnoreIs(h, errSentinel) },
			err:   fmt.Errorf("wrapped: %w", errSentinel),
			want:  false,
		},
		{
			name:  "IgnoreThenUnignore",
			setup: func(h *Handler) { Ignore[*contextualErr](h); Unignore[*contextualErr](h) },
			err:   &contextualErr{},
			want:  true,
		},
		{
			name:  "IgnoreIsThenUnignoreIs",
			setup: func(h *Handler) { IgnoreIs(h, errSentinel); UnignoreIs(h, errSentinel) },
			err:   errSentinel,
			want:  true,
		},
		{
			name:  "UnignoreThenIgnore",
			setup: func(h *Handler) { Unignore[*contextualErr](h); Ignore[*contextualErr](h) },
			err:   &contextualErr{},
			want:  false,
		},
		{
			name:  "UnignoreLiftsFrameworkIgnore",
			setup: func(h *Handler) { Unignore[*http.MaxBytesError](h) },
			err:   &http.MaxBytesError{Limit: 1},
			want:  true,
		},
		{
			name:  "UnignoreLiftsShouldReportFalse",
			setup: func(h *Handler) { Unignore[*contract.HTTPError](h) },
			err:   NotFound(),
			want:  true,
		},
		{
			name:  "UnignoreLiftsDeadCancel",
			setup: func(h *Handler) { UnignoreIs(h, context.Canceled) },
			err:   context.Canceled,
			reqCx: canceled,
			want:  true,
		},
		{
			name: "PredicateIgnores",
			setup: func(h *Handler) {
				h.IgnoreIf(func(err error, _ *ErrorContext) bool { return err.Error() == "noisy" })
			},
			err:  errors.New("noisy"),
			want: false,
		},
		{
			name: "PredicateSeesContext",
			setup: func(h *Handler) {
				h.IgnoreIf(func(_ error, ctx *ErrorContext) bool { return ctx != nil && ctx.Method == http.MethodHead })
			},
			err:  errors.New("boom"),
			ctx:  &ErrorContext{Method: http.MethodHead},
			want: false,
		},
		{
			name:  "MapRuleAppliesBeforeGate",
			setup: func(h *Handler) { MapIs(h, errSentinel, func(err error) error { return NotFound().WithCause(err) }) },
			err:   errSentinel,
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			if tt.setup != nil {
				tt.setup(h)
			}
			reqCx := tt.reqCx
			if reqCx == nil {
				reqCx = context.Background()
			}
			rc, _ := newRCWithContext(reqCx, http.MethodGet, "/x")
			h.HandleRequest(rc, tt.err, tt.ctx)
			if got := rep.count() == 1; got != tt.want {
				t.Errorf("reported = %v (count %d), want %v", got, rep.count(), tt.want)
			}
		})
	}
}

func TestReportGate_ThrottleSample(t *testing.T) {
	tests := []struct {
		name   string
		sample float64
		draw   float64
		want   bool
	}{
		{"DrawBelowSampleReported", 0.5, 0.1, true},
		{"DrawAboveSampleDropped", 0.5, 0.9, false},
		{"SampleOneAlwaysReported", 1, 0.99, true},
		{"SampleZeroDisabled", 0, 0.99, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			h.throttle.sample = func() float64 { return tt.draw }
			ThrottleFor[*contextualErr](h, contract.Throttle{Sample: tt.sample})
			h.Report(&contextualErr{}, nil)
			if got := rep.count() == 1; got != tt.want {
				t.Errorf("reported = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReportGate_ThrottleBucket(t *testing.T) {
	h, rep, _ := newTestHandler()
	now := time.Unix(1_000_000, 0)
	h.throttle.now = func() time.Time { return now }
	ThrottleFor[*contextualErr](h, contract.Throttle{
		MaxPerWindow: 2,
		Window:       time.Minute,
		By: func(err error) string {
			var c *contextualErr
			if errors.As(err, &c) {
				return fmt.Sprint(c.fields["tenant"])
			}
			return ""
		},
	})
	tenantA := &contextualErr{fields: map[string]any{"tenant": "a"}}
	tenantB := &contextualErr{fields: map[string]any{"tenant": "b"}}

	steps := []struct {
		name    string
		err     error
		advance time.Duration
		want    int
	}{
		{"FirstA", tenantA, 0, 1},
		{"SecondA", tenantA, 0, 2},
		{"ThirdADropped", tenantA, 0, 2},
		{"FirstBOwnBucket", tenantB, 0, 3},
		{"UnmatchedNotThrottled", errors.New("other"), 0, 4},
		{"WindowResetsA", tenantA, time.Minute, 5},
	}
	for _, st := range steps {
		now = now.Add(st.advance)
		h.Report(st.err, nil)
		if got := rep.count(); got != st.want {
			t.Fatalf("%s: reports = %d, want %d", st.name, got, st.want)
		}
	}
}

func TestReportGate_ThrottleDefaultWindowAndSweep(t *testing.T) {
	b := newThrottleBuckets()
	now := time.Unix(0, 0)
	b.now = func() time.Time { return now }
	rule := contract.ThrottleRule{Match: func(error) bool { return true }, Throttle: contract.Throttle{MaxPerWindow: 1}}
	if !b.allow(rule, errSentinel) || b.allow(rule, errSentinel) {
		t.Fatal("anonymous rule must allow exactly one report per window")
	}
	now = now.Add(defaultThrottleWindow)
	if !b.allow(rule, errSentinel) {
		t.Fatal("default window must reset after one minute")
	}

	keyed := contract.ThrottleRule{Key: "k", Match: rule.Match, Throttle: contract.Throttle{
		MaxPerWindow: 1, Window: time.Second, By: func(err error) string { return err.Error() },
	}}
	for i := 0; i < maxThrottleBuckets+10; i++ {
		b.allow(keyed, fmt.Errorf("e%d", i))
		now = now.Add(2 * time.Second)
	}
	b.mu.Lock()
	n := len(b.buckets)
	b.mu.Unlock()
	if n > maxThrottleBuckets {
		t.Errorf("buckets = %d, want at most %d after sweeping", n, maxThrottleBuckets)
	}
}

// TestReportGate_ThrottleEvictsOldestLiveBucket asserts the bucket map
// never grows past maxThrottleBuckets when no bucket has expired: with a
// fixed hour-long window, each insert into a full map evicts the bucket
// whose window started first.
func TestReportGate_ThrottleEvictsOldestLiveBucket(t *testing.T) {
	b := newThrottleBuckets()
	now := time.Unix(0, 0)
	b.now = func() time.Time { return now }
	rule := contract.ThrottleRule{Key: "k", Match: func(error) bool { return true }, Throttle: contract.Throttle{
		MaxPerWindow: 1, Window: time.Hour, By: func(err error) string { return err.Error() },
	}}
	const overflow = 50
	for i := 0; i < maxThrottleBuckets+overflow; i++ {
		if !b.allow(rule, fmt.Errorf("e%d", i)) {
			t.Fatalf("insert %d: a new key must get its first report", i)
		}
		b.mu.Lock()
		n := len(b.buckets)
		b.mu.Unlock()
		if n > maxThrottleBuckets {
			t.Fatalf("insert %d: buckets = %d, want at most %d", i, n, maxThrottleBuckets)
		}
		now = now.Add(time.Millisecond)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	for i := 0; i < overflow; i++ {
		if _, kept := b.buckets[bucketID{rule: "k", by: fmt.Sprintf("e%d", i)}]; kept {
			t.Errorf("bucket e%d kept, want it evicted as one of the oldest", i)
		}
	}
	for _, i := range []int{overflow, maxThrottleBuckets + overflow - 1} {
		if _, kept := b.buckets[bucketID{rule: "k", by: fmt.Sprintf("e%d", i)}]; !kept {
			t.Errorf("bucket e%d evicted, want it kept", i)
		}
	}
}

func TestReportGate_ShouldReportDoesNotConsumeThrottle(t *testing.T) {
	h, rep, _ := newTestHandler()
	ThrottleFor[*contextualErr](h, contract.Throttle{MaxPerWindow: 1, Window: time.Hour})
	for i := 0; i < 3; i++ {
		if !h.ShouldReport(&contextualErr{}) {
			t.Fatal("ShouldReport must pass a throttled type")
		}
	}
	h.Report(&contextualErr{}, nil)
	if rep.count() != 1 {
		t.Errorf("reports = %d, want 1: ShouldReport consumed the budget", rep.count())
	}
}

func TestReportGate_StopsAndStages(t *testing.T) {
	tests := []struct {
		name          string
		err           func() error
		setup         func(h *Handler, calls *[]string)
		wantReporters int
		wantCalls     []string
	}{
		{
			name:          "SelfReportStops",
			err:           func() error { return &selfErr{stop: true} },
			setup:         func(h *Handler, calls *[]string) { recordReportFor[*selfErr](h, calls, false) },
			wantReporters: 0,
			wantCalls:     nil,
		},
		{
			name:          "SelfReportContinues",
			err:           func() error { return &selfErr{stop: false} },
			setup:         func(h *Handler, calls *[]string) { recordReportFor[*selfErr](h, calls, false) },
			wantReporters: 1,
			wantCalls:     []string{"reportFor"},
		},
		{
			name:          "TypedReportStops",
			err:           func() error { return &contextualErr{} },
			setup:         func(h *Handler, calls *[]string) { recordReportFor[*contextualErr](h, calls, true) },
			wantReporters: 0,
			wantCalls:     []string{"reportFor"},
		},
		{
			name:          "TypedReportContinues",
			err:           func() error { return &contextualErr{} },
			setup:         func(h *Handler, calls *[]string) { recordReportFor[*contextualErr](h, calls, false) },
			wantReporters: 1,
			wantCalls:     []string{"reportFor"},
		},
		{
			name: "ThrottledNeverReachesSelfReport",
			err:  func() error { return &selfErr{stop: false} },
			setup: func(h *Handler, calls *[]string) {
				h.throttle.sample = func() float64 { return 0.99 }
				ThrottleFor[*selfErr](h, contract.Throttle{Sample: 0.1})
				recordReportFor[*selfErr](h, calls, false)
			},
			wantReporters: 0,
			wantCalls:     nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			var calls []string
			tt.setup(h, &calls)
			err := tt.err()
			h.Report(err, nil)
			if rep.count() != tt.wantReporters {
				t.Errorf("reporter calls = %d, want %d", rep.count(), tt.wantReporters)
			}
			if fmt.Sprint(calls) != fmt.Sprint(tt.wantCalls) {
				t.Errorf("stage calls = %v, want %v", calls, tt.wantCalls)
			}
			var self *selfErr
			if errors.As(err, &self) && tt.name == "ThrottledNeverReachesSelfReport" && self.hits != 0 {
				t.Error("throttled error reached ReportError")
			}
		})
	}
}

// recordReportFor registers a ReportFor[T] rule that records its call and
// asserts it runs before the context merge.
func recordReportFor[T error](h *Handler, calls *[]string, stop bool) {
	h.ContextUsing(func(error, *ErrorContext) map[string]any { return map[string]any{"merged": true} })
	ReportFor[T](h, func(_ T, ctx *ErrorContext) bool {
		if _, merged := ctx.Extra["merged"]; merged {
			*calls = append(*calls, "merged-too-early")
		}
		*calls = append(*calls, "reportFor")
		return stop
	})
}

func TestReportGate_ContextMerge(t *testing.T) {
	h, rep, _ := newTestHandler()
	h.ContextUsing(func(err error, _ *ErrorContext) map[string]any {
		return map[string]any{"provider": err.Error(), "shared": "provider"}
	})
	h.Report(&contextualErr{fields: map[string]any{"order": 42, "shared": "error"}}, nil)
	ctx, _ := rep.last()
	if ctx == nil {
		t.Fatal("not reported")
	}
	want := map[string]any{"order": 42, "provider": "contextual", "shared": "provider"}
	for k, v := range want {
		if ctx.Extra[k] != v {
			t.Errorf("Extra[%q] = %v, want %v", k, ctx.Extra[k], v)
		}
	}
}

func TestReportGate_LevelSelection(t *testing.T) {
	tests := []struct {
		name  string
		setup func(h *Handler)
		err   error
		ctx   *ErrorContext
		want  contract.LogLevel
	}{
		{name: "DefaultError", err: errors.New("boom"), want: contract.LogLevelError},
		{name: "CallerLevelKept", err: errors.New("boom"), ctx: &ErrorContext{Level: contract.LogLevelInfo}, want: contract.LogLevelInfo},
		{name: "FrameworkDeadlineWarn", err: fmt.Errorf("q: %w", context.DeadlineExceeded), want: contract.LogLevelWarn},
		{
			name:  "UserOutranksFramework",
			setup: func(h *Handler) { LevelIs(h, context.DeadlineExceeded, contract.LogLevelInfo) },
			err:   context.DeadlineExceeded,
			want:  contract.LogLevelInfo,
		},
		{
			name:  "LevelForType",
			setup: func(h *Handler) { LevelFor[*contextualErr](h, contract.LogLevelDebug) },
			err:   &contextualErr{},
			want:  contract.LogLevelDebug,
		},
		{
			name: "LaterKeyReplacesEarlier",
			setup: func(h *Handler) {
				LevelFor[*contextualErr](h, contract.LogLevelDebug)
				LevelFor[*contextualErr](h, contract.LogLevelWarn)
			},
			err:  &contextualErr{},
			want: contract.LogLevelWarn,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			if tt.setup != nil {
				tt.setup(h)
			}
			h.Report(tt.err, tt.ctx)
			ctx, _ := rep.last()
			if ctx == nil {
				t.Fatal("not reported")
			}
			if ctx.Level != tt.want {
				t.Errorf("Level = %v, want %v", ctx.Level, tt.want)
			}
		})
	}
}

func TestReportGate_PanicsInUserCodeAreContained(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(h *Handler)
		wantLog string
		wantRep int
	}{
		{
			name:    "MapRulePanics",
			setup:   func(h *Handler) { MapIs(h, errSentinel, func(error) error { panic("map") }) },
			wantLog: "problem: map rule panicked",
			wantRep: 1,
		},
		{
			name:    "PredicatePanics",
			setup:   func(h *Handler) { h.IgnoreIf(func(error, *ErrorContext) bool { panic("pred") }) },
			wantLog: "problem: report failed",
			wantRep: 0,
		},
		{
			name: "ReporterPanicsOthersRun",
			setup: func(h *Handler) {
				h.SetReporters(NewCallbackReporter(func(error, *ErrorContext) { panic("rep") }))
			},
			wantLog: "problem: reporter panicked",
			wantRep: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, logger := newTestHandler()
			tt.setup(h)
			if tt.name == "ReporterPanicsOthersRun" {
				h.AddReporter(rep)
			}
			rc, w := newRC(http.MethodGet, "/x")
			h.HandleRequest(rc, errSentinel, nil)
			if !logger.has("error", tt.wantLog) {
				t.Errorf("missing log %q in %v", tt.wantLog, logger.all())
			}
			if rep.count() != tt.wantRep {
				t.Errorf("reports = %d, want %d", rep.count(), tt.wantRep)
			}
			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500 still rendered", w.Code)
			}
		})
	}
}

func TestHandler_ConcurrentRulesAndRequests(t *testing.T) {
	h, _, _ := newTestHandler()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				Ignore[*contextualErr](h)
				Unignore[*contextualErr](h)
				LevelFor[*statusErr](h, contract.LogLevelWarn)
				ThrottleFor[*selfErr](h, contract.Throttle{MaxPerWindow: 5})
				h.SetAPIPrefixes("/api")
				h.AddRenderer("json", NewJSONRenderer())
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				rc, _ := newRC(http.MethodGet, "/api/x", "Accept", "application/json")
				h.HandleRequest(rc, &contextualErr{}, nil)
				h.Report(&selfErr{}, nil)
				_ = h.ShouldReport(&statusErr{code: 500})
			}
		}()
	}
	wg.Wait()
}

// TestHandleRequest_MarkersInsideAndAroundAPanic asserts a response-written
// or report-once marker the panic value carries counts for nothing (a
// reported, rendered 500), while one wrapped around the panic still
// counts.
func TestHandleRequest_MarkersInsideAndAroundAPanic(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		recovered   bool
		wantReports int
		wantStatus  int // 0: nothing rendered
	}{
		{name: "SentinelInsidePanic", err: panicerr.FromRecovered(contract.ErrResponseWritten), wantReports: 1, wantStatus: http.StatusInternalServerError},
		{name: "HandledInsidePanic", err: panicerr.FromRecovered(contract.Handled(errors.New("x"))), wantReports: 1, wantStatus: http.StatusInternalServerError},
		{name: "ReportedInsidePanic", err: panicerr.FromRecovered(contract.MarkReported(errors.New("x"))), wantReports: 1, wantStatus: http.StatusInternalServerError},
		{name: "SentinelFlaggedRecovered", err: contract.ErrResponseWritten, recovered: true, wantReports: 1, wantStatus: http.StatusInternalServerError},
		{name: "HandledAroundPanic", err: contract.Handled(panicerr.FromRecovered("boom")), wantReports: 1},
		{name: "ReportedAroundPanic", err: contract.MarkReported(panicerr.FromRecovered("boom")), wantStatus: http.StatusInternalServerError},
		{name: "BareSentinel", err: contract.ErrResponseWritten},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			ctx := NewErrorContext()
			ctx.Recovered = tt.recovered
			rc, w := newRC(http.MethodGet, "/x", "Accept", "application/json")
			h.HandleRequest(rc, tt.err, ctx)
			if rep.count() != tt.wantReports {
				t.Errorf("reports = %d, want %d", rep.count(), tt.wantReports)
			}
			if tt.wantStatus == 0 {
				if rc.Written() {
					t.Errorf("rendered %d, want nothing", w.Code)
				}
				return
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

// TestMarkers_OutsidePanicParity pins where a report-once or
// response-written marker counts relative to a recovered panic, through
// HandleRequest and Report: a marker inside the panic value never counts,
// one around the panic or on a sibling branch of a join does, both inside
// and around counts once, and a ctx flagged recovered whose error carries
// no panic node treats the whole error as the panic value.
func TestMarkers_OutsidePanicParity(t *testing.T) {
	x := errors.New("x")
	p := func(v any) error { return panicerr.FromRecovered(v) }
	tests := []struct {
		name        string
		err         error
		recovered   bool
		wantReports int // through HandleRequest
		wantStatus  int // 0: nothing rendered
		wantReport  int // through Report
	}{
		{name: "NoPanicReported", err: contract.MarkReported(x), wantStatus: 500},
		{name: "NoPanicHandled", err: contract.Handled(x), wantReports: 1, wantReport: 1},
		{name: "ReportedInsidePanicOnly", err: p(contract.MarkReported(x)), recovered: true, wantReports: 1, wantStatus: 500, wantReport: 1},
		{name: "HandledInsidePanicOnly", err: p(contract.Handled(x)), recovered: true, wantReports: 1, wantStatus: 500, wantReport: 1},
		{name: "ReportedAroundPanic", err: contract.MarkReported(p("boom")), recovered: true, wantStatus: 500},
		{name: "ReportedInsideAndAround", err: contract.MarkReported(p(contract.MarkReported(x))), recovered: true, wantStatus: 500},
		{name: "HandledInsideAndAround", err: contract.Handled(p(contract.Handled(x))), recovered: true, wantReports: 1, wantReport: 1},
		{name: "RecoveredNoPanicNodeReported", err: contract.MarkReported(x), recovered: true, wantReports: 1, wantStatus: 500, wantReport: 1},
		{name: "RecoveredNoPanicNodeHandled", err: contract.Handled(x), recovered: true, wantReports: 1, wantStatus: 500, wantReport: 1},
		{name: "JoinReportedSibling", err: errors.Join(p("boom"), contract.MarkReported(x)), recovered: true, wantStatus: 500},
		{name: "JoinHandledSibling", err: errors.Join(p("boom"), contract.Handled(x)), recovered: true, wantReports: 1, wantReport: 1},
		{name: "HandledAroundPanic", err: contract.Handled(p("boom")), recovered: true, wantReports: 1, wantReport: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			ctx := NewErrorContext()
			ctx.Recovered = tt.recovered
			rc, w := newRC(http.MethodGet, "/x", "Accept", "application/json")
			h.HandleRequest(rc, tt.err, ctx)
			if rep.count() != tt.wantReports {
				t.Errorf("HandleRequest reports = %d, want %d", rep.count(), tt.wantReports)
			}
			if tt.wantStatus == 0 && rc.Written() {
				t.Errorf("rendered %d, want nothing", w.Code)
			}
			if tt.wantStatus != 0 && w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}

			h2, rep2, _ := newTestHandler()
			ctx2 := NewErrorContext()
			ctx2.Recovered = tt.recovered
			h2.Report(tt.err, ctx2)
			if rep2.count() != tt.wantReport {
				t.Errorf("Report reports = %d, want %d", rep2.count(), tt.wantReport)
			}
		})
	}
}

// TestReport_ThenMarkARecoveredPanic asserts the report-once flow for a
// recovered panic whose value was marked: reporting it once and marking
// the result keeps the boundary from reporting it again.
func TestReport_ThenMarkARecoveredPanic(t *testing.T) {
	h, rep, _ := newTestHandler()
	pe := panicerr.FromRecovered(contract.MarkReported(errors.New("reported inside the handler")))

	h.Report(pe, nil)
	marked := contract.MarkReported(pe)

	ctx := NewErrorContext()
	ctx.Recovered = true
	rc, w := newRC(http.MethodGet, "/x", "Accept", "application/json")
	h.HandleRequest(rc, marked, ctx)

	if rep.count() != 1 {
		t.Errorf("reports = %d, want exactly 1", rep.count())
	}
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// TestHandleRequest_DeepChainMarkers asserts a marker counts by position
// at any depth: a recovered panic carrying a marker under more wrappers
// than a depth-limited walk visits is a reported 500, a plain marker that
// deep still counts, and a marker past the marker walk's cap does not.
func TestHandleRequest_DeepChainMarkers(t *testing.T) {
	wrap := func(err error, n int) error {
		for i := 0; i < n; i++ {
			err = fmt.Errorf("layer %d: %w", i, err)
		}
		return err
	}
	tests := []struct {
		name        string
		err         error
		wantReports int
		wantStatus  int // 0: nothing rendered
	}{
		{name: "SentinelInsidePanicPastWalkLimit", err: wrap(panicerr.FromRecovered(contract.ErrResponseWritten), 65), wantReports: 1, wantStatus: http.StatusInternalServerError},
		{name: "ReportedInsidePanicPastWalkLimit", err: wrap(panicerr.FromRecovered(contract.MarkReported(errors.New("x"))), 65), wantReports: 1, wantStatus: http.StatusInternalServerError},
		{name: "SentinelPastWalkLimit", err: wrap(contract.ErrResponseWritten, 65)},
		{name: "ReportedPastWalkLimit", err: wrap(contract.MarkReported(errors.New("x")), 65), wantStatus: http.StatusInternalServerError},
		{name: "SentinelPastMarkerCap", err: wrap(contract.ErrResponseWritten, 1025), wantReports: 1, wantStatus: http.StatusInternalServerError},
		{name: "ReportedPastMarkerCap", err: wrap(contract.MarkReported(errors.New("x")), 1025), wantReports: 1, wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			rc, w := newRC(http.MethodGet, "/x", "Accept", "application/json")
			h.HandleRequest(rc, tt.err, nil)
			if rep.count() != tt.wantReports {
				t.Errorf("reports = %d, want %d", rep.count(), tt.wantReports)
			}
			if tt.wantStatus == 0 {
				if rc.Written() {
					t.Errorf("rendered %d, want nothing", w.Code)
				}
				return
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

// consumerRecovered stands for the recovered-panic error of a consumer's
// own recovery middleware: it implements contract.RecoveredPanic and
// unwraps to the error it carries, and is no framework panic type.
type consumerRecovered struct{ err error }

func (e *consumerRecovered) Error() string  { return "recovered: " + e.err.Error() }
func (e *consumerRecovered) Recovered() any { return e.err }
func (e *consumerRecovered) Unwrap() error  { return e.err }

// TestHandleRequest_ConsumerRecoveredPanic asserts any contract.RecoveredPanic
// is a recovered panic to the pipeline: always reported with Recovered
// set, always a 500 that does not echo the value's message, whatever the
// value would answer or be ignored as on its own.
func TestHandleRequest_ConsumerRecoveredPanic(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name  string
		value error
		reqCx context.Context
	}{
		{name: "ClientError", value: NotFound("payload")},
		{name: "CanceledLiveRequest", value: context.Canceled},
		{name: "CanceledDeadRequest", value: context.Canceled, reqCx: dead},
		{name: "IgnoredSentinel", value: fmt.Errorf("wrapped: %w", errSentinel)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			IgnoreIs(h, errSentinel)
			err := &consumerRecovered{err: tt.value}
			if !h.ShouldReport(err) {
				t.Error("ShouldReport = false, want true")
			}
			reqCx := tt.reqCx
			if reqCx == nil {
				reqCx = context.Background()
			}
			rc, w := newRCWithContext(reqCx, http.MethodGet, "/x")
			rc.Request().Header.Set("Accept", "application/json")
			h.HandleRequest(rc, err, nil)
			if rep.count() != 1 {
				t.Fatalf("reports = %d, want 1", rep.count())
			}
			if ctx, _ := rep.last(); ctx == nil || !ctx.Recovered {
				t.Errorf("reported ctx = %+v, want Recovered", ctx)
			}
			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", w.Code)
			}
			if strings.Contains(w.Body.String(), "payload") {
				t.Errorf("body leaks the panic value's message: %q", w.Body.String())
			}
		})
	}
}

// TestRenderFor_RecoveredPanicFacet asserts a render rule keyed on
// contract.RecoveredPanic sees every recovered panic, the framework's and a
// consumer's own, pinned at 500.
func TestRenderFor_RecoveredPanicFacet(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "Framework", err: panicerr.FromRecovered(NotFound("payload"))},
		{name: "Consumer", err: &consumerRecovered{err: NotFound("payload")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler()
			calls := 0
			RenderFor(h, func(rc RenderContext, _ contract.RecoveredPanic, _ *ErrorContext) bool {
				calls++
				rc.WriteHeader(http.StatusInternalServerError)
				return true
			})
			rc, w := newRC(http.MethodGet, "/x", "Accept", "application/json")
			h.HandleRequest(rc, tt.err, nil)
			if calls != 1 || w.Code != http.StatusInternalServerError {
				t.Errorf("rule calls = %d, status = %d; want 1 and 500", calls, w.Code)
			}
		})
	}
}
