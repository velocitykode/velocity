package orm

import (
	"os"
	"strconv"
	"time"

	"github.com/velocitykode/velocity/pkg/orm/drivers"
)

// init registers built-in database drivers.
// No connections are opened — use NewManager() or Init() to connect.
func init() {
	// Register built-in drivers (pure data, no I/O)
	RegisterDriver("sqlite", drivers.NewSQLiteDriver)
	RegisterDriver("sqlite3", drivers.NewSQLiteDriver) // Alias for compatibility
	RegisterDriver("postgres", drivers.NewPostgresDriver)
	RegisterDriver("mysql", drivers.NewMySQLDriver)
}

// InitFromEnv manually initializes the ORM from environment variables.
func InitFromEnv() error {
	// Get database connection from environment
	dbConnection := os.Getenv("DB_CONNECTION")
	if dbConnection == "" {
		return nil
	}

	// Build configuration
	config := make(map[string]any)
	config["driver"] = dbConnection
	config["host"] = getEnvOrDefault("DB_HOST", "127.0.0.1")
	config["port"] = getEnvOrDefault("DB_PORT", getDefaultPortForDriver(dbConnection))
	config["database"] = os.Getenv("DB_DATABASE")
	config["username"] = os.Getenv("DB_USERNAME")
	config["password"] = os.Getenv("DB_PASSWORD")

	// Optional settings
	if sslMode := os.Getenv("DB_SSL_MODE"); sslMode != "" {
		config["ssl_mode"] = sslMode
	}

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
