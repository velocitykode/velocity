package problem

import (
	"io"
	"net/http"
	"sync"

	"github.com/velocitykode/velocity/contract"
)

// Verify *FakeHandler implements contract.ErrorHandler at compile time.
var _ contract.ErrorHandler = (*FakeHandler)(nil)

// FakeHandler is a contract.ErrorHandler for consumer tests. It records
// every error passed to Report, Render, HandleRequest and HandleConsole,
// applies no rules, and renders only the resolved status line. It is safe
// for concurrent use; read Reported and Rendered after the code under test
// has finished, or through the snapshot methods while it runs.
type FakeHandler struct {
	mu sync.Mutex

	// Reported holds every error reported, in call order.
	Reported []error
	// Rendered holds every error rendered, in call order.
	Rendered []error

	debug       bool
	environment string
	apiMode     bool
	apiPrefixes []string
}

// NewFakeHandler returns an empty FakeHandler.
func NewFakeHandler() *FakeHandler {
	return &FakeHandler{}
}

// Reset clears the recorded errors.
func (f *FakeHandler) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Reported = nil
	f.Rendered = nil
}

// ReportedErrors returns a copy of the recorded reports.
func (f *FakeHandler) ReportedErrors() []error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]error(nil), f.Reported...)
}

// RenderedErrors returns a copy of the recorded renders.
func (f *FakeHandler) RenderedErrors() []error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]error(nil), f.Rendered...)
}

// HandleRequest records err as reported and rendered and writes its
// resolved status.
func (f *FakeHandler) HandleRequest(rc RenderContext, err error, ctx *ErrorContext) {
	f.Report(err, ctx)
	f.Render(rc, err, ctx)
}

// HandleConsole records err as reported and returns its exit code.
func (f *FakeHandler) HandleConsole(_ io.Writer, err error) int {
	if err == nil {
		return 0
	}
	f.Report(err, nil)
	return exitCode(err)
}

// Report records err.
func (f *FakeHandler) Report(err error, _ *ErrorContext) {
	if err == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Reported = append(f.Reported, err)
}

// Render records err and writes its resolved status when nothing was
// written. As in Handler.Render, an err marking the response written
// (contract.ErrResponseWritten or a contract.Handled value) outside the
// value of a recovered panic is recorded but writes nothing.
func (f *FakeHandler) Render(rc RenderContext, err error, ctx *ErrorContext) {
	if err == nil {
		return
	}
	f.mu.Lock()
	f.Rendered = append(f.Rendered, err)
	f.mu.Unlock()
	if outsidePanic(err, ctx, contract.IsResponseWritten) {
		return
	}
	if rc != nil && !rc.Written() {
		status, _, _ := contract.StatusOf(err)
		rc.WriteHeader(status)
	}
}

// ShouldReport reports true for any non-nil err.
func (f *FakeHandler) ShouldReport(err error) bool { return err != nil }

// AddMapRule is a no-op.
func (f *FakeHandler) AddMapRule(contract.MapRule) {}

// AddRenderRule is a no-op.
func (f *FakeHandler) AddRenderRule(contract.RenderRule) {}

// AddReportRule is a no-op.
func (f *FakeHandler) AddReportRule(contract.ReportRule) {}

// AddIgnoreRule is a no-op.
func (f *FakeHandler) AddIgnoreRule(contract.IgnoreRule) {}

// AddLevelRule is a no-op.
func (f *FakeHandler) AddLevelRule(contract.LevelRule) {}

// AddThrottleRule is a no-op.
func (f *FakeHandler) AddThrottleRule(contract.ThrottleRule) {}

// IgnoreIf is a no-op.
func (f *FakeHandler) IgnoreIf(func(error, *ErrorContext) bool) {}

// ContextUsing is a no-op.
func (f *FakeHandler) ContextUsing(func(error, *ErrorContext) map[string]any) {}

// JSONWhen is a no-op.
func (f *FakeHandler) JSONWhen(func(*http.Request, error) bool) {}

// WantsJSON reports true in API mode, for a path under an API prefix
// (matched as Handler.SetAPIPrefixes describes), or when
// contract.WantsJSON holds for r.
func (f *FakeHandler) WantsJSON(r *http.Request, _ error) bool {
	f.mu.Lock()
	apiMode, prefixes := f.apiMode, f.apiPrefixes
	f.mu.Unlock()
	if apiMode {
		return true
	}
	if r == nil {
		return false
	}
	if r.URL != nil {
		for _, prefix := range prefixes {
			if underAPIPrefix(r.URL.Path, prefix) {
				return true
			}
		}
	}
	return contract.WantsJSON(r)
}

// BeforeRender is a no-op.
func (f *FakeHandler) BeforeRender(func(RenderContext, error, int) int) {}

// SetErrorPageRenderer is a no-op.
func (f *FakeHandler) SetErrorPageRenderer(contract.ErrorPageRenderer) {}

// AddReporter is a no-op.
func (f *FakeHandler) AddReporter(Reporter) {}

// SetReporters is a no-op.
func (f *FakeHandler) SetReporters(...Reporter) {}

// AddRenderer is a no-op.
func (f *FakeHandler) AddRenderer(string, Renderer) {}

// SetDebug stores the debug flag.
func (f *FakeHandler) SetDebug(debug bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.debug = debug
}

// IsDebug returns the stored debug flag.
func (f *FakeHandler) IsDebug() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.debug
}

// SetEnvironment stores the environment name.
func (f *FakeHandler) SetEnvironment(env string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.environment = env
}

// GetEnvironment returns the stored environment name.
func (f *FakeHandler) GetEnvironment() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.environment
}

// SetAPIMode stores the API mode flag.
func (f *FakeHandler) SetAPIMode(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiMode = enabled
}

// IsAPIMode returns the stored API mode flag.
func (f *FakeHandler) IsAPIMode() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apiMode
}

// SetAPIPrefixes stores the API prefixes.
func (f *FakeHandler) SetAPIPrefixes(prefixes ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiPrefixes = append([]string(nil), prefixes...)
}

// GetAPIPrefixes returns a copy of the stored API prefixes.
func (f *FakeHandler) GetAPIPrefixes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.apiPrefixes...)
}
