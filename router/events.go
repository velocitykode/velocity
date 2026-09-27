package router

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// RequestStarted is dispatched when an HTTP request begins processing. Its
// At is when the router received the request.
type RequestStarted struct {
	contract.EventMeta
	Method string
	Path   string
	// RemoteAddr is the peer address the connection came from
	// (http.Request.RemoteAddr), a proxy's when one forwarded the request.
	RemoteAddr string
	UserAgent  string
	RequestID  string
}

// Name returns the event name
func (e *RequestStarted) Name() string {
	return "router.request.started"
}

// RequestRouted is dispatched after route matching completes
type RequestRouted struct {
	contract.EventMeta
	RequestID string
	Route     string            // Route pattern e.g. "/users/{id}"
	RouteName string            // Named route if any
	Params    map[string]string // Route parameters
	Matched   bool              // false for 404
}

// Name returns the event name
func (e *RequestRouted) Name() string {
	return "router.request.routed"
}

// RequestHandled is dispatched when an HTTP request completes successfully.
// Its At is when the request ended and Duration how long it took.
type RequestHandled struct {
	contract.EventMeta
	RequestID    string
	Method       string
	Path         string
	Route        string // Route pattern that was matched
	StatusCode   int
	BytesWritten int64
	Duration     time.Duration
}

// Name returns the event name
func (e *RequestHandled) Name() string {
	return "router.request.completed"
}

// RequestFailed is dispatched when an HTTP request fails, decided once the
// router's error boundary has answered the error: a recovered panic
// (including one the Timeout middleware forwarded, and one its handler
// goroutine recovered after the 503 went out, which dispatches this event
// after the request's RequestHandled), an answer to the error with status
// 500 or above, or an error that names no status below 500 when nothing
// answered it. The answer is the response the boundary wrote, or the one
// a middleware wrote before returning contract.Handled; its status
// decides, not the error as the handler returned it, so an error the
// installed error handler answers below 500 (a not-found sentinel it maps
// to 404, an application map rule) is a response, not a failure, and
// dispatches nothing. A response the handler committed itself before
// returning a plain error is not an answer to it: the error decides, as
// when nothing was written. A contract.Handled value dispatches its cause
// under the same rule, and a bare contract.ErrResponseWritten dispatches
// nothing, except inside the value of a recovered panic, which always
// dispatches. Its At is when the failure was decided and Duration how long the request had run
// by then (zero for the panic a Timeout handler goroutine recovers after
// the 503, which the router reports without the request's start).
type RequestFailed struct {
	contract.EventMeta
	RequestID string
	Method    string
	Path      string
	Err       error
	Stack     string // Stack trace if panic recovered
	Recovered bool   // true if recovered from panic
	Duration  time.Duration
}

// Name returns the event name
func (e *RequestFailed) Name() string {
	return "router.request.failed"
}

// MarshalJSON encodes the event with Err as its text.
func (e RequestFailed) MarshalJSON() ([]byte, error) {
	type fields RequestFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), errorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *RequestFailed) UnmarshalJSON(data []byte) error {
	type fields RequestFailed
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = textError(v.Err)
	return nil
}

// errorText returns err's text, or "" for a nil error.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// textError returns an error with text, or nil for "".
func textError(text string) error {
	if text == "" {
		return nil
	}
	return errors.New(text)
}
