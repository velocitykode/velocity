package stack

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/log"
)

// resolveStack drives the leaf stack factory through the canonical
// registry the same way an application would.
func resolveStack(t *testing.T, cfg map[string]any) (log.Logger, error) {
	t.Helper()
	return log.Drivers().Resolve(context.Background(), "stack", log.LogConfig{Driver: "stack", Config: cfg})
}

// TestStackLeaf_ChannelsAnySlice verifies the leaf accepts a []any list
// (the shape JSON/env decoding produces) under the unified "channels"
// key, not only a native []string.
func TestStackLeaf_ChannelsAnySlice(t *testing.T) {
	logger, err := resolveStack(t, map[string]any{
		"channels": []any{"console", "null"},
	})
	if err != nil {
		t.Fatalf("resolve stack with []any channels error = %v", err)
	}
	if logger == nil {
		t.Fatal("resolve stack returned nil")
	}
	logger.Info("ok")
}

// TestStackLeaf_ChannelsStringSlice keeps the native []string path working
// under the unified key.
func TestStackLeaf_ChannelsStringSlice(t *testing.T) {
	logger, err := resolveStack(t, map[string]any{
		"channels": []string{"console", "null"},
	})
	if err != nil {
		t.Fatalf("resolve stack with []string channels error = %v", err)
	}
	if logger == nil {
		t.Fatal("resolve stack returned nil")
	}
}

// TestStackLeaf_LegacyStackKey verifies the legacy "stack" key is still
// honoured as a fallback when "channels" is absent.
func TestStackLeaf_LegacyStackKey(t *testing.T) {
	logger, err := resolveStack(t, map[string]any{
		"stack": []any{"console", "null"},
	})
	if err != nil {
		t.Fatalf("resolve stack with legacy stack key error = %v", err)
	}
	if logger == nil {
		t.Fatal("resolve stack returned nil")
	}
}

// TestStackLeaf_ChannelsMalformed pins the fail-loud stance: a present but
// malformed channel list (non-string element) must error rather than
// silently fall back to the default child set.
func TestStackLeaf_ChannelsMalformed(t *testing.T) {
	if _, err := resolveStack(t, map[string]any{
		"channels": []any{"console", 42},
	}); err == nil {
		t.Error("resolve stack with a non-string channel entry should error")
	}
	// Legacy key, same malformed-value stance.
	if _, err := resolveStack(t, map[string]any{
		"stack": []any{99},
	}); err == nil {
		t.Error("resolve stack with malformed legacy stack key should error")
	}
}
