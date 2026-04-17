package mail

import "errors"

var (
	ErrDriverNotConfigured = errors.New("velocity/mail: driver not configured")
	ErrChannelNotFound     = errors.New("velocity/mail: channel not found")
)
