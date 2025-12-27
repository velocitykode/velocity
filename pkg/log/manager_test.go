package log

import (
	"testing"

	"github.com/velocitykode/velocity/pkg/config"
)

func TestNewManager(t *testing.T) {
	cfg := config.LoggingConfig{
		Default: "console",
		Channels: map[string]config.ChannelConfig{
			"console": {
				Driver: "console",
				Level:  "debug",
			},
		},
	}

	manager := NewManager(cfg)
	if manager == nil {
		t.Error("NewManager() returned nil")
	}
	if manager.config.Default != "console" {
		t.Errorf("NewManager() default = %v, want console", manager.config.Default)
	}
}

func TestManager_Channel(t *testing.T) {
	cfg := config.LoggingConfig{
		Default: "console",
		Channels: map[string]config.ChannelConfig{
			"console": {
				Driver: "console",
				Level:  "debug",
			},
			"file": {
				Driver: "file",
				Level:  "info",
				Path:   "/tmp/test-logs",
			},
			"null": {
				Driver: "null",
			},
		},
	}

	manager := NewManager(cfg)

	// Test getting console channel
	console, err := manager.Channel("console")
	if err != nil {
		t.Errorf("Channel(console) error = %v", err)
	}
	if console == nil {
		t.Error("Channel(console) returned nil")
	}

	// Test getting file channel
	file, err := manager.Channel("file")
	if err != nil {
		t.Errorf("Channel(file) error = %v", err)
	}
	if file == nil {
		t.Error("Channel(file) returned nil")
	}

	// Test getting null channel
	null, err := manager.Channel("null")
	if err != nil {
		t.Errorf("Channel(null) error = %v", err)
	}
	if null == nil {
		t.Error("Channel(null) returned nil")
	}

	// Test getting non-existent channel
	_, err = manager.Channel("nonexistent")
	if err == nil {
		t.Error("Channel(nonexistent) should return error")
	}
}

func TestManager_Default(t *testing.T) {
	cfg := config.LoggingConfig{
		Default: "console",
		Channels: map[string]config.ChannelConfig{
			"console": {
				Driver: "console",
				Level:  "debug",
			},
		},
	}

	manager := NewManager(cfg)

	logger, err := manager.Default()
	if err != nil {
		t.Errorf("Default() error = %v", err)
	}
	if logger == nil {
		t.Error("Default() returned nil")
	}
}

func TestManager_ConcurrentAccess(t *testing.T) {
	cfg := config.LoggingConfig{
		Default: "console",
		Channels: map[string]config.ChannelConfig{
			"console": {
				Driver: "console",
				Level:  "debug",
			},
			"file": {
				Driver: "file",
				Level:  "info",
				Path:   "/tmp/test-logs",
			},
		},
	}

	manager := NewManager(cfg)
	done := make(chan bool)

	// Launch multiple goroutines to access channels concurrently
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				channel := "console"
				if j%2 == 0 {
					channel = "file"
				}
				logger, err := manager.Channel(channel)
				if err != nil {
					t.Errorf("Goroutine %d: Channel(%s) error = %v", id, channel, err)
				}
				if logger != nil {
					logger.Info("test", "goroutine", id, "iteration", j)
				}
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestStackLogger(t *testing.T) {
	// Create mock loggers
	logger1 := NewNullLogger()
	logger2 := NewNullLogger()

	stack := NewStackLogger(logger1, logger2)

	// These should not panic
	stack.Debug("debug message")
	stack.Info("info message")
	stack.Warn("warn message")
	stack.Error("error message")
	stack.Fatal("fatal message")
}

func TestNullLogger(t *testing.T) {
	logger := NewNullLogger()

	// Test that all methods exist and don't panic
	// Coverage will show these are called
	logger.Debug("debug message", "key", "value")
	logger.Info("info message", "key", "value")
	logger.Warn("warn message", "key", "value")
	logger.Error("error message", "key", "value")
	logger.Fatal("fatal message", "key", "value")
}

func TestManager_StackDriverWithChannels(t *testing.T) {
	cfg := config.LoggingConfig{
		Default: "stack",
		Channels: map[string]config.ChannelConfig{
			"console": {
				Driver: "console",
			},
			"file": {
				Driver: "file",
				Path:   "/tmp/test-logs",
			},
			"stack": {
				Driver: "stack",
				Options: map[string]any{
					"channels": []string{"console", "file"},
				},
			},
		},
	}

	manager := NewManager(cfg)

	// First create the dependent channels to avoid deadlock
	console, _ := manager.Channel("console")
	file, _ := manager.Channel("file")

	if console == nil || file == nil {
		t.Fatal("Failed to create dependent channels")
	}

	stack, err := manager.Channel("stack")
	if err != nil {
		t.Errorf("Channel(stack) error = %v", err)
	}
	if stack == nil {
		t.Error("Channel(stack) returned nil")
	}
}

func TestManager_StackDriverWithoutChannels(t *testing.T) {
	cfg := config.LoggingConfig{
		Default: "stack",
		Channels: map[string]config.ChannelConfig{
			"stack": {
				Driver: "stack",
			},
		},
	}

	manager := NewManager(cfg)

	_, err := manager.Channel("stack")
	if err == nil {
		t.Error("Channel(stack) without channels option should return error")
	}
}

func TestManager_StackDriverEmptyChannels(t *testing.T) {
	cfg := config.LoggingConfig{
		Default: "stack",
		Channels: map[string]config.ChannelConfig{
			"stack": {
				Driver: "stack",
				Options: map[string]any{
					"channels": []string{},
				},
			},
		},
	}

	manager := NewManager(cfg)

	_, err := manager.Channel("stack")
	if err == nil {
		t.Error("Channel(stack) with empty channels should return error")
	}
}

func TestManager_StackDriverInvalidChannel(t *testing.T) {
	cfg := config.LoggingConfig{
		Default: "stack",
		Channels: map[string]config.ChannelConfig{
			"stack": {
				Driver: "stack",
				Options: map[string]any{
					"channels": []string{"nonexistent"},
				},
			},
		},
	}

	manager := NewManager(cfg)

	_, err := manager.Channel("stack")
	if err == nil {
		t.Error("Channel(stack) with only invalid channels should return error")
	}
}

func TestManager_FileDriverDefaultPath(t *testing.T) {
	cfg := config.LoggingConfig{
		Default: "file",
		Channels: map[string]config.ChannelConfig{
			"file": {
				Driver: "file",
				// No path specified, should use default
			},
		},
	}

	manager := NewManager(cfg)

	file, err := manager.Channel("file")
	if err != nil {
		t.Errorf("Channel(file) error = %v", err)
	}
	if file == nil {
		t.Error("Channel(file) returned nil")
	}
}

func TestManager_UnsupportedDriver(t *testing.T) {
	cfg := config.LoggingConfig{
		Default: "unsupported",
		Channels: map[string]config.ChannelConfig{
			"unsupported": {
				Driver: "unsupported",
			},
		},
	}

	manager := NewManager(cfg)

	_, err := manager.Channel("unsupported")
	if err == nil {
		t.Error("Channel(unsupported) should return error for unsupported driver")
	}
}

func TestManager_RaceConditionCoverage(t *testing.T) {
	cfg := config.LoggingConfig{
		Default: "console",
		Channels: map[string]config.ChannelConfig{
			"test": {
				Driver: "console",
			},
		},
	}

	manager := NewManager(cfg)

	// Pre-create the channel
	first, _ := manager.Channel("test")

	// Try to get it again - this will hit the double-check branch
	second, _ := manager.Channel("test")

	if first != second {
		t.Error("Should return same logger instance")
	}
}
