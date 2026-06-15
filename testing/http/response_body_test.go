package http_test

import (
	"testing"

	velhttp "github.com/velocitykode/velocity/testing/http"
)

// /hello returns the body "Hello, World!" (see newTestRouter in client_test.go).

func TestAssertSee(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantError bool
	}{
		{name: "see-hit", text: "Hello", wantError: false},
		{name: "see-miss-fails", text: "Goodbye", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mt := &mockT{}
			client := velhttp.NewTestClient(mt, newTestRouter())
			client.Get("/hello").AssertSee(tt.text)
			gotError := len(mt.errors) > 0
			if gotError != tt.wantError {
				t.Errorf("AssertSee(%q): gotError=%v, want=%v (errors=%v)", tt.text, gotError, tt.wantError, mt.errors)
			}
		})
	}
}

func TestAssertDontSee(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantError bool
	}{
		{name: "dontsee-hit-fails", text: "Hello", wantError: true},
		{name: "dontsee-miss", text: "Goodbye", wantError: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mt := &mockT{}
			client := velhttp.NewTestClient(mt, newTestRouter())
			client.Get("/hello").AssertDontSee(tt.text)
			gotError := len(mt.errors) > 0
			if gotError != tt.wantError {
				t.Errorf("AssertDontSee(%q): gotError=%v, want=%v (errors=%v)", tt.text, gotError, tt.wantError, mt.errors)
			}
		})
	}
}
