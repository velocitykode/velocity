package config

import (
	"testing"
)

func TestEnv(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		envValue     string
		setEnv       bool
		defaultValue string
		want         string
	}{
		{
			name:         "returns env value when set",
			key:          "TEST_ENV_VAR",
			envValue:     "custom_value",
			setEnv:       true,
			defaultValue: "default",
			want:         "custom_value",
		},
		{
			name:         "returns default when env not set",
			key:          "TEST_ENV_VAR_UNSET",
			envValue:     "",
			setEnv:       false,
			defaultValue: "default",
			want:         "default",
		},
		{
			name:         "returns default when env is empty string",
			key:          "TEST_ENV_VAR_EMPTY",
			envValue:     "",
			setEnv:       true,
			defaultValue: "default",
			want:         "default",
		},
		{
			name:         "returns empty default when env not set and default is empty",
			key:          "TEST_ENV_VAR_EMPTY_DEFAULT",
			envValue:     "",
			setEnv:       false,
			defaultValue: "",
			want:         "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv(tt.key, tt.envValue)
			}
			got := Env(tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("Env() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvInt(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		envValue     string
		setEnv       bool
		defaultValue int
		want         int
	}{
		{
			name:         "returns parsed integer when valid",
			key:          "TEST_INT_VAR",
			envValue:     "42",
			setEnv:       true,
			defaultValue: 10,
			want:         42,
		},
		{
			name:         "returns default when invalid string",
			key:          "TEST_INT_VAR_INVALID",
			envValue:     "not_a_number",
			setEnv:       true,
			defaultValue: 10,
			want:         10,
		},
		{
			name:         "returns default when float string",
			key:          "TEST_INT_VAR_FLOAT",
			envValue:     "3.14",
			setEnv:       true,
			defaultValue: 10,
			want:         10,
		},
		{
			name:         "returns negative integer when valid",
			key:          "TEST_INT_VAR_NEGATIVE",
			envValue:     "-5",
			setEnv:       true,
			defaultValue: 10,
			want:         -5,
		},
		{
			name:         "returns zero when zero is set",
			key:          "TEST_INT_VAR_ZERO",
			envValue:     "0",
			setEnv:       true,
			defaultValue: 10,
			want:         0,
		},
		{
			name:         "returns default when spaces around number",
			key:          "TEST_INT_VAR_SPACES",
			envValue:     " 42 ",
			setEnv:       true,
			defaultValue: 10,
			want:         10,
		},
		{
			name:         "returns default when env not set",
			key:          "TEST_INT_VAR_UNSET",
			envValue:     "",
			setEnv:       false,
			defaultValue: 10,
			want:         10,
		},
		{
			name:         "returns default when env is empty string",
			key:          "TEST_INT_VAR_EMPTY",
			envValue:     "",
			setEnv:       true,
			defaultValue: 10,
			want:         10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv(tt.key, tt.envValue)
			}
			got := EnvInt(tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("EnvInt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvBool(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		envValue     string
		setEnv       bool
		defaultValue bool
		want         bool
	}{
		{
			name:         "returns true for lowercase true",
			key:          "TEST_BOOL_VAR",
			envValue:     "true",
			setEnv:       true,
			defaultValue: false,
			want:         true,
		},
		{
			name:         "returns true for uppercase TRUE",
			key:          "TEST_BOOL_VAR",
			envValue:     "TRUE",
			setEnv:       true,
			defaultValue: false,
			want:         true,
		},
		{
			name:         "returns true for mixed case True",
			key:          "TEST_BOOL_VAR",
			envValue:     "True",
			setEnv:       true,
			defaultValue: false,
			want:         true,
		},
		{
			name:         "returns true for 1",
			key:          "TEST_BOOL_VAR",
			envValue:     "1",
			setEnv:       true,
			defaultValue: false,
			want:         true,
		},
		{
			name:         "returns true for lowercase yes",
			key:          "TEST_BOOL_VAR",
			envValue:     "yes",
			setEnv:       true,
			defaultValue: false,
			want:         true,
		},
		{
			name:         "returns true for uppercase YES",
			key:          "TEST_BOOL_VAR",
			envValue:     "YES",
			setEnv:       true,
			defaultValue: false,
			want:         true,
		},
		{
			name:         "returns false for false string",
			key:          "TEST_BOOL_VAR",
			envValue:     "false",
			setEnv:       true,
			defaultValue: true,
			want:         false,
		},
		{
			name:         "returns false for 0",
			key:          "TEST_BOOL_VAR",
			envValue:     "0",
			setEnv:       true,
			defaultValue: true,
			want:         false,
		},
		{
			name:         "returns false for no",
			key:          "TEST_BOOL_VAR",
			envValue:     "no",
			setEnv:       true,
			defaultValue: true,
			want:         false,
		},
		{
			name:         "returns false for invalid value",
			key:          "TEST_BOOL_VAR",
			envValue:     "invalid",
			setEnv:       true,
			defaultValue: true,
			want:         false,
		},
		{
			name:         "returns default when env not set",
			key:          "TEST_BOOL_VAR_UNSET",
			envValue:     "",
			setEnv:       false,
			defaultValue: true,
			want:         true,
		},
		{
			name:         "returns default false when env not set",
			key:          "TEST_BOOL_VAR_UNSET",
			envValue:     "",
			setEnv:       false,
			defaultValue: false,
			want:         false,
		},
		{
			name:         "returns default when env is empty string",
			key:          "TEST_BOOL_VAR_EMPTY",
			envValue:     "",
			setEnv:       true,
			defaultValue: true,
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv(tt.key, tt.envValue)
			}
			got := EnvBool(tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("EnvBool() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoggingConfig_GetChannel(t *testing.T) {
	config := GetLoggingConfig()

	tests := []struct {
		name       string
		channel    string
		wantExists bool
		wantDriver string
	}{
		{
			name:       "returns stack channel when exists",
			channel:    "stack",
			wantExists: true,
			wantDriver: "stack",
		},
		{
			name:       "returns single channel when exists",
			channel:    "single",
			wantExists: true,
			wantDriver: "file",
		},
		{
			name:       "returns daily channel when exists",
			channel:    "daily",
			wantExists: true,
			wantDriver: "file",
		},
		{
			name:       "returns console channel when exists",
			channel:    "console",
			wantExists: true,
			wantDriver: "console",
		},
		{
			name:       "returns syslog channel when exists",
			channel:    "syslog",
			wantExists: true,
			wantDriver: "syslog",
		},
		{
			name:       "returns errorlog channel when exists",
			channel:    "errorlog",
			wantExists: true,
			wantDriver: "file",
		},
		{
			name:       "returns null channel when exists",
			channel:    "null",
			wantExists: true,
			wantDriver: "null",
		},
		{
			name:       "returns false for non-existent channel",
			channel:    "nonexistent",
			wantExists: false,
			wantDriver: "",
		},
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
		name          string
		logChannelEnv string
		setEnv        bool
		wantExists    bool
		wantDriver    string
	}{
		{
			name:          "returns stack channel by default",
			logChannelEnv: "",
			setEnv:        false,
			wantExists:    true,
			wantDriver:    "stack",
		},
		{
			name:          "returns console channel when LOG_CHANNEL set to console",
			logChannelEnv: "console",
			setEnv:        true,
			wantExists:    true,
			wantDriver:    "console",
		},
		{
			name:          "returns false for non-existent default channel",
			logChannelEnv: "nonexistent",
			setEnv:        true,
			wantExists:    false,
			wantDriver:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv("LOG_CHANNEL", tt.logChannelEnv)
			}
			config := GetLoggingConfig()
			channel, exists := config.GetDefaultChannel()
			if exists != tt.wantExists {
				t.Errorf("GetDefaultChannel() exists = %v, want %v", exists, tt.wantExists)
			}
			if tt.wantExists && channel.Driver != tt.wantDriver {
				t.Errorf("GetDefaultChannel() driver = %v, want %v", channel.Driver, tt.wantDriver)
			}
		})
	}
}

func TestGetLoggingConfig(t *testing.T) {
	t.Run("returns defaults when no env vars set", func(t *testing.T) {
		config := GetLoggingConfig()

		if config.Default != "stack" {
			t.Errorf("Default = %v, want stack", config.Default)
		}

		expectedChannels := []string{"stack", "single", "daily", "console", "syslog", "errorlog", "null"}
		for _, name := range expectedChannels {
			if _, exists := config.Channels[name]; !exists {
				t.Errorf("expected channel %s to exist", name)
			}
		}
	})

	t.Run("respects LOG_CHANNEL env var", func(t *testing.T) {
		t.Setenv("LOG_CHANNEL", "console")
		config := GetLoggingConfig()

		if config.Default != "console" {
			t.Errorf("Default = %v, want console", config.Default)
		}
	})

	t.Run("respects LOG_LEVEL env var", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "error")
		config := GetLoggingConfig()

		stackChannel, _ := config.GetChannel("stack")
		if stackChannel.Level != "error" {
			t.Errorf("stack channel Level = %v, want error", stackChannel.Level)
		}

		singleChannel, _ := config.GetChannel("single")
		if singleChannel.Level != "error" {
			t.Errorf("single channel Level = %v, want error", singleChannel.Level)
		}
	})

	t.Run("respects LOG_PATH env var", func(t *testing.T) {
		t.Setenv("LOG_PATH", "/custom/path/logs")
		config := GetLoggingConfig()

		singleChannel, _ := config.GetChannel("single")
		if singleChannel.Path != "/custom/path/logs" {
			t.Errorf("single channel Path = %v, want /custom/path/logs", singleChannel.Path)
		}

		dailyChannel, _ := config.GetChannel("daily")
		if dailyChannel.Path != "/custom/path/logs" {
			t.Errorf("daily channel Path = %v, want /custom/path/logs", dailyChannel.Path)
		}
	})

	t.Run("respects LOG_MAX_SIZE env var", func(t *testing.T) {
		t.Setenv("LOG_MAX_SIZE", "200")
		config := GetLoggingConfig()

		singleChannel, _ := config.GetChannel("single")
		if singleChannel.MaxSize != 200 {
			t.Errorf("single channel MaxSize = %v, want 200", singleChannel.MaxSize)
		}
	})

	t.Run("respects LOG_MAX_AGE env var", func(t *testing.T) {
		t.Setenv("LOG_MAX_AGE", "60")
		config := GetLoggingConfig()

		dailyChannel, _ := config.GetChannel("daily")
		if dailyChannel.MaxAge != 60 {
			t.Errorf("daily channel MaxAge = %v, want 60", dailyChannel.MaxAge)
		}
	})

	t.Run("respects LOG_MAX_BACKUPS env var", func(t *testing.T) {
		t.Setenv("LOG_MAX_BACKUPS", "20")
		config := GetLoggingConfig()

		dailyChannel, _ := config.GetChannel("daily")
		if dailyChannel.MaxBackups != 20 {
			t.Errorf("daily channel MaxBackups = %v, want 20", dailyChannel.MaxBackups)
		}
	})

	t.Run("contains all expected channels", func(t *testing.T) {
		config := GetLoggingConfig()

		expectedChannels := map[string]string{
			"stack":    "stack",
			"single":   "file",
			"daily":    "file",
			"console":  "console",
			"syslog":   "syslog",
			"errorlog": "file",
			"null":     "null",
		}

		if len(config.Channels) != len(expectedChannels) {
			t.Errorf("number of channels = %v, want %v", len(config.Channels), len(expectedChannels))
		}

		for name, expectedDriver := range expectedChannels {
			channel, exists := config.Channels[name]
			if !exists {
				t.Errorf("expected channel %s to exist", name)
				continue
			}
			if channel.Driver != expectedDriver {
				t.Errorf("channel %s driver = %v, want %v", name, channel.Driver, expectedDriver)
			}
		}
	})

	t.Run("errorlog channel has fixed error level", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "debug")
		config := GetLoggingConfig()

		errorlogChannel, _ := config.GetChannel("errorlog")
		if errorlogChannel.Level != "error" {
			t.Errorf("errorlog channel Level = %v, want error (should not be affected by LOG_LEVEL)", errorlogChannel.Level)
		}
	})

	t.Run("console channel has color option enabled", func(t *testing.T) {
		config := GetLoggingConfig()

		consoleChannel, _ := config.GetChannel("console")
		colorOption, exists := consoleChannel.Options["color"]
		if !exists {
			t.Error("console channel should have color option")
			return
		}
		if colorOption != true {
			t.Errorf("console channel color option = %v, want true", colorOption)
		}
	})

	t.Run("stack channel has channels option with single and daily", func(t *testing.T) {
		config := GetLoggingConfig()

		stackChannel, _ := config.GetChannel("stack")
		channelsOption, exists := stackChannel.Options["channels"]
		if !exists {
			t.Error("stack channel should have channels option")
			return
		}
		channels, ok := channelsOption.([]string)
		if !ok {
			t.Error("stack channel channels option should be []string")
			return
		}
		if len(channels) != 2 || channels[0] != "single" || channels[1] != "daily" {
			t.Errorf("stack channel channels = %v, want [single daily]", channels)
		}
	})

	t.Run("daily channel has daily option enabled", func(t *testing.T) {
		config := GetLoggingConfig()

		dailyChannel, _ := config.GetChannel("daily")
		dailyOption, exists := dailyChannel.Options["daily"]
		if !exists {
			t.Error("daily channel should have daily option")
			return
		}
		if dailyOption != true {
			t.Errorf("daily channel daily option = %v, want true", dailyOption)
		}
	})
}
