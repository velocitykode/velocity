package log

import "testing"

func TestLoggingConfig_GetChannel(t *testing.T) {
	config := LoggingConfig{
		Default: "stack",
		Channels: map[string]ChannelConfig{
			"stack":   {Driver: "stack", Level: "debug"},
			"console": {Driver: "console", Level: "debug"},
			"null":    {Driver: "null"},
		},
	}

	tests := []struct {
		name       string
		channel    string
		wantExists bool
		wantDriver string
	}{
		{name: "returns stack channel when exists", channel: "stack", wantExists: true, wantDriver: "stack"},
		{name: "returns console channel when exists", channel: "console", wantExists: true, wantDriver: "console"},
		{name: "returns null channel when exists", channel: "null", wantExists: true, wantDriver: "null"},
		{name: "returns false for non-existent channel", channel: "nonexistent", wantExists: false, wantDriver: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel, exists := config.GetChannel(tt.channel)
			if exists != tt.wantExists {
				t.Errorf("GetChannel() exists = %v, want %v", exists, tt.wantExists)
			}
			if tt.wantExists && channel.Driver != tt.wantDriver {
				t.Errorf("GetChannel() driver = %v, want %v", channel.Driver, tt.wantDriver)
			}
		})
	}
}

func TestLoggingConfig_GetDefaultChannel(t *testing.T) {
	tests := []struct {
		name       string
		config     LoggingConfig
		wantExists bool
		wantDriver string
	}{
		{
			name: "returns default channel when exists",
			config: LoggingConfig{
				Default:  "console",
				Channels: map[string]ChannelConfig{"console": {Driver: "console"}},
			},
			wantExists: true,
			wantDriver: "console",
		},
		{
			name: "returns false when default channel does not exist",
			config: LoggingConfig{
				Default:  "nonexistent",
				Channels: map[string]ChannelConfig{},
			},
			wantExists: false,
			wantDriver: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel, exists := tt.config.GetDefaultChannel()
			if exists != tt.wantExists {
				t.Errorf("GetDefaultChannel() exists = %v, want %v", exists, tt.wantExists)
			}
			if tt.wantExists && channel.Driver != tt.wantDriver {
				t.Errorf("GetDefaultChannel() driver = %v, want %v", channel.Driver, tt.wantDriver)
			}
		})
	}
}
