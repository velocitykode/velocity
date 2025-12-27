package mail

import (
	"fmt"
	"os"
	"sync"

	"github.com/joho/godotenv"
)

var (
	driverFactories = make(map[string]func() (Mailer, error))
	driverMu        sync.RWMutex
)

var initOnce sync.Once

func init() {
	// Load .env file
	godotenv.Load()
}

// ensureInitialized lazily initializes the default mailer on first use
func ensureInitialized() {
	initOnce.Do(func() {
		if defaultMailer != nil {
			return // Already initialized
		}

		// Get mail driver from environment
		driver := os.Getenv("MAIL_DRIVER")
		if driver == "" {
			driver = "log" // Default to log driver for development
		}

		mailer, err := createDriver(driver)
		if err != nil {
			panic(fmt.Sprintf("Failed to initialize mail driver '%s': %v", driver, err))
		}
		defaultMailer = mailer
	})
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

// Reinitialize reinitializes the mail driver (useful after config changes)
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

// ReinitializeWithDriver reinitializes with a specific driver
func ReinitializeWithDriver(driver string) error {
	mailer, err := createDriver(driver)
	if err != nil {
		return err
	}

	defaultMailer = mailer
	return nil
}
