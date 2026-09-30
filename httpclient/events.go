package httpclient

import (
	"context"
	"encoding/json"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventmeta"
)

// RequestSent is dispatched after an HTTP request completes successfully
type RequestSent struct {
	contract.EventMeta
	Method       string
	URL          string
	StatusCode   int
	Duration     time.Duration
	RequestSize  int64
	ResponseSize int64
}

// Name returns the event name
func (e *RequestSent) Name() string {
	return "httpclient.request.completed"
}

// RequestFailed is dispatched when an HTTP request fails
type RequestFailed struct {
	contract.EventMeta
	Method   string
	URL      string
	Err      error
	Duration time.Duration
}

// Name returns the event name
func (e *RequestFailed) Name() string {
	return "httpclient.request.failed"
}

// MarshalJSON encodes the event with Err as its text.
func (e RequestFailed) MarshalJSON() ([]byte, error) {
	type fields RequestFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
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
	e.Err = eventmeta.TextError(v.Err)
	return nil
}

// dispatchRequestSent dispatches a RequestSent event. The event is built
// only when a dispatcher is installed.
func (c *Client) dispatchRequestSent(ctx context.Context, method, url string, statusCode int, duration time.Duration, requestSize, responseSize int64) {
	c.events.EmitBuilt(ctx, func() any {
		return &RequestSent{
			EventMeta:    eventmeta.Current(ctx),
			Method:       method,
			URL:          url,
			StatusCode:   statusCode,
			Duration:     duration,
			RequestSize:  requestSize,
			ResponseSize: responseSize,
		}
	})
}

// dispatchRequestFailed dispatches a RequestFailed event. The event is
// built only when a dispatcher is installed.
func (c *Client) dispatchRequestFailed(ctx context.Context, method, url string, err error, duration time.Duration) {
	c.events.EmitBuilt(ctx, func() any {
		return &RequestFailed{
			EventMeta: eventmeta.Current(ctx),
			Method:    method,
			URL:       url,
			Err:       err,
			Duration:  duration,
		}
	})
}
