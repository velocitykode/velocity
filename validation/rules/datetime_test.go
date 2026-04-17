package rules

import (
	"testing"
	"time"
)

func TestDateRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"nil is ok", nil, false},
		{"RFC3339", "2024-01-02T03:04:05Z", false},
		{"date only", "2024-01-02", false},
		{"datetime with space", "2024-01-02 03:04:05", false},
		{"slash MDY", "01/02/2024", false},
		{"time.Time passes", time.Now(), false},
		{"empty string", "", true},
		{"garbage", "not a date", true},
		{"int is invalid", 1234, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := DateRule("ts", tt.value, nil, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestDateFormatRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		params  []string
		wantErr bool
	}{
		{"nil is ok", nil, []string{"2006-01-02"}, false},
		{"missing layout", "2024-01-02", nil, true},
		{"matching layout", "2024-01-02", []string{"2006-01-02"}, false},
		{"mismatched layout", "2024/01/02", []string{"2006-01-02"}, true},
		{"non-string", 123, []string{"2006-01-02"}, true},
		{"layout with commas preserved", "2024-01-02T03:04:05-07:00", []string{"2006-01-02T15:04:05Z07:00"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := DateFormatRule("ts", tt.value, tt.params, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestTimezoneRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"nil is ok", nil, false},
		{"UTC", "UTC", false},
		{"America/New_York", "America/New_York", false},
		{"empty", "", true},
		{"nonsense", "Moon/Crater", true},
		{"non-string", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := TimezoneRule("tz", tt.value, nil, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}
