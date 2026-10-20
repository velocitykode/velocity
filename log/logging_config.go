package log

// LoggingConfig defines the logging configuration structure
type LoggingConfig struct {
	// Channels maps a channel name to its configuration.
	Channels map[string]ChannelConfig `json:"channels"`

	// Default is the name of the channel the Manager uses by default.
	Default string `json:"default"`
}

// ChannelConfig defines configuration for a specific log channel
type ChannelConfig struct {
	// Driver is the log driver of the channel. "stack" fans out to the
	// Manager channels named in Options["channels"].
	Driver string `json:"driver"`

	// Level is the minimum log level, passed to the driver as "level".
	Level string `json:"level"`

	// MaxAge is the number of days to keep log files, passed to the driver
	// as "days" when positive.
	MaxAge int `json:"max_age,omitempty"`

	// Options holds driver-specific options, merged into the driver config
	// after Level, Path and MaxAge.
	Options map[string]any `json:"options,omitempty"`

	// Path is the log file path, passed to the driver as "path" when
	// non-empty.
	Path string `json:"path,omitempty"`
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
