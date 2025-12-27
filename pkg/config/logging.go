package config

import (
	"os"
	"strconv"
	"strings"
)

// LoggingConfig defines the logging configuration structure
type LoggingConfig struct {
	Default  string                    `json:"default"`
	Channels map[string]ChannelConfig  `json:"channels"`
}

// ChannelConfig defines configuration for a specific log channel
type ChannelConfig struct {
	Driver     string                 `json:"driver"`
	Level      string                 `json:"level"`
	Path       string                 `json:"path,omitempty"`
	MaxSize    int                    `json:"max_size,omitempty"`    // MB
	MaxAge     int                    `json:"max_age,omitempty"`     // days
	MaxBackups int                    `json:"max_backups,omitempty"` // number of old files
	Format     string                 `json:"format,omitempty"`      // json, text
	Options    map[string]interface{} `json:"options,omitempty"`     // driver-specific options
}

// GetLoggingConfig returns the default logging configuration
func GetLoggingConfig() LoggingConfig {
	return LoggingConfig{
		Default: Env("LOG_CHANNEL", "stack"),
		Channels: map[string]ChannelConfig{
			"stack": {
				Driver: "stack",
				Level:  Env("LOG_LEVEL", "debug"),
				Options: map[string]interface{}{
					"channels": []string{"single", "daily"},
				},
			},
			"single": {
				Driver:  "file",
				Level:   Env("LOG_LEVEL", "debug"),
				Path:    Env("LOG_PATH", "./storage/logs/velocity.log"),
				Format:  Env("LOG_FORMAT", "text"),
				MaxSize: EnvInt("LOG_MAX_SIZE", 100),
			},
			"daily": {
				Driver:     "file",
				Level:      Env("LOG_LEVEL", "debug"),
				Path:       Env("LOG_PATH", "./storage/logs"),
				Format:     Env("LOG_FORMAT", "text"),
				MaxAge:     EnvInt("LOG_MAX_AGE", 30),
				MaxBackups: EnvInt("LOG_MAX_BACKUPS", 10),
				Options: map[string]interface{}{
					"daily": true,
				},
			},
			"console": {
				Driver: "console",
				Level:  Env("LOG_LEVEL", "debug"),
				Format: "text",
				Options: map[string]interface{}{
					"color": true,
				},
			},
			"syslog": {
				Driver: "syslog",
				Level:  Env("LOG_LEVEL", "info"),
				Options: map[string]interface{}{
					"facility": "local0",
					"tag":      Env("APP_NAME", "velocity"),
				},
			},
			"errorlog": {
				Driver:  "file",
				Level:   "error",
				Path:    "./storage/logs/errors.log",
				Format:  "json",
				MaxSize: 50,
			},
			"null": {
				Driver: "null",
			},
		},
	}
}

// Env retrieves an environment variable with a default value
func Env(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// EnvInt retrieves an environment variable as integer with a default value
func EnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}

// EnvBool retrieves an environment variable as boolean with a default value
func EnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		value = strings.ToLower(value)
		return value == "true" || value == "1" || value == "yes"
	}
	return defaultValue
}

// GetChannel returns a specific channel configuration
func (c LoggingConfig) GetChannel(name string) (ChannelConfig, bool) {
	channel, exists := c.Channels[name]
	return channel, exists
}

// GetDefaultChannel returns the default channel configuration
func (c LoggingConfig) GetDefaultChannel() (ChannelConfig, bool) {
	return c.GetChannel(c.Default)
}