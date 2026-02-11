package mail

import (
	"fmt"
	"os"
	"sync"
)

var (
	driverFactories = make(map[string]func() (Mailer, error))
	driverMu        sync.RWMutex
)

// MailConfig holds configuration for creating a mailer.
type MailConfig struct {
	// Driver is the mail driver to use (e.g. "log", "postmark", "mailgun").
	Driver string
}

// NewMailer creates a new Mailer from the given configuration.
// Drivers must be registered via RegisterDriver before calling this function.
func NewMailer(config MailConfig) (Mailer, error) {
	driver := config.Driver
	if driver == "" {
		driver = "log"
	}
	return createDriver(driver)
}

// init initializes the mail package.
// Use NewMailer() to create mailer instances explicitly.
func init() {
	// No-op: global singleton is no longer eagerly initialized.
	// Driver registration still happens via RegisterDriver() calls in driver packages.
}

// RegisterDriver allows drivers to register themselves
func RegisterDriver(name string, factory func() (Mailer, error)) {
	driverMu.Lock()
	defer driverMu.Unlock()
	driverFactories[name] = factory
}

// createDriver creates a mail driver based on the driver name
func createDriver(driver string) (Mailer, error) {
	driverMu.RLock()
	factory, exists := driverFactories[driver]
	driverMu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unsupported mail driver: %s (driver not registered)", driver)
	}

	return factory()
}

// Reinitialize reinitializes the mail driver (useful after config changes).
func Reinitialize() error {
	driver := os.Getenv("MAIL_DRIVER")
	if driver == "" {
		driver = "log"
	}

	mailer, err := createDriver(driver)
	if err != nil {
		return err
	}

	defaultMailer = mailer
	return nil
}

// ReinitializeWithDriver reinitializes with a specific driver.
func ReinitializeWithDriver(driver string) error {
	mailer, err := createDriver(driver)
	if err != nil {
		return err
	}

	defaultMailer = mailer
	return nil
}
