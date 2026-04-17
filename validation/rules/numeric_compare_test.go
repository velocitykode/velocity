package rules

import "testing"

func TestGtRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{"nil ok", nil, []string{"5"}, nil, false},
		{"no params", 5, nil, nil, true},
		{"int greater", 10, []string{"5"}, nil, false},
		{"int equal fails", 5, []string{"5"}, nil, true},
		{"int less fails", 1, []string{"5"}, nil, true},
		{"string numeric passes", "7", []string{"5"}, nil, false},
		{"non-numeric value", "abc", []string{"5"}, nil, true},
		{"field reference passes", 10, []string{"other"}, map[string]interface{}{"other": 5}, false},
		{"field reference fails", 3, []string{"other"}, map[string]interface{}{"other": 5}, true},
		{"field reference missing numeric", 3, []string{"other"}, map[string]interface{}{"other": "notnum"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := GtRule("age", tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestGteRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		params  []string
		wantErr bool
	}{
		{"nil ok", nil, []string{"5"}, false},
		{"equal passes", 5, []string{"5"}, false},
		{"greater passes", 7, []string{"5"}, false},
		{"less fails", 4, []string{"5"}, true},
		{"float passes", 5.1, []string{"5"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := GteRule("age", tt.value, tt.params, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestLtRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		params  []string
		wantErr bool
	}{
		{"nil ok", nil, []string{"5"}, false},
		{"less passes", 3, []string{"5"}, false},
		{"equal fails", 5, []string{"5"}, true},
		{"greater fails", 6, []string{"5"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := LtRule("age", tt.value, tt.params, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestLteRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		params  []string
		wantErr bool
	}{
		{"nil ok", nil, []string{"5"}, false},
		{"less passes", 3, []string{"5"}, false},
		{"equal passes", 5, []string{"5"}, false},
		{"greater fails", 6, []string{"5"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := LteRule("age", tt.value, tt.params, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}
