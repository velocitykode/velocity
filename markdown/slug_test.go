package markdown_test

import (
	"testing"

	"github.com/velocitykode/velocity/markdown"
)

func TestSlug(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Hello World", "hello-world"},
		{"  padded  ", "padded"},
		{"C++ & .NET: What's new?", "c--net-whats-new"},
		{"Café Déjà Vu", "café-déjà-vu"},
		{"snake_case and-kebab", "snake_case-and-kebab"},
		{"1. Numbered", "1-numbered"},
		{"Tabs\tand\nnewlines", "tabs-and-newlines"},
		{"emoji 🚀 dropped", "emoji--dropped"},
		{"日本語 見出し", "日本語-見出し"},
		{"", "section"},
		{"!!!", "section"},
		{"MiXeD CaSe", "mixed-case"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := markdown.Slug(tt.in); got != tt.want {
				t.Errorf("Slug(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}
