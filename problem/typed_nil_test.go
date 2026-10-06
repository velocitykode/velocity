package problem

import (
	"errors"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

type nilReporter struct{ Reporter }

type nilRenderer struct{ Renderer }

type nilRenderContext struct{ contract.RenderContext }

type nilErrorHandler struct{ contract.ErrorHandler }

// A typed nil is absence at the pipeline's entry points: a reporter or
// renderer that is one is not added, a render context that is one renders
// nothing, and the rule helpers ignore a handler that is one.
func TestTypedNilIsAbsence(t *testing.T) {
	var (
		reporter *nilReporter
		renderer *nilRenderer
		rc       *nilRenderContext
		handler  *nilErrorHandler
	)
	boom := errors.New("boom")

	h := NewHandler()
	before := len(h.reporters)
	h.AddReporter(reporter)
	if got := len(h.reporters); got != before {
		t.Errorf("AddReporter(typed nil) added a reporter: %d, was %d", got, before)
	}
	multi := &MultiReporter{}
	multi.AddReporter(reporter)
	multi.Report(boom, nil)

	h.AddRenderer("json", renderer)
	if r, ok := h.renderers["json"]; ok && r == Renderer(renderer) {
		t.Error("AddRenderer(typed nil) replaced the json renderer")
	}

	h.HandleRequest(rc, boom, nil)
	h.Render(rc, boom, nil)
	if h.RenderJSON(rc, boom, nil) {
		t.Error("RenderJSON(typed nil) = true, want false")
	}
	NewFakeHandler().Render(rc, boom, nil)

	RenderFor(handler, func(RenderContext, error, *ErrorContext) bool { return true })
	ReportFor(handler, func(error, *ErrorContext) bool { return true })
	Ignore[error](handler)
}
