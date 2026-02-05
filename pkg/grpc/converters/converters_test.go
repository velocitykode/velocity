package converters_test

import (
	"testing"
	"time"

	"github.com/velocitykode/velocity/pkg/grpc/converters"
)

func TestTimeToProto(t *testing.T) {
	tests := []struct {
		name  string
		input *time.Time
		isNil bool
	}{
		{
			name:  "nil input",
			input: nil,
			isNil: true,
		},
		{
			name:  "valid time",
			input: timePtr(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)),
			isNil: false,
		},
		{
			name:  "zero time",
			input: timePtr(time.Time{}),
			isNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := converters.TimeToProto(tt.input)
			if tt.isNil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
			} else {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				if !result.AsTime().Equal(*tt.input) {
					t.Errorf("time mismatch: got %v, want %v", result.AsTime(), *tt.input)
				}
			}
		})
	}
}

func TestTimeValueToProto(t *testing.T) {
	now := time.Now()
	result := converters.TimeValueToProto(now)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.AsTime().Equal(now) {
		t.Errorf("time mismatch")
	}
}

func TestProtoToTime(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := converters.ProtoToTime(nil)
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("valid input", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		proto := converters.TimeValueToProto(now)
		result := converters.ProtoToTime(proto)
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		if !result.Equal(now) {
			t.Errorf("time mismatch: got %v, want %v", *result, now)
		}
	})
}

func TestProtoToTimeValue(t *testing.T) {
	t.Run("nil input returns zero", func(t *testing.T) {
		result := converters.ProtoToTimeValue(nil)
		if !result.IsZero() {
			t.Errorf("expected zero time, got %v", result)
		}
	})

	t.Run("valid input", func(t *testing.T) {
		now := time.Now()
		proto := converters.TimeValueToProto(now)
		result := converters.ProtoToTimeValue(proto)
		if result.IsZero() {
			t.Error("expected non-zero time")
		}
	})
}

func TestNowProto(t *testing.T) {
	before := time.Now()
	result := converters.NowProto()
	after := time.Now()

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	ts := result.AsTime()
	if ts.Before(before) || ts.After(after) {
		t.Error("timestamp outside expected range")
	}
}

func TestDurationToProto(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
	}{
		{"zero", 0},
		{"one second", time.Second},
		{"one hour", time.Hour},
		{"negative", -5 * time.Minute},
		{"complex", 2*time.Hour + 30*time.Minute + 45*time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := converters.DurationToProto(tt.duration)
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			if result.AsDuration() != tt.duration {
				t.Errorf("duration mismatch: got %v, want %v", result.AsDuration(), tt.duration)
			}
		})
	}
}

func TestProtoToDuration(t *testing.T) {
	t.Run("nil input returns zero", func(t *testing.T) {
		result := converters.ProtoToDuration(nil)
		if result != 0 {
			t.Errorf("expected 0, got %v", result)
		}
	})

	t.Run("valid input", func(t *testing.T) {
		d := 5 * time.Minute
		proto := converters.DurationToProto(d)
		result := converters.ProtoToDuration(proto)
		if result != d {
			t.Errorf("duration mismatch: got %v, want %v", result, d)
		}
	})
}

func TestTimeOrNil(t *testing.T) {
	t.Run("zero time returns nil", func(t *testing.T) {
		result := converters.TimeOrNil(time.Time{})
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("non-zero time returns pointer", func(t *testing.T) {
		now := time.Now()
		result := converters.TimeOrNil(now)
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		if !result.Equal(now) {
			t.Error("time mismatch")
		}
	})
}

func TestTimeOrZero(t *testing.T) {
	t.Run("nil returns zero", func(t *testing.T) {
		result := converters.TimeOrZero(nil)
		if !result.IsZero() {
			t.Errorf("expected zero time, got %v", result)
		}
	})

	t.Run("non-nil returns value", func(t *testing.T) {
		now := time.Now()
		result := converters.TimeOrZero(&now)
		if !result.Equal(now) {
			t.Error("time mismatch")
		}
	})
}

func TestNormalizePagination(t *testing.T) {
	tests := []struct {
		name       string
		page       int32
		pageSize   int32
		wantPage   int
		wantSize   int
		wantOffset int
	}{
		{
			name:       "defaults for zero values",
			page:       0,
			pageSize:   0,
			wantPage:   1,
			wantSize:   converters.DefaultPageSize,
			wantOffset: 0,
		},
		{
			name:       "negative page becomes 1",
			page:       -5,
			pageSize:   10,
			wantPage:   1,
			wantSize:   10,
			wantOffset: 0,
		},
		{
			name:       "page 1",
			page:       1,
			pageSize:   10,
			wantPage:   1,
			wantSize:   10,
			wantOffset: 0,
		},
		{
			name:       "page 2",
			page:       2,
			pageSize:   10,
			wantPage:   2,
			wantSize:   10,
			wantOffset: 10,
		},
		{
			name:       "page 5 with size 20",
			page:       5,
			pageSize:   20,
			wantPage:   5,
			wantSize:   20,
			wantOffset: 80,
		},
		{
			name:       "exceeds max page size",
			page:       1,
			pageSize:   500,
			wantPage:   1,
			wantSize:   converters.MaxPageSize,
			wantOffset: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := converters.NormalizePagination(tt.page, tt.pageSize)
			if result.Page != tt.wantPage {
				t.Errorf("Page = %v, want %v", result.Page, tt.wantPage)
			}
			if result.Size != tt.wantSize {
				t.Errorf("Size = %v, want %v", result.Size, tt.wantSize)
			}
			if result.Offset != tt.wantOffset {
				t.Errorf("Offset = %v, want %v", result.Offset, tt.wantOffset)
			}
			if result.Limit != tt.wantSize {
				t.Errorf("Limit = %v, want %v", result.Limit, tt.wantSize)
			}
		})
	}
}

func TestNewPaginationResponse(t *testing.T) {
	tests := []struct {
		name        string
		page        int32
		pageSize    int32
		totalItems  int32
		wantPages   int32
		wantHasNext bool
		wantHasPrev bool
	}{
		{
			name:        "first page with more pages",
			page:        1,
			pageSize:    10,
			totalItems:  25,
			wantPages:   3,
			wantHasNext: true,
			wantHasPrev: false,
		},
		{
			name:        "middle page",
			page:        2,
			pageSize:    10,
			totalItems:  25,
			wantPages:   3,
			wantHasNext: true,
			wantHasPrev: true,
		},
		{
			name:        "last page",
			page:        3,
			pageSize:    10,
			totalItems:  25,
			wantPages:   3,
			wantHasNext: false,
			wantHasPrev: true,
		},
		{
			name:        "single page",
			page:        1,
			pageSize:    10,
			totalItems:  5,
			wantPages:   1,
			wantHasNext: false,
			wantHasPrev: false,
		},
		{
			name:        "empty results",
			page:        1,
			pageSize:    10,
			totalItems:  0,
			wantPages:   1,
			wantHasNext: false,
			wantHasPrev: false,
		},
		{
			name:        "exact page boundary",
			page:        1,
			pageSize:    10,
			totalItems:  10,
			wantPages:   1,
			wantHasNext: false,
			wantHasPrev: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := converters.NewPaginationResponse(tt.page, tt.pageSize, tt.totalItems)
			if result.Page != tt.page {
				t.Errorf("Page = %v, want %v", result.Page, tt.page)
			}
			if result.PageSize != tt.pageSize {
				t.Errorf("PageSize = %v, want %v", result.PageSize, tt.pageSize)
			}
			if result.TotalItems != tt.totalItems {
				t.Errorf("TotalItems = %v, want %v", result.TotalItems, tt.totalItems)
			}
			if result.TotalPages != tt.wantPages {
				t.Errorf("TotalPages = %v, want %v", result.TotalPages, tt.wantPages)
			}
			if result.HasNext != tt.wantHasNext {
				t.Errorf("HasNext = %v, want %v", result.HasNext, tt.wantHasNext)
			}
			if result.HasPrev != tt.wantHasPrev {
				t.Errorf("HasPrev = %v, want %v", result.HasPrev, tt.wantHasPrev)
			}
		})
	}
}

func TestCalculateTotalPages(t *testing.T) {
	tests := []struct {
		total    int32
		pageSize int32
		want     int32
	}{
		{0, 10, 1},
		{1, 10, 1},
		{9, 10, 1},
		{10, 10, 1},
		{11, 10, 2},
		{20, 10, 2},
		{21, 10, 3},
		{100, 25, 4},
		{101, 25, 5},
		{0, 0, 1},  // zero page size defaults
		{10, -1, 1}, // negative page size defaults
	}

	for _, tt := range tests {
		got := converters.CalculateTotalPages(tt.total, tt.pageSize)
		if got != tt.want {
			t.Errorf("CalculateTotalPages(%d, %d) = %d, want %d", tt.total, tt.pageSize, got, tt.want)
		}
	}
}

func TestNormalizeCursorPagination(t *testing.T) {
	tests := []struct {
		name      string
		cursor    string
		limit     int32
		wantLimit int
	}{
		{
			name:      "default limit",
			cursor:    "abc",
			limit:     0,
			wantLimit: converters.DefaultPageSize,
		},
		{
			name:      "negative limit defaults",
			cursor:    "abc",
			limit:     -1,
			wantLimit: converters.DefaultPageSize,
		},
		{
			name:      "exceeds max",
			cursor:    "abc",
			limit:     500,
			wantLimit: converters.MaxPageSize,
		},
		{
			name:      "valid limit",
			cursor:    "abc",
			limit:     50,
			wantLimit: 50,
		},
		{
			name:      "empty cursor",
			cursor:    "",
			limit:     25,
			wantLimit: 25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := converters.NormalizeCursorPagination(tt.cursor, tt.limit)
			if result.Cursor != tt.cursor {
				t.Errorf("Cursor = %v, want %v", result.Cursor, tt.cursor)
			}
			if result.Limit != tt.wantLimit {
				t.Errorf("Limit = %v, want %v", result.Limit, tt.wantLimit)
			}
		})
	}
}

func TestNewCursorResponse(t *testing.T) {
	resp := converters.NewCursorResponse("next123", "prev456", true, 25)
	if resp.NextCursor != "next123" {
		t.Errorf("NextCursor = %v, want next123", resp.NextCursor)
	}
	if resp.PrevCursor != "prev456" {
		t.Errorf("PrevCursor = %v, want prev456", resp.PrevCursor)
	}
	if !resp.HasMore {
		t.Error("HasMore should be true")
	}
	if resp.Limit != 25 {
		t.Errorf("Limit = %v, want 25", resp.Limit)
	}
}

// Helper
func timePtr(t time.Time) *time.Time {
	return &t
}
