package cache

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	defaultManager *Manager
	once           sync.Once
)

// Initialize sets up the cache manager with configuration from environment
func Initialize() error {
	var initError error
	once.Do(func() {
		config := loadConfig()
		defaultManager = NewManager(config)
	})
	return initError
}

// loadConfig loads configuration from environment variables
func loadConfig() *Config {
	driver := os.Getenv("CACHE_DRIVER")
	if driver == "" {
		driver = DriverMemory
	}

	prefix := os.Getenv("CACHE_PREFIX")
	if prefix == "" {
		prefix = "velocity_cache"
	}

	config := &Config{
		Default: "default",
		Prefix:  prefix,
		Stores:  make(map[string]StoreConfig),
	}

	// Configure default store based on driver
	switch driver {
	case DriverFile:
		config.Stores["default"] = StoreConfig{
			Driver: DriverFile,
			Path:   os.Getenv("CACHE_PATH"),
		}
	case DriverRedis:
		port, _ := strconv.Atoi(os.Getenv("REDIS_PORT"))
		if port == 0 {
			port = 6379
		}
		database, _ := strconv.Atoi(os.Getenv("REDIS_DATABASE"))

		config.Stores["default"] = StoreConfig{
			Driver:   DriverRedis,
			Host:     getEnvOrDefault("REDIS_HOST", "127.0.0.1"),
			Port:     port,
			Password: os.Getenv("REDIS_PASSWORD"),
			Database: database,
		}
	default:
		config.Stores["default"] = StoreConfig{
			Driver: DriverMemory,
		}
	}

	// Add additional stores if configured
	// Example: CACHE_STORES=session:memory,api:redis
	if stores := os.Getenv("CACHE_STORES"); stores != "" {
		for _, store := range strings.Split(stores, ",") {
			parts := strings.Split(store, ":")
			if len(parts) == 2 {
				name := strings.TrimSpace(parts[0])
				driver := strings.TrimSpace(parts[1])
				config.Stores[name] = StoreConfig{
					Driver: driver,
				}
			}
		}
	}

	return config
}

// getEnvOrDefault returns environment variable or default value
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// GetManager returns the default cache manager
func GetManager() *Manager {
	if defaultManager == nil {
		Initialize()
	}
	return defaultManager
}

// Global convenience functions that use the default manager

// Get retrieves a value from the default cache
func Get(key string) (interface{}, bool) {
	return GetManager().Get(key)
}

// GetString retrieves a string value from the default cache
func GetString(key string) (string, bool) {
	return GetManager().GetString(key)
}

// Put stores a value in the default cache with TTL
func Put(key string, value interface{}, ttl time.Duration) error {
	return GetManager().Put(key, value, ttl)
}

// Forever stores a value in the default cache indefinitely
func Forever(key string, value interface{}) error {
	return GetManager().Forever(key, value)
}

// Forget removes a value from the default cache
func Forget(key string) error {
	return GetManager().Forget(key)
}

// Flush removes all values from the default cache
func Flush() error {
	return GetManager().Flush()
}

// Increment increments a numeric value in the default cache
func Increment(key string, value int64) (int64, error) {
	return GetManager().Increment(key, value)
}

// Decrement decrements a numeric value in the default cache
func Decrement(key string, value int64) (int64, error) {
	return GetManager().Decrement(key, value)
}

// Remember gets from cache or computes and stores
func Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error) {
	return GetManager().Remember(key, ttl, callback)
}

// RememberForever gets from cache or computes and stores forever
func RememberForever(key string, callback func() interface{}) (interface{}, error) {
	return GetManager().RememberForever(key, callback)
}

// Many retrieves multiple values from the default cache
func Many(keys []string) map[string]interface{} {
	return GetManager().Many(keys)
}

// PutMany stores multiple values in the default cache
func PutMany(items map[string]interface{}, ttl time.Duration) error {
	return GetManager().PutMany(items, ttl)
}

// Has checks if a key exists in the default cache
func Has(key string) bool {
	return GetManager().Has(key)
}

// GetStore returns a specific cache store
func GetStore(name string) (Store, error) {
	return GetManager().Store(name)
}
