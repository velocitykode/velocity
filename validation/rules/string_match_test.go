package rules

import "testing"

func TestStartsWithRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		params  []string
		wantErr bool
	}{
		{"nil ok", nil, []string{"foo"}, false},
		{"no params", "foo", nil, true},
		{"matches one prefix", "foobar", []string{"foo"}, false},
		{"matches alt prefix", "httpsfoo", []string{"http", "https"}, false},
		{"mismatch", "bar", []string{"foo"}, true},
		{"non-string", 1, []string{"foo"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := StartsWithRule("name", tt.value, tt.params, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestEndsWithRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		params  []string
		wantErr bool
	}{
		{"nil ok", nil, []string{".pdf"}, false},
		{"no params", "x.pdf", nil, true},
		{"matches suffix", "report.pdf", []string{".pdf"}, false},
		{"matches alt suffix", "image.png", []string{".jpg", ".png"}, false},
		{"mismatch", "index.html", []string{".pdf"}, true},
		{"non-string", 1, []string{".pdf"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := EndsWithRule("file", tt.value, tt.params, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestPasswordRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		params  []string
		wantErr bool
	}{
		{"nil fails", nil, nil, true},
		{"empty fails", "", nil, true},
		{"short fails", "Aa1!", nil, true},
		{"missing upper fails", "aaaa1111!!!!", nil, true},
		{"missing lower fails", "AAAA1111!!!!", nil, true},
		{"missing digit fails", "Aaaaaaaaa!!!", nil, true},
		{"missing symbol fails", "Aaaaaa1111", nil, true},
		{"strong passes", "Passw0rd!", nil, false},
		{"length override passes", "VeryL0ngP@ssword", []string{"12"}, false},
		{"length override fails when too short", "Sh0rt!", []string{"12"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := PasswordRule("password", tt.value, tt.params, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}
