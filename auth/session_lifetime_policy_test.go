package auth

import (
	"testing"
	"time"
)

func TestSessionConfig_ExpiresAt(t *testing.T) {
	created := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		idle, abs  int
		lastActive time.Duration // after created
		want       time.Time
	}{
		{"idle window from last activity", 120, 480, 60 * time.Minute, created.Add(180 * time.Minute)},
		{"absolute cap wins near the end", 120, 480, 400 * time.Minute, created.Add(480 * time.Minute)},
		{"default absolute cap is 30 days", 120, 0, 30*24*time.Hour - time.Minute, created.Add(30 * 24 * time.Hour)},
		{"no idle timeout: absolute cap only", 0, 480, 10 * time.Minute, created.Add(480 * time.Minute)},
		{"no absolute cap: idle only", 120, -1, 1000 * time.Hour, created.Add(1000*time.Hour + 120*time.Minute)},
		{"neither: never", 0, -1, time.Hour, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := SessionConfig{IdleLifetime: tt.idle, AbsoluteLifetime: tt.abs}
			if got := c.ExpiresAt(created, created.Add(tt.lastActive)); !got.Equal(tt.want) {
				t.Fatalf("ExpiresAt = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionConfig_Timeouts(t *testing.T) {
	tests := []struct {
		idle, abs         int
		wantIdle, wantAbs time.Duration
	}{
		{120, 480, 120 * time.Minute, 480 * time.Minute},
		{0, 0, 0, 30 * 24 * time.Hour},
		{-5, -1, 0, 0},
	}
	for _, tt := range tests {
		c := SessionConfig{IdleLifetime: tt.idle, AbsoluteLifetime: tt.abs}
		if got := c.IdleTimeout(); got != tt.wantIdle {
			t.Errorf("IdleTimeout(%d) = %v, want %v", tt.idle, got, tt.wantIdle)
		}
		if got := c.AbsoluteTimeout(); got != tt.wantAbs {
			t.Errorf("AbsoluteTimeout(%d) = %v, want %v", tt.abs, got, tt.wantAbs)
		}
	}
}

func TestSessionConfig_RememberTimeout(t *testing.T) {
	tests := []struct {
		remember int
		want     time.Duration
	}{
		{0, 30 * 24 * time.Hour},
		{60, time.Hour},
		{90 * 24 * 60, 90 * 24 * time.Hour},
	}
	for _, tt := range tests {
		c := SessionConfig{IdleLifetime: 120, RememberLifetime: tt.remember}
		if got := c.RememberTimeout(); got != tt.want {
			t.Errorf("RememberTimeout(%d) = %v, want %v", tt.remember, got, tt.want)
		}
	}
}
