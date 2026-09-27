package bond

import (
	"encoding/json"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventmeta"
)

// Event name constants for the framework event dispatcher. Listeners
// can subscribe by type (e.g. via events.Listen) or by this name.
const EventSSRRenderFailed = "bond.ssr.render.failed"

// SSRErrorType classifies SSR render failures. Mirrors the categories
// emitted by the broader Inertia ecosystem so listeners/metrics built
// against the standard enum can reuse the same labels.
type SSRErrorType string

const (
	SSRErrorBrowserAPI          SSRErrorType = "browser-api"
	SSRErrorComponentResolution SSRErrorType = "component-resolution"
	SSRErrorRender              SSRErrorType = "render"
	SSRErrorConnection          SSRErrorType = "connection"
	SSRErrorUnknown             SSRErrorType = "unknown"
)

// ParseSSRErrorType normalizes a raw string (typically from the SSR
// server's error payload) into a typed SSRErrorType. An empty or
// unrecognized value maps to SSRErrorUnknown.
func ParseSSRErrorType(s string) SSRErrorType {
	switch SSRErrorType(s) {
	case SSRErrorBrowserAPI, SSRErrorComponentResolution, SSRErrorRender, SSRErrorConnection:
		return SSRErrorType(s)
	default:
		return SSRErrorUnknown
	}
}

// SSRRenderFailed is dispatched whenever the SSR gateway fails to
// render a page and the renderer falls back to CSR. The payload mirrors
// the Inertia SsrRenderFailed event shape so listeners can emit the
// same metrics/log structure across stacks.
type SSRRenderFailed struct {
	contract.EventMeta
	// Component is the Inertia component name that failed to render.
	Component string `json:"component"`
	// URL is the page URL that was being rendered.
	URL string `json:"url"`
	// Err is the failure: the transport, read or decode error the gateway
	// met, or for an answer outside 2xx an error with the message the SSR
	// server returned. Its JSON form ("error") is its text.
	Err error `json:"error"`
	// Type categorizes the failure. "connection" for transport issues,
	// "render"/"component-resolution"/"browser-api" when the SSR server
	// reports a structured error, "unknown" as a catch-all.
	Type SSRErrorType `json:"type"`
	// Hint is an optional remediation hint supplied by the SSR server
	// (e.g. "make sure useBrowserAPI is client-only").
	Hint string `json:"hint,omitempty"`
	// BrowserAPI names the API that caused the failure when the SSR
	// error type is browser-api (e.g. "document", "window").
	BrowserAPI string `json:"browserApi,omitempty"`
	// Stack is the JS stack trace from the SSR server if provided.
	Stack string `json:"stack,omitempty"`
	// SourceLocation is a file:line hint supplied by the SSR server.
	SourceLocation string `json:"sourceLocation,omitempty"`
}

// Name returns the dispatcher name for this event.
func (SSRRenderFailed) Name() string { return EventSSRRenderFailed }

// MarshalJSON encodes the event with Err as its text.
func (e SSRRenderFailed) MarshalJSON() ([]byte, error) {
	type fields SSRRenderFailed
	return json.Marshal(struct {
		fields
		Err string `json:"error"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *SSRRenderFailed) UnmarshalJSON(data []byte) error {
	type fields SSRRenderFailed
	v := struct {
		*fields
		Err string `json:"error"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = eventmeta.TextError(v.Err)
	return nil
}
