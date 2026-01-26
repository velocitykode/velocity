package exceptions

import (
	"errors"
	"os"
	"sync"
	"testing"
)

func TestInitialize(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	Initialize()

	if globalHandler == nil {
		t.Error("Initialize did not set globalHandler")
	}
}

func TestInitialize_WithOptions(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	Initialize(WithDebug(true), WithEnvironment("testing"))

	if !globalHandler.IsDebug() {
		t.Error("Option WithDebug not applied")
	}
	if globalHandler.GetEnvironment() != "testing" {
		t.Errorf("Environment = %q, want testing", globalHandler.GetEnvironment())
	}
}

func TestInitialize_OnlyOnce(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	Initialize(WithDebug(true))
	Initialize(WithDebug(false)) // Should be ignored

	if !globalHandler.IsDebug() {
		t.Error("Second Initialize should be ignored")
	}
}

func TestGet(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	h := Get()

	if h == nil {
		t.Fatal("Get() returned nil")
	}

	// Calling again should return same instance
	h2 := Get()
	if h != h2 {
		t.Error("Get() should return same instance")
	}
}

func TestSetGlobal(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	custom := NewHandler(WithDebug(true))
	SetGlobal(custom)

	if Get() != custom {
		t.Error("SetGlobal did not set handler")
	}
}

func TestGlobalReport(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	var reported bool
	mockReporter := NewCallbackReporter(func(err error, ctx *ExceptionContext) {
		reported = true
	})

	SetGlobal(NewHandler(WithReporters(mockReporter)))

	Report(errors.New("test"), nil)

	if !reported {
		t.Error("Global Report did not call reporter")
	}
}

func TestGlobalRender(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	SetGlobal(NewHandler())

	ctx := &mockRenderContext{headers: make(map[string]string), accept: "application/json"}
	Render(ctx, errors.New("test"), nil)

	if ctx.statusCode == 0 {
		t.Error("Global Render did not render")
	}
}

func TestGlobalHandle(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	SetGlobal(NewHandler())

	ctx := &mockRenderContext{headers: make(map[string]string), accept: "application/json"}
	Handle(ctx, errors.New("test"))

	if ctx.statusCode == 0 {
		t.Error("Global Handle did not handle")
	}
}

func TestGlobalHandleWithContext(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	SetGlobal(NewHandler())

	ctx := &mockRenderContext{headers: make(map[string]string), accept: "application/json"}
	exCtx := NewExceptionContext()
	HandleWithContext(ctx, errors.New("test"), exCtx)

	if ctx.statusCode == 0 {
		t.Error("Global HandleWithContext did not handle")
	}
}

func TestGlobalIsDebug(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	SetGlobal(NewHandler(WithDebug(true)))

	if !IsDebug() {
		t.Error("Global IsDebug returned false")
	}
}

func TestGlobalSetDebug(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	Initialize()
	SetDebug(true)

	if !IsDebug() {
		t.Error("Global SetDebug did not set debug")
	}
}

func TestIsDebugMode(t *testing.T) {
	tests := []struct {
		name     string
		appDebug string
		appEnv   string
		want     bool
	}{
		{"debug true", "true", "", true},
		{"debug 1", "1", "", true},
		{"debug yes", "yes", "", true},
		{"debug false", "false", "", false},
		{"env local", "", "local", true},
		{"env development", "", "development", true},
		{"env production", "", "production", false},
		{"env staging", "", "staging", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear env vars
			os.Unsetenv("APP_DEBUG")
			os.Unsetenv("APP_ENV")

			if tt.appDebug != "" {
				os.Setenv("APP_DEBUG", tt.appDebug)
			}
			if tt.appEnv != "" {
				os.Setenv("APP_ENV", tt.appEnv)
			}

			got := isDebugMode()
			if got != tt.want {
				t.Errorf("isDebugMode() = %v, want %v", got, tt.want)
			}
		})
	}

	// Cleanup
	os.Unsetenv("APP_DEBUG")
	os.Unsetenv("APP_ENV")
}

func TestGetEnvOrDefault(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		envValue   string
		defaultVal string
		want       string
	}{
		{"env set", "TEST_VAR", "value", "default", "value"},
		{"env empty uses default", "TEST_VAR", "", "default", "default"},
		{"env not set", "NONEXISTENT_VAR", "", "default", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv(tt.key)
			if tt.envValue != "" {
				os.Setenv(tt.key, tt.envValue)
			}

			got := getEnvOrDefault(tt.key, tt.defaultVal)
			if got != tt.want {
				t.Errorf("getEnvOrDefault() = %q, want %q", got, tt.want)
			}

			os.Unsetenv(tt.key)
		})
	}
}

func TestResetGlobal(t *testing.T) {
	Initialize()

	if globalHandler == nil {
		t.Fatal("Handler should be set after Initialize")
	}

	ResetGlobal()

	if globalHandler != nil {
		t.Error("ResetGlobal did not clear handler")
	}
}

func TestConcurrentGet(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	var wg sync.WaitGroup
	handlers := make([]*Handler, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			handlers[idx] = Get()
		}(i)
	}

	wg.Wait()

	// All should return the same handler
	for i := 1; i < 100; i++ {
		if handlers[i] != handlers[0] {
			t.Error("Concurrent Get returned different handlers")
			break
		}
	}
}

func TestInitialize_FromEnv(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	os.Setenv("APP_DEBUG", "true")
	os.Setenv("APP_ENV", "local")
	defer os.Unsetenv("APP_DEBUG")
	defer os.Unsetenv("APP_ENV")

	Initialize()

	if !globalHandler.IsDebug() {
		t.Error("Should be debug mode from env")
	}
	if globalHandler.GetEnvironment() != "local" {
		t.Error("Environment should be local from env")
	}
}

func TestGlobalAPIMode(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	Initialize()

	// Default should be false
	if IsAPIMode() {
		t.Error("API mode should default to false")
	}

	SetAPIMode(true)
	if !IsAPIMode() {
		t.Error("SetAPIMode did not enable API mode")
	}

	SetAPIMode(false)
	if IsAPIMode() {
		t.Error("SetAPIMode did not disable API mode")
	}
}

func TestGlobalSetAPIPrefixes(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	Initialize()

	SetAPIPrefixes("/api/v1", "/api/v2")

	prefixes := Get().GetAPIPrefixes()
	if len(prefixes) != 2 {
		t.Errorf("Expected 2 prefixes, got %d", len(prefixes))
	}
}

func TestIsAPIMode_FromEnv(t *testing.T) {
	tests := []struct {
		name   string
		envVal string
		want   bool
	}{
		{"true", "true", true},
		{"1", "1", true},
		{"yes", "yes", true},
		{"false", "false", false},
		{"empty", "", false},
		{"other", "no", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv("API_MODE")
			if tt.envVal != "" {
				os.Setenv("API_MODE", tt.envVal)
			}
			defer os.Unsetenv("API_MODE")

			got := isAPIMode()
			if got != tt.want {
				t.Errorf("isAPIMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInitialize_WithAPIMode(t *testing.T) {
	ResetGlobal()
	defer ResetGlobal()

	os.Setenv("API_MODE", "true")
	defer os.Unsetenv("API_MODE")

	Initialize()

	if !globalHandler.IsAPIMode() {
		t.Error("Should be API mode from env")
	}
}
