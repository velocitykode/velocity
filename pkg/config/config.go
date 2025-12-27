package config

import "os"

// Get retrieves an environment variable with an optional fallback value
func Get(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
