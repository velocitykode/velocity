package grpc

import "testing"

func TestMsgSizeFromEnv(t *testing.T) {
	const def = defaultMaxMsgSize
	tests := []struct {
		name     string
		set      bool
		value    string
		want     int
		wantWarn bool
	}{
		{name: "unset uses default", set: false, want: def, wantWarn: false},
		{name: "valid positive", set: true, value: "1048576", want: 1048576, wantWarn: false},
		{name: "zero falls back + warns", set: true, value: "0", want: def, wantWarn: true},
		{name: "negative falls back + warns", set: true, value: "-1", want: def, wantWarn: true},
		{name: "unparseable falls back + warns", set: true, value: "abc", want: def, wantWarn: true},
		{name: "oversize clamps + warns", set: true, value: "9999999999", want: maxMsgSizeCeil, wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "GRPC_TEST_MSG_SIZE"
			if tt.set {
				t.Setenv(key, tt.value)
			} else {
				t.Setenv(key, "")
			}
			got, warn := msgSizeFromEnv(key, def)
			if got != tt.want {
				t.Errorf("value = %d, want %d", got, tt.want)
			}
			if (warn != "") != tt.wantWarn {
				t.Errorf("warn = %q, wantWarn = %v", warn, tt.wantWarn)
			}
		})
	}
}

func TestClampMsgSize(t *testing.T) {
	if got := clampMsgSize(0); got != defaultMaxMsgSize {
		t.Errorf("clampMsgSize(0) = %d, want default %d", got, defaultMaxMsgSize)
	}
	if got := clampMsgSize(-5); got != defaultMaxMsgSize {
		t.Errorf("clampMsgSize(-5) = %d, want default %d", got, defaultMaxMsgSize)
	}
	if got := clampMsgSize(1024); got != 1024 {
		t.Errorf("clampMsgSize(1024) = %d, want 1024", got)
	}
	if got := clampMsgSize(maxMsgSizeCeil + 1); got != maxMsgSizeCeil {
		t.Errorf("clampMsgSize(ceil+1) = %d, want ceil %d", got, maxMsgSizeCeil)
	}
}
