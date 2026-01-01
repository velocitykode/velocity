package orm

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/velocitykode/velocity/pkg/orm/drivers"
)

// init automatically initializes the ORM from environment variables
func init() {
	// Try to load .env file if it exists
	godotenv.Load()

	// Register built-in drivers
	RegisterDriver("sqlite", drivers.NewSQLiteDriver)
	RegisterDriver("sqlite3", drivers.NewSQLiteDriver) // Alias for compatibility
	RegisterDriver("postgres", drivers.NewPostgresDriver)
	RegisterDriver("mysql", drivers.NewMySQLDriver)

	// Check if auto-initialization is disabled
	if os.Getenv("ORM_AUTO_INIT") == "false" {
		return
	}

	// Get database connection from environment
	dbConnection := os.Getenv("DB_CONNECTION")
	if dbConnection == "" {
		// No database configuration, skip initialization
		return
	}

	// Build configuration from environment variables
	config := make(map[string]any)

	// Basic connection settings
	config["driver"] = dbConnection
	config["host"] = getEnvOrDefault("DB_HOST", "127.0.0.1")
	config["port"] = getEnvOrDefault("DB_PORT", getDefaultPortForDriver(dbConnection))
	config["database"] = os.Getenv("DB_DATABASE")
	config["username"] = os.Getenv("DB_USERNAME")
	config["password"] = os.Getenv("DB_PASSWORD")

	// Optional settings
	if charset := os.Getenv("DB_CHARSET"); charset != "" {
		config["charset"] = charset
	}

	if collation := os.Getenv("DB_COLLATION"); collation != "" {
		config["collation"] = collation
	}

	if prefix := os.Getenv("DB_PREFIX"); prefix != "" {
		config["prefix"] = prefix
	}

	if schema := os.Getenv("DB_SCHEMA"); schema != "" {
		config["schema"] = schema
	}

	if sslMode := os.Getenv("DB_SSL_MODE"); sslMode != "" {
		config["ssl_mode"] = sslMode
	}

	if timezone := os.Getenv("DB_TIMEZONE"); timezone != "" {
		config["timezone"] = timezone
	}

	// Connection pool settings
	if maxIdle := os.Getenv("DB_MAX_IDLE_CONNS"); maxIdle != "" {
		if v, err := strconv.Atoi(maxIdle); err == nil {
			config["max_idle_conns"] = v
		}
	}

	if maxOpen := os.Getenv("DB_MAX_OPEN_CONNS"); maxOpen != "" {
		if v, err := strconv.Atoi(maxOpen); err == nil {
			config["max_open_conns"] = v
		}
	}

	if maxLifetime := os.Getenv("DB_CONN_MAX_LIFETIME"); maxLifetime != "" {
		if v, err := strconv.Atoi(maxLifetime); err == nil {
			config["conn_max_lifetime"] = v
		}
	}

	// Logging settings
	if logQueries := os.Getenv("DB_LOG_QUERIES"); logQueries != "" {
		config["log_queries"] = logQueries == "true"
	}

	if slowThreshold := os.Getenv("DB_SLOW_QUERY_THRESHOLD"); slowThreshold != "" {
		config["slow_query_threshold"] = slowThreshold
	}

	// Initialize the ORM
	if err := Init(dbConnection, config); err != nil {
		// Log error but don't panic
		fmt.Fprintf(os.Stderr, "Failed to initialize ORM: %v\n", err)
	}
}

// InitFromEnv manually initializes the ORM from environment variables
func InitFromEnv() error {
	// Get database connection from environment
	dbConnection := os.Getenv("DB_CONNECTION")
	if dbConnection == "" {
		return fmt.Errorf("DB_CONNECTION not set in environment")
	}

	// Build configuration
	config := make(map[string]any)
	config["driver"] = dbConnection
	config["host"] = getEnvOrDefault("DB_HOST", "127.0.0.1")
	config["port"] = getEnvOrDefault("DB_PORT", getDefaultPortForDriver(dbConnection))
	config["database"] = os.Getenv("DB_DATABASE")
	config["username"] = os.Getenv("DB_USERNAME")
	config["password"] = os.Getenv("DB_PASSWORD")

	// Parse numeric settings
	if maxIdle := os.Getenv("DB_MAX_IDLE_CONNS"); maxIdle != "" {
		if v, err := strconv.Atoi(maxIdle); err == nil {
			config["max_idle_conns"] = v
		}
	}

	if maxOpen := os.Getenv("DB_MAX_OPEN_CONNS"); maxOpen != "" {
		if v, err := strconv.Atoi(maxOpen); err == nil {
			config["max_open_conns"] = v
		}
	}

	if maxLifetime := os.Getenv("DB_CONN_MAX_LIFETIME"); maxLifetime != "" {
		if v, err := strconv.Atoi(maxLifetime); err == nil {
			config["conn_max_lifetime"] = v
		}
	}

	// Parse boolean settings
	if logQueries := os.Getenv("DB_LOG_QUERIES"); logQueries != "" {
		config["log_queries"] = logQueries == "true"
	}

	if slowThreshold := os.Getenv("DB_SLOW_QUERY_THRESHOLD"); slowThreshold != "" {
		if duration, err := time.ParseDuration(slowThreshold); err == nil {
			config["slow_query_threshold"] = duration
		}
	}

	return Init(dbConnection, config)
}

// getEnvOrDefault gets environment variable or returns default value
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getDefaultPortForDriver returns the default port for a database driver
func getDefaultPortForDriver(driver string) string {
	switch driver {
	case "mysql":
		return "3306"
	case "postgres":
		return "5432"
	case "sqlite", "sqlite3":
		return ""
	default:
		return ""
	}
}
