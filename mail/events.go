package mail

import (
	"context"
	"encoding/json"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/eventmeta"
)

// MailSent is dispatched after an email is sent successfully
type MailSent struct {
	contract.EventMeta
	To       []string
	Subject  string
	Channel  string
	Duration time.Duration
}

// Name returns the event name
func (e *MailSent) Name() string {
	return "mail.completed"
}

// MailFailed is dispatched when an email fails to send
type MailFailed struct {
	contract.EventMeta
	To       []string
	Subject  string
	Channel  string
	Err      error
	Duration time.Duration
}

// Name returns the event name
func (e *MailFailed) Name() string {
	return "mail.failed"
}

// MarshalJSON encodes the event with Err as its text.
func (e MailFailed) MarshalJSON() ([]byte, error) {
	type fields MailFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *MailFailed) UnmarshalJSON(data []byte) error {
	type fields MailFailed
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

// dispatchMailSent dispatches a MailSent event
func dispatchMailSent(events *eventemit.Emitter, ctx context.Context, to []string, subject, channel string, duration time.Duration) {
	events.EmitBuilt(ctx, func() any {
		return &MailSent{
			EventMeta: eventmeta.Current(ctx),
			To:        to,
			Subject:   subject,
			Channel:   channel,
			Duration:  duration,
		}
	})
}

// dispatchMailFailed dispatches a MailFailed event
func dispatchMailFailed(events *eventemit.Emitter, ctx context.Context, to []string, subject, channel string, err error, duration time.Duration) {
	events.EmitBuilt(ctx, func() any {
		return &MailFailed{
			EventMeta: eventmeta.Current(ctx),
			To:        to,
			Subject:   subject,
			Channel:   channel,
			Err:       err,
			Duration:  duration,
		}
	})
}
