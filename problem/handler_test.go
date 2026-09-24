package problem

import (
	"bytes"
	"errors"
	stdlog "log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

func TestNewHandler_Defaults(t *testing.T) {
	logger := &recLogger{}
	h := NewHandler(WithHandlerLogger(logger))
	if h.IsDebug() || h.GetEnvironment() != "production" || h.IsAPIMode() {
		t.Error("unexpected defaults")
	}
	h.Report(errors.New("default reporter"), nil)
	if !logger.has("error", "default reporter") {
		t.Errorf("default LogReporter did not log through the handler logger: %v", logger.all())
	}
}

func TestNewHandler_StderrLoggerFallback(t *testing.T) {
	h := NewHandler(WithReporters())
	if _, ok := h.logger.(stderrLogger); !ok {
		t.Fatalf("logger = %T, want the stderr fallback", h.logger)
	}
	var buf bytes.Buffer
	l := stderrLogger{l: stdlog.New(&buf, "", 0)}
	for _, fn := range []func(string, ...any){l.Debug, l.Info, l.Warn, l.Error, l.Fatal} {
		fn("line", "k", "v")
	}
	want := "[DEBUG] line k v\n[INFO] line k v\n[WARN] line k v\n[ERROR] line k v\n[ERROR] line k v\n"
	if buf.String() != want {
		t.Errorf("stderr logger output = %q, want %q", buf.String(), want)
	}
}

func TestNewHandler_DebugPolicy(t *testing.T) {
	tests := []struct {
		name      string
		env       string
		debug     bool
		wantDebug bool
		wantWarn  bool
	}{
		{"ProductionForcesOff", "production", true, false, true},
		{"StagingForcesOff", "staging", true, false, true},
		{"DevelopmentAllows", "development", true, true, true},
		{"OffNoWarning", "development", false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &recLogger{}
			h := NewHandler(WithHandlerLogger(logger), WithEnvironment(tt.env), WithDebug(tt.debug))
			if h.IsDebug() != tt.wantDebug {
				t.Errorf("IsDebug = %v, want %v", h.IsDebug(), tt.wantDebug)
			}
			warned := false
			for _, e := range logger.all() {
				warned = warned || e.level == "warn"
			}
			if warned != tt.wantWarn {
				t.Errorf("warned = %v, want %v", warned, tt.wantWarn)
			}
		})
	}
}

func TestHandler_SetDebug(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		set   bool
		want  bool
		wantW bool
	}{
		{"RefusedInProduction", "production", true, false, true},
		{"EnabledInDev", "local", true, true, true},
		{"Disabled", "local", false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &recLogger{}
			h := NewHandler(WithHandlerLogger(logger), WithEnvironment(tt.env))
			h.SetDebug(tt.set)
			if h.IsDebug() != tt.want {
				t.Errorf("IsDebug = %v, want %v", h.IsDebug(), tt.want)
			}
			if got := len(logger.all()) > 0; got != tt.wantW {
				t.Errorf("warned = %v, want %v", got, tt.wantW)
			}
		})
	}
}

func TestHandler_Settings(t *testing.T) {
	h, rep, _ := newTestHandler(WithAPIPrefixes("/a"), WithAPIMode(true))
	if !h.IsAPIMode() || h.GetAPIPrefixes()[0] != "/a" {
		t.Error("options not applied")
	}
	h.SetAPIMode(false)
	h.SetAPIPrefixes("/b", "/c")
	prefixes := h.GetAPIPrefixes()
	prefixes[0] = "/mutated"
	if h.IsAPIMode() || h.GetAPIPrefixes()[0] != "/b" {
		t.Error("setters not applied or getter leaked internal slice")
	}
	h.SetEnvironment("local")
	if h.GetEnvironment() != "local" {
		t.Error("SetEnvironment not applied")
	}

	second := &recReporter{}
	h.AddReporter(second)
	h.AddReporter(nil)
	h.Report(errors.New("x"), nil)
	if rep.count() != 1 || second.count() != 1 {
		t.Error("AddReporter did not append")
	}
	h.SetReporters(second)
	h.Report(errors.New("y"), nil)
	if rep.count() != 1 || second.count() != 2 {
		t.Error("SetReporters did not replace")
	}

	h.AddRenderer("html", nil)
	h.AddRenderer("html", failRenderer{})
	rc, w := newRC(http.MethodGet, "/x")
	h.Render(rc, errors.New("z"), nil)
	if w.Body.String() != "Internal Server Error" {
		t.Error("AddRenderer replacement not used")
	}
}

func TestHandler_TrustedProxiesCloned(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("192.0.2.0/24")
	proxies := []*net.IPNet{trusted}
	h, rep, _ := newTestHandler()
	h.SetTrustedProxies(proxies)
	trusted.IP = net.ParseIP("10.0.0.0")
	rc, _ := newRC(http.MethodGet, "/x", "X-Forwarded-For", "203.0.113.9")
	h.HandleRequest(rc, errors.New("boom"), nil)
	if ctx, _ := rep.last(); ctx.IP != "203.0.113.9" {
		t.Errorf("IP = %q: caller mutation reached the handler's proxy list", ctx.IP)
	}
}

func TestHandler_RuleRegistration(t *testing.T) {
	tests := []struct {
		name  string
		apply func(h *Handler)
		count func(h *Handler) int
		want  int
	}{
		{
			name: "NilMatchersDropped",
			apply: func(h *Handler) {
				h.AddMapRule(contract.MapRule{})
				h.AddRenderRule(contract.RenderRule{Match: matchIs(errSentinel)})
				h.AddReportRule(contract.ReportRule{Match: matchIs(errSentinel)})
				h.AddIgnoreRule(contract.IgnoreRule{})
				h.AddLevelRule(contract.LevelRule{})
				h.AddThrottleRule(contract.ThrottleRule{})
				h.IgnoreIf(nil)
				h.ContextUsing(nil)
				h.BeforeRender(nil)
			},
			count: func(h *Handler) int {
				return len(h.mapRules) + len(h.renderRules) + len(h.reportRules) + len(h.ignoreRules) +
					len(h.levelRules) + len(h.throttleRules) + len(h.ignorePredicates) + len(h.contextProviders) + len(h.beforeRender)
			},
			want: 0,
		},
		{
			name: "SameKeyReplacesInPlace",
			apply: func(h *Handler) {
				MapIs(h, errSentinel, func(error) error { return NotFound() })
				MapFor[*statusErr](h, func(*statusErr) error { return nil })
				MapIs(h, errSentinel, func(error) error { return Gone() })
			},
			count: func(h *Handler) int { return len(h.mapRules) },
			want:  2,
		},
		{
			name: "AnonymousRulesAppend",
			apply: func(h *Handler) {
				h.AddMapRule(contract.MapRule{Match: matchIs(errSentinel), Map: func(err error) error { return err }})
				h.AddMapRule(contract.MapRule{Match: matchIs(errSentinel), Map: func(err error) error { return err }})
			},
			count: func(h *Handler) int { return len(h.mapRules) },
			want:  2,
		},
		{
			name: "NonComparableKeyIsAnonymous",
			apply: func(h *Handler) {
				h.AddLevelRule(contract.LevelRule{Key: []int{1}, Match: matchIs(errSentinel)})
				h.AddLevelRule(contract.LevelRule{Key: []int{1}, Match: matchIs(errSentinel)})
			},
			count: func(h *Handler) int { return len(h.levelRules) },
			want:  2,
		},
		{
			name: "FrameworkRegistrationGuards",
			apply: func(h *Handler) {
				h.AddFrameworkIgnoreRule(contract.IgnoreRule{})
				h.AddFrameworkIgnoreRule(contract.IgnoreRule{Match: matchIs(errSentinel), Unignore: true})
				h.AddFrameworkPrepareRule(contract.MapRule{Match: matchIs(errSentinel)})
				h.AddFrameworkRenderRule(contract.RenderRule{Match: matchIs(errSentinel)})
				h.AddFrameworkLevelRule(contract.LevelRule{})
			},
			count: func(h *Handler) int {
				base := NewHandler(WithReporters())
				return len(h.frameworkIgnores) - len(base.frameworkIgnores) +
					len(h.frameworkPrepare) - len(base.frameworkPrepare) +
					len(h.frameworkRender) - len(base.frameworkRender) +
					len(h.frameworkLevels) - len(base.frameworkLevels)
			},
			want: 0,
		},
		{
			name: "GenericHelpersNilSafe",
			apply: func(h *Handler) {
				RenderFor[*statusErr](nil, func(RenderContext, *statusErr, *ErrorContext) bool { return true })
				RenderFor[*statusErr](h, nil)
				RenderStatus[*statusErr](nil, 400)
				ReportFor[*statusErr](nil, func(*statusErr, *ErrorContext) bool { return true })
				ReportFor[*statusErr](h, nil)
				MapFor[*statusErr](h, nil)
				MapIs(h, nil, func(err error) error { return err })
				Ignore[*statusErr](nil)
				IgnoreIs(h, nil)
				Unignore[*statusErr](nil)
				UnignoreIs(h, nil)
				LevelFor[*statusErr](nil, contract.LogLevelWarn)
				LevelIs(h, nil, contract.LogLevelWarn)
				ThrottleFor[*statusErr](nil, contract.Throttle{})
			},
			count: func(h *Handler) int {
				return len(h.renderRules) + len(h.reportRules) + len(h.mapRules) + len(h.ignoreRules) +
					len(h.unignoreRules) + len(h.levelRules) + len(h.throttleRules)
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler()
			tt.apply(h)
			if got := tt.count(h); got != tt.want {
				t.Errorf("rules = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestHandler_ReplacedMapRuleWins(t *testing.T) {
	h, _, _ := newTestHandler()
	MapIs(h, errSentinel, func(error) error { return NotFound() })
	MapIs(h, errSentinel, func(error) error { return Gone() })
	rc, w := newRC(http.MethodGet, "/x")
	h.HandleRequest(rc, errSentinel, nil)
	if w.Code != http.StatusGone {
		t.Errorf("status = %d, want 410 from the replacing rule", w.Code)
	}
}

func TestHandler_MapRuleNilKeepsOriginal(t *testing.T) {
	h, rep, _ := newTestHandler()
	MapFor[*statusErr](h, func(*statusErr) error { return nil })
	MapIs(h, errSentinel, func(err error) error { return nil })
	h.Report(&statusErr{code: 500}, nil)
	h.Report(errSentinel, nil)
	if rep.count() != 2 {
		t.Errorf("reports = %d, want 2", rep.count())
	}
	if _, err := rep.last(); !errors.Is(err, errSentinel) {
		t.Errorf("mapped to %v, want the original", err)
	}
}

func TestHandler_ErrorPageRendererReplaceable(t *testing.T) {
	h, _, _ := newTestHandler()
	h.SetErrorPageRenderer(&fakeErrorPage{ok: true, write: true})
	h.SetErrorPageRenderer(nil)
	rc, w := newRC(http.MethodGet, "/p", "X-Inertia", "true")
	h.HandleRequest(rc, NotFound(), nil)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 once the page renderer is removed", w.Code)
	}
}

func TestFakeHandler(t *testing.T) {
	f := NewFakeHandler()
	var eh contract.ErrorHandler = f

	rc, w := newRC(http.MethodGet, "/x")
	eh.HandleRequest(rc, NotFound(), nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	eh.Render(rc, errors.New("already written"), nil)
	eh.Report(nil, nil)
	eh.Render(rc, nil, nil)
	if code := eh.HandleConsole(nil, &exitErr{code: 4}); code != 4 {
		t.Errorf("console code = %d, want 4", code)
	}
	if code := eh.HandleConsole(nil, nil); code != 0 {
		t.Errorf("console code = %d, want 0", code)
	}
	if len(f.Reported) != 2 || len(f.Rendered) != 2 {
		t.Errorf("reported %d rendered %d, want 2 and 2", len(f.Reported), len(f.Rendered))
	}
	if !eh.ShouldReport(errSentinel) || eh.ShouldReport(nil) {
		t.Error("ShouldReport")
	}

	browser := httptest.NewRequest(http.MethodGet, "/x", nil)
	browser.Header.Set("Accept", "text/html")
	jsonReq := httptest.NewRequest(http.MethodGet, "/x", nil)
	jsonReq.Header.Set("Accept", "application/json")
	if eh.WantsJSON(browser, nil) || !eh.WantsJSON(jsonReq, nil) || eh.WantsJSON(nil, nil) {
		t.Error("WantsJSON before API settings")
	}

	eh.AddMapRule(contract.MapRule{})
	eh.AddRenderRule(contract.RenderRule{})
	eh.AddReportRule(contract.ReportRule{})
	eh.AddIgnoreRule(contract.IgnoreRule{})
	eh.AddLevelRule(contract.LevelRule{})
	eh.AddThrottleRule(contract.ThrottleRule{})
	eh.IgnoreIf(nil)
	eh.ContextUsing(nil)
	eh.JSONWhen(nil)
	eh.BeforeRender(nil)
	eh.SetErrorPageRenderer(nil)
	eh.AddReporter(nil)
	eh.SetReporters()
	eh.AddRenderer("json", nil)
	eh.SetDebug(true)
	eh.SetEnvironment("local")
	eh.SetAPIMode(true)
	eh.SetAPIPrefixes("/api")
	if !eh.IsDebug() || eh.GetEnvironment() != "local" || !eh.IsAPIMode() || eh.GetAPIPrefixes()[0] != "/api" {
		t.Error("fake settings not stored")
	}
	if !eh.WantsJSON(browser, nil) {
		t.Error("WantsJSON in API mode = false, want true")
	}
	eh.SetAPIMode(false)
	apiReq := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	apiReq.Header.Set("Accept", "text/html")
	if !eh.WantsJSON(apiReq, nil) || eh.WantsJSON(browser, nil) {
		t.Error("WantsJSON with an API prefix")
	}

	f.Reset()
	if len(f.ReportedErrors()) != 0 || len(f.RenderedErrors()) != 0 {
		t.Error("Reset did not clear")
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc, _ := newRC(http.MethodGet, "/x")
			eh.HandleRequest(rc, errSentinel, nil)
			_ = f.ReportedErrors()
		}()
	}
	wg.Wait()
	if len(f.ReportedErrors()) != 8 {
		t.Errorf("concurrent reports = %d, want 8", len(f.ReportedErrors()))
	}
}
