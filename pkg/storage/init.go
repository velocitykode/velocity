package storage

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

// init no longer auto-initializes from environment.
// Use NewManager(config) to create an instance, or call InitFromEnv() explicitly.
func init() {
	// Intentionally empty — env loading moved to InitFromEnv().
}

// InitFromEnv configures the global storage manager from environment variables.
func InitFromEnv() error {
	initializeFromEnv()
	return nil
}

// initializeFromEnv configures storage from environment variables
func initializeFromEnv() {
	// Try to load .env file
	envPath := findEnvFile()
	if envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			log.Printf("Warning: Failed to load .env file: %v", err)
		}
	}

	// Read storage configuration from environment
	defaultDisk := os.Getenv("FILESYSTEM_DISK")
	if defaultDisk == "" {
		defaultDisk = "local"
	}

	// Create config for default disk
	config := Config{
		Default: defaultDisk,
		Disks:   make(map[string]DiskConfig),
	}

	// Configure local disk
	localRoot := os.Getenv("FILESYSTEM_LOCAL_ROOT")
	if localRoot == "" {
		localRoot = "storage/app"
	}
	localURL := os.Getenv("FILESYSTEM_LOCAL_URL")
	localVisibility := os.Getenv("FILESYSTEM_LOCAL_VISIBILITY")
	if localVisibility == "" {
		localVisibility = "private"
	}

	config.Disks["local"] = DiskConfig{
		Driver:     "local",
		Root:       localRoot,
		URL:        localURL,
		Visibility: localVisibility,
	}

	// Configure public disk (typically for publicly accessible files)
	publicRoot := os.Getenv("FILESYSTEM_PUBLIC_ROOT")
	if publicRoot == "" {
		publicRoot = "storage/app/public"
	}
	publicURL := os.Getenv("FILESYSTEM_PUBLIC_URL")
	if publicURL == "" {
		publicURL = os.Getenv("APP_URL")
		if publicURL != "" {
			publicURL = strings.TrimSuffix(publicURL, "/") + "/storage"
		}
	}

	config.Disks["public"] = DiskConfig{
		Driver:     "local",
		Root:       publicRoot,
		URL:        publicURL,
		Visibility: "public",
	}

	// Configure S3 disk if credentials are present
	s3Key := os.Getenv("AWS_ACCESS_KEY_ID")
	s3Secret := os.Getenv("AWS_SECRET_ACCESS_KEY")
	s3Region := os.Getenv("AWS_DEFAULT_REGION")
	s3Bucket := os.Getenv("AWS_BUCKET")

	if s3Key != "" && s3Secret != "" && s3Region != "" && s3Bucket != "" {
		s3URL := os.Getenv("AWS_URL")
		s3Visibility := os.Getenv("AWS_VISIBILITY")
		if s3Visibility == "" {
			s3Visibility = "private"
		}

		config.Disks["s3"] = DiskConfig{
			Driver:     "s3",
			Key:        s3Key,
			Secret:     s3Secret,
			Region:     s3Region,
			Bucket:     s3Bucket,
			URL:        s3URL,
			Visibility: s3Visibility,
		}

		// If S3 is configured and set as default
		if defaultDisk == "s3" {
			config.Default = "s3"
		}
	}

	// Configure test/memory disk if in test environment
	if os.Getenv("APP_ENV") == "testing" || os.Getenv("GO_ENV") == "test" {
		config.Disks["testing"] = DiskConfig{
			Driver:  "memory",
			MaxSize: 10 * 1024 * 1024, // 10MB for testing
		}
		// Use memory driver for testing
		config.Default = "testing"
	}

	// Initialize the global manager
	if err := Configure(config); err != nil {
		log.Printf("Warning: Failed to initialize storage: %v", err)
		// Fall back to memory driver
		fallbackConfig := Config{
			Default: "memory",
			Disks: map[string]DiskConfig{
				"memory": {
					Driver:  "memory",
					MaxSize: 100 * 1024 * 1024, // 100MB
				},
			},
		}
		if err := Configure(fallbackConfig); err != nil {
			log.Printf("Error: Failed to initialize fallback storage: %v", err)
		}
	}
}

// findEnvFile searches for .env file in current and parent directories
func findEnvFile() string {
	// Start from current working directory
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Search up to 5 levels up
	for i := 0; i < 5; i++ {
		envPath := filepath.Join(dir, ".env")
		if _, err := os.Stat(envPath); err == nil {
			return envPath
		}

		// Move to parent directory
		parent := filepath.Dir(dir)
		if parent == dir {
			break // Reached root
		}
		dir = parent
	}

	return ""
}

// ReloadFromEnv reloads storage configuration from environment.
func ReloadFromEnv() {
	initializeFromEnv()
}
