package csrf

import (
	"errors"
	"net/http"

	"github.com/velocitykode/velocity/contract"
)

// TokenMismatchError is the error Protect returns when an unsafe request
// fails CSRF protection. It answers 419 with Message as the client-facing
// text (contract.MessageError), is never reported, and unwraps to its
// Reason (ErrTokenMissing when Reason is unset), so errors.Is and
// errors.As reach the actual cause however the rejection is wrapped: an
// invalid token matches ErrTokenInvalid and not ErrTokenMissing.
type TokenMismatchError struct {
	// Reason is why the request was rejected: ErrTokenMissing,
	// ErrTokenInvalid, ErrNoSession, ErrFormBodyTooLarge, or the error the
	// SessionIDResolver returned. It is the error Config.ErrorHandler
	// receives.
	Reason error
	// Message is the client-facing text: the Config.ErrorMessage of the
	// instance that rejected the request. Empty means the status title.
	Message string

	// handler is the Config.ErrorHandler of the instance that rejected the
	// request, nil when that instance has none.
	handler func(http.ResponseWriter, *http.Request, error)
}

// Error returns the rejection with its reason. It reaches logs only; the
// client-facing message comes from the error pipeline.
func (e *TokenMismatchError) Error() string {
	if e.Reason == nil {
		return "velocity/csrf: token mismatch"
	}
	return "velocity/csrf: token mismatch: " + e.Reason.Error()
}

// StatusCode returns 419.
func (e *TokenMismatchError) StatusCode() int { return contract.StatusTokenMismatch }

// ClientMessage returns Message.
func (e *TokenMismatchError) ClientMessage() string { return e.Message }

// ShouldReport returns false: a rejected token is a client outcome, not a
// failure.
func (e *TokenMismatchError) ShouldReport() bool { return false }

// Unwrap returns Reason, or ErrTokenMissing when Reason is unset.
func (e *TokenMismatchError) Unwrap() error { return e.reason() }

// reason returns Reason, or ErrTokenMissing when it is unset.
func (e *TokenMismatchError) reason() error {
	if e.Reason == nil {
		return ErrTokenMissing
	}
	return e.Reason
}

// RenderTokenMismatch is the framework's default render rule for a
// *TokenMismatchError in err's chain. When the CSRF instance that rejected
// the request has a Config.ErrorHandler, that handler answers with the
// response writer, the request and the rejection reason, and
// RenderTokenMismatch returns true. Otherwise it writes nothing and returns
// false, so the error pipeline renders the 419 through content negotiation.
func RenderTokenMismatch(rc contract.RenderContext, err error, _ *contract.ErrorContext) bool {
	var tm *TokenMismatchError
	if rc == nil || !errors.As(err, &tm) || tm.handler == nil {
		return false
	}
	tm.handler(rc.Writer(), rc.Request(), tm.reason())
	return true
}
