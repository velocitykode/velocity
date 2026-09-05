package markdown_test

import (
	"reflect"
	"testing"

	"github.com/velocitykode/velocity/markdown"
)

func TestParseFrontMatter(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		wantMeta map[string]any
		wantBody string
		wantErr  bool
	}{
		{"no front matter", "# Hi\n", map[string]any{}, "# Hi\n", false},
		{"empty source", "", map[string]any{}, "", false},
		{"simple", "---\ntitle: Hi\n---\nbody", map[string]any{"title": "Hi"}, "body", false},
		{"crlf", "---\r\ntitle: Hi\r\n---\r\nbody", map[string]any{"title": "Hi"}, "body", false},
		{"dots closing", "---\ntitle: Hi\n...\nbody", map[string]any{"title": "Hi"}, "body", false},
		{"closing fence at eof", "---\ntitle: Hi\n---", map[string]any{"title": "Hi"}, "", false},
		{"empty block", "---\n---\nbody", map[string]any{}, "body", false},
		{"whitespace only block", "---\n  \n---\nbody", map[string]any{}, "body", false},
		{"trailing spaces on fences", "---  \ntitle: Hi\n---\t\nbody", map[string]any{"title": "Hi"}, "body", false},
		{"bom stripped", "\xEF\xBB\xBF---\ntitle: Hi\n---\nbody", map[string]any{"title": "Hi"}, "body", false},
		{"bom without front matter", "\xEF\xBB\xBFbody", map[string]any{}, "body", false},
		{"typed values", "---\nweight: 2\ndraft: true\ntags: [a, b]\nnested:\n  k: v\n---\n",
			map[string]any{"weight": 2, "draft": true, "tags": []any{"a", "b"}, "nested": map[string]any{"k": "v"}}, "", false},
		{"unclosed is a thematic break", "---\ntitle: Hi\nbody", map[string]any{}, "---\ntitle: Hi\nbody", false},
		{"thematic break alone", "---", map[string]any{}, "---", false},
		{"dashes with text are not a fence", "--- title\n---\n", map[string]any{}, "--- title\n---\n", false},
		{"indented fence is not a fence", " ---\ntitle: Hi\n---\n", map[string]any{}, " ---\ntitle: Hi\n---\n", false},
		{"later fence pairs are not front matter", "text\n---\ntitle: Hi\n---\n", map[string]any{}, "text\n---\ntitle: Hi\n---\n", false},
		{"malformed yaml", "---\ntitle: [\n---\nbody", nil, "", true},
		{"scalar block", "---\njust a string\n---\nbody", nil, "", true},
		{"sequence block", "---\n- a\n- b\n---\nbody", nil, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, body, err := markdown.ParseFrontMatter([]byte(tt.src))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v; wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(meta, tt.wantMeta) {
				t.Errorf("meta = %#v; want %#v", meta, tt.wantMeta)
			}
			if string(body) != tt.wantBody {
				t.Errorf("body = %q; want %q", body, tt.wantBody)
			}
		})
	}
}

func TestParseFrontMatter_DoesNotAliasErrorResults(t *testing.T) {
	meta, body, err := markdown.ParseFrontMatter([]byte("---\n: bad\n---\n"))
	if err == nil {
		t.Fatal("expected error")
	}
	if meta != nil || body != nil {
		t.Errorf("meta, body = %v, %v on error; want nil, nil", meta, body)
	}
}
