package config

// LoggingConfig defines the logging configuration structure
type LoggingConfig struct {
	Default  string                   `json:"default"`
	Channels map[string]ChannelConfig `json:"channels"`
}

// ChannelConfig defines configuration for a specific log channel
type ChannelConfig struct {
	Driver  string                 `json:"driver"`
	Level   string                 `json:"level"`
	Path    string                 `json:"path,omitempty"`
	MaxAge  int                    `json:"max_age,omitempty"` // days
	Options map[string]interface{} `json:"options,omitempty"` // driver-specific options
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
