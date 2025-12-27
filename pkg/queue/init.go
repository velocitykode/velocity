package queue

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

func init() {
	// Try to load .env file
	projectRoot := findProjectRoot()
	if projectRoot != "" {
		envPath := filepath.Join(projectRoot, ".env")
		_ = godotenv.Load(envPath)
	}

	// Read driver from environment, default to memory for safety during init
	driver := strings.ToLower(os.Getenv("QUEUE_DRIVER"))
	if driver == "" {
		// Default to memory driver for development and initial boot
		driver = "memory"
		log.Printf("QUEUE_DRIVER not set, defaulting to memory driver. Call queue.Reinitialize() after database is ready.")
	}

	// Initialize based on driver
	var d Driver
	var err error

	switch driver {
	case "redis":
		d, err = NewRedisQueue()
		if err != nil {
			log.Printf("Failed to initialize Redis queue: %v, falling back to memory", err)
			d = NewMemoryQueue()
		}
	case "database":
		d, err = NewDatabaseQueue()
		if err != nil {
			log.Printf("Failed to initialize database queue: %v, falling back to memory", err)
			d = NewMemoryQueue()
		}
	case "memory":
		d = NewMemoryQueue()
	default:
		log.Printf("Unknown queue driver: %s, using memory", driver)
		d = NewMemoryQueue()
	}

	SetDefault(d)
}

// findProjectRoot searches for project root by looking for go.mod
func findProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return ""
}

// NewRedisQueue creates a Redis queue from environment config
func NewRedisQueue() (Driver, error) {
	host := os.Getenv("QUEUE_REDIS_HOST")
	if host == "" {
		host = "localhost"
	}

	port := os.Getenv("QUEUE_REDIS_PORT")
	if port == "" {
		port = "6379"
	}

	db := os.Getenv("QUEUE_REDIS_DB")
	if db == "" {
		db = "0"
	}

	password := os.Getenv("QUEUE_REDIS_PASSWORD")

	config := RedisConfig{
		Host:     host,
		Port:     port,
		Password: password,
		DB:       db,
	}

	return NewRedisDriver(config)
}

// NewDatabaseQueue creates a database queue from environment config
func NewDatabaseQueue() (Driver, error) {
	// Use the database driver with ORM
	return NewDatabaseDriver(), nil
}

// NewMemoryQueue creates an in-memory queue
func NewMemoryQueue() Driver {
	return NewMemoryDriver()
}