package mail

import (
	"fmt"
	"strings"
	"sync"

	"github.com/velocitykode/velocity/contract"
)

// allowedPostmarkStreams enumerates the Message Streams recognised by default.
// Callers that use custom broadcast streams can configure AllowedPostmarkStreams
// via ConfigureAllowedPostmarkStreams.
var (
	allowedPostmarkStreams   = map[string]struct{}{"outbound": {}, "broadcast": {}, "transactional": {}, "inbound": {}}
	allowedPostmarkStreamsMu sync.RWMutex
)

// ConfigureAllowedPostmarkStreams replaces the set of allowed Postmark message
// streams. Names are lower-cased. Passing an empty slice restores the defaults.
func ConfigureAllowedPostmarkStreams(streams []string) {
	allowedPostmarkStreamsMu.Lock()
	defer allowedPostmarkStreamsMu.Unlock()
	if len(streams) == 0 {
		allowedPostmarkStreams = map[string]struct{}{"outbound": {}, "broadcast": {}, "transactional": {}, "inbound": {}}
		return
	}
	next := make(map[string]struct{}, len(streams))
	for _, s := range streams {
		next[strings.ToLower(strings.TrimSpace(s))] = struct{}{}
	}
	allowedPostmarkStreams = next
}

// IsAllowedPostmarkStream reports whether a stream name passes the allowlist.
func IsAllowedPostmarkStream(name string) bool {
	allowedPostmarkStreamsMu.RLock()
	defer allowedPostmarkStreamsMu.RUnlock()
	_, ok := allowedPostmarkStreams[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

var (
	driverFactories = make(map[string]func(MailConfig) (Mailer, error))
	driverMu        sync.RWMutex
)

// MailConfig holds configuration for creating a mailer.
type MailConfig struct {
	// Driver is the mail driver to use (e.g. "log", "postmark", "mailgun", "local").
	Driver      string
	FromAddress string
	FromName    string

	Mailgun  MailgunConfig
	Postmark PostmarkConfig
	Local    LocalConfig
}

// MailgunConfig holds Mailgun-specific configuration.
type MailgunConfig struct {
	Domain            string
	Secret            string // SENSITIVE: do not log
	Endpoint          string
	WebhookSigningKey string // SENSITIVE: do not log
}

// PostmarkConfig holds Postmark-specific configuration.
type PostmarkConfig struct {
	Token         string // SENSITIVE: do not log
	MessageStream string
}

// LocalConfig holds local SMTP/sendmail configuration.
type LocalConfig struct {
	Host         string
	Port         string
	Username     string // SENSITIVE: do not log
	Password     string // SENSITIVE: do not log
	Encryption   string
	SendmailPath string
}

// NewMailer creates a new Mailer from the given configuration.
// Drivers must be registered via RegisterDriver before calling this function.
func NewMailer(config MailConfig) (Mailer, error) {
	driver := config.Driver
	if driver == "" {
		driver = "log"
	}

	driverMu.RLock()
	factory, exists := driverFactories[driver]
	driverMu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unsupported mail driver: %s (driver not registered)", driver)
	}

	return factory(config)
}

// RegisterDriver allows drivers to register themselves.
// Panics with *contract.RegistrationError if factory is nil or the driver name
// is already registered.
func RegisterDriver(name string, factory func(MailConfig) (Mailer, error)) {
	if factory == nil {
		panic(contract.NewRegistrationError("mail", fmt.Sprintf("nil factory for %q", name)))
	}
	driverMu.Lock()
	defer driverMu.Unlock()
	if _, exists := driverFactories[name]; exists {
		panic(contract.NewRegistrationError("mail", fmt.Sprintf("driver %q already registered", name)))
	}
	driverFactories[name] = factory
}
