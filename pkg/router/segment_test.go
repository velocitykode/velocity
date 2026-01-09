package router

import (
	"testing"
)

func TestParseSegments_StaticRoutes(t *testing.T) {
	t.Run("parses simple static path", func(t *testing.T) {
		segments, err := ParseSegments("/users")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(segments) != 1 {
			t.Fatalf("expected 1 segment, got %d", len(segments))
		}

		if segments[0].Type != SegmentStatic {
			t.Errorf("expected static segment, got %v", segments[0].Type)
		}

		if segments[0].Value != "users" {
			t.Errorf("expected value 'users', got %q", segments[0].Value)
		}
	})

	t.Run("parses deeply nested static path", func(t *testing.T) {
		segments, err := ParseSegments("/api/v1/admin/settings/notifications")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := []string{"api", "v1", "admin", "settings", "notifications"}
		if len(segments) != len(expected) {
			t.Fatalf("expected %d segments, got %d", len(expected), len(segments))
		}

		for i, exp := range expected {
			if segments[i].Value != exp {
				t.Errorf("segment[%d]: expected %q, got %q", i, exp, segments[i].Value)
			}
		}
	})

	t.Run("handles root path", func(t *testing.T) {
		segments, err := ParseSegments("/")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(segments) != 0 {
			t.Errorf("root path should produce empty segments, got %d", len(segments))
		}
	})

	t.Run("handles empty path", func(t *testing.T) {
		segments, err := ParseSegments("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(segments) != 0 {
			t.Errorf("empty path should produce empty segments, got %d", len(segments))
		}
	})

	t.Run("normalizes paths with trailing slashes", func(t *testing.T) {
		withSlash, _ := ParseSegments("/users/")
		withoutSlash, _ := ParseSegments("/users")

		if len(withSlash) != len(withoutSlash) {
			t.Error("trailing slash should not affect segment count")
		}
	})

	t.Run("normalizes paths without leading slash", func(t *testing.T) {
		withSlash, _ := ParseSegments("/users/posts")
		withoutSlash, _ := ParseSegments("users/posts")

		if len(withSlash) != len(withoutSlash) {
			t.Error("leading slash should not affect segment count")
		}
	})
}

func TestParseSegments_ParameterRoutes(t *testing.T) {
	t.Run("parses single parameter", func(t *testing.T) {
		segments, err := ParseSegments("/users/{id}")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(segments) != 2 {
			t.Fatalf("expected 2 segments, got %d", len(segments))
		}

		// First segment should be static
		if segments[0].Type != SegmentStatic || segments[0].Value != "users" {
			t.Errorf("first segment should be static 'users'")
		}

		// Second segment should be param
		if segments[1].Type != SegmentParam {
			t.Errorf("expected param segment, got %v", segments[1].Type)
		}

		if segments[1].Value != "id" {
			t.Errorf("expected param name 'id', got %q", segments[1].Value)
		}
	})

	t.Run("parses multiple parameters in path", func(t *testing.T) {
		segments, err := ParseSegments("/users/{userId}/posts/{postId}/comments/{commentId}")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify param segments extracted correctly
		paramNames := []string{}
		for _, seg := range segments {
			if seg.Type == SegmentParam {
				paramNames = append(paramNames, seg.Value)
			}
		}

		expected := []string{"userId", "postId", "commentId"}
		if len(paramNames) != len(expected) {
			t.Fatalf("expected %d params, got %d", len(expected), len(paramNames))
		}

		for i, exp := range expected {
			if paramNames[i] != exp {
				t.Errorf("param[%d]: expected %q, got %q", i, exp, paramNames[i])
			}
		}
	})

	t.Run("param segment matches any value", func(t *testing.T) {
		segments, _ := ParseSegments("/users/{id}")
		paramSeg := segments[1]

		// Should match various values
		testValues := []string{"123", "abc", "hello-world", "under_score", "MixedCase"}
		for _, val := range testValues {
			if !paramSeg.Match(val) {
				t.Errorf("param segment should match %q", val)
			}
		}
	})
}

func TestParseSegments_RegexConstrainedParameters(t *testing.T) {
	t.Run("parses numeric constraint", func(t *testing.T) {
		segments, err := ParseSegments("/users/{id:[0-9]+}")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		regexSeg := segments[1]
		if regexSeg.Type != SegmentRegex {
			t.Fatalf("expected regex segment, got %v", regexSeg.Type)
		}

		if regexSeg.Value != "id" {
			t.Errorf("expected param name 'id', got %q", regexSeg.Value)
		}

		if regexSeg.Pattern == nil {
			t.Fatal("regex pattern should be compiled")
		}

		// Test matching
		if !regexSeg.Match("123") {
			t.Error("should match '123'")
		}
		if !regexSeg.Match("999999") {
			t.Error("should match '999999'")
		}
		if regexSeg.Match("abc") {
			t.Error("should NOT match 'abc'")
		}
		if regexSeg.Match("12abc") {
			t.Error("should NOT match '12abc'")
		}
		if regexSeg.Match("") {
			t.Error("should NOT match empty string")
		}
	})

	t.Run("parses slug constraint", func(t *testing.T) {
		segments, err := ParseSegments("/posts/{slug:[a-z0-9-]+}")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		regexSeg := segments[1]

		if !regexSeg.Match("hello-world") {
			t.Error("should match 'hello-world'")
		}
		if !regexSeg.Match("post-123") {
			t.Error("should match 'post-123'")
		}
		if regexSeg.Match("Hello-World") {
			t.Error("should NOT match uppercase 'Hello-World'")
		}
		if regexSeg.Match("hello_world") {
			t.Error("should NOT match underscore 'hello_world'")
		}
	})

	t.Run("parses UUID constraint", func(t *testing.T) {
		segments, err := ParseSegments("/resources/{uuid:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}}")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		regexSeg := segments[1]

		if !regexSeg.Match("550e8400-e29b-41d4-a716-446655440000") {
			t.Error("should match valid UUID")
		}
		if regexSeg.Match("not-a-uuid") {
			t.Error("should NOT match invalid UUID")
		}
	})

	t.Run("returns error for invalid regex", func(t *testing.T) {
		_, err := ParseSegments("/users/{id:[invalid(}")
		if err == nil {
			t.Error("expected error for invalid regex pattern")
		}
	})

	t.Run("preserves raw pattern for URL generation", func(t *testing.T) {
		segments, _ := ParseSegments("/users/{id:[0-9]+}")
		regexSeg := segments[1]

		if regexSeg.RawPattern != "[0-9]+" {
			t.Errorf("expected raw pattern '[0-9]+', got %q", regexSeg.RawPattern)
		}
	})
}

func TestParseSegments_WildcardRoutes(t *testing.T) {
	t.Run("parses wildcard at end of path", func(t *testing.T) {
		segments, err := ParseSegments("/files/{path:.*}")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(segments) != 2 {
			t.Fatalf("expected 2 segments, got %d", len(segments))
		}

		wildcardSeg := segments[1]
		if wildcardSeg.Type != SegmentWildcard {
			t.Errorf("expected wildcard segment, got %v", wildcardSeg.Type)
		}

		if wildcardSeg.Value != "path" {
			t.Errorf("expected param name 'path', got %q", wildcardSeg.Value)
		}
	})

	t.Run("wildcard matches any path including slashes", func(t *testing.T) {
		segments, _ := ParseSegments("/files/{path:.*}")
		wildcardSeg := segments[1]

		testPaths := []string{
			"file.txt",
			"dir/file.txt",
			"deep/nested/path/to/file.txt",
			"",
		}

		for _, path := range testPaths {
			if !wildcardSeg.Match(path) {
				t.Errorf("wildcard should match %q", path)
			}
		}
	})
}

func TestParseSegments_ComplexRealWorldRoutes(t *testing.T) {
	t.Run("REST API versioned route", func(t *testing.T) {
		segments, err := ParseSegments("/api/{version:[v][0-9]+}/users/{id:[0-9]+}/posts")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should have: api, version, users, id, posts
		if len(segments) != 5 {
			t.Fatalf("expected 5 segments, got %d", len(segments))
		}

		// Test version segment
		versionSeg := segments[1]
		if !versionSeg.Match("v1") {
			t.Error("version should match 'v1'")
		}
		if !versionSeg.Match("v123") {
			t.Error("version should match 'v123'")
		}
		if versionSeg.Match("1") {
			t.Error("version should NOT match '1' (missing v prefix)")
		}

		// Test id segment
		idSeg := segments[3]
		if !idSeg.Match("42") {
			t.Error("id should match '42'")
		}
		if idSeg.Match("abc") {
			t.Error("id should NOT match 'abc'")
		}
	})

	t.Run("file serving route with wildcard", func(t *testing.T) {
		segments, err := ParseSegments("/static/{tenant:[a-z]+}/assets/{filepath:.*}")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Tenant must be lowercase letters
		tenantSeg := segments[1]
		if !tenantSeg.Match("acme") {
			t.Error("tenant should match 'acme'")
		}
		if tenantSeg.Match("Acme") {
			t.Error("tenant should NOT match 'Acme' (uppercase)")
		}
		if tenantSeg.Match("acme123") {
			t.Error("tenant should NOT match 'acme123' (has numbers)")
		}

		// Filepath is wildcard
		filepathSeg := segments[3]
		if filepathSeg.Type != SegmentWildcard {
			t.Error("filepath should be wildcard")
		}
	})
}

func TestBuildPath_URLGeneration(t *testing.T) {
	t.Run("builds simple static path", func(t *testing.T) {
		segments, _ := ParseSegments("/users")
		path, err := BuildPath(segments, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if path != "/users" {
			t.Errorf("expected '/users', got %q", path)
		}
	})

	t.Run("builds path with single parameter", func(t *testing.T) {
		segments, _ := ParseSegments("/users/{id}")
		path, err := BuildPath(segments, map[string]string{"id": "42"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if path != "/users/42" {
			t.Errorf("expected '/users/42', got %q", path)
		}
	})

	t.Run("builds path with multiple parameters", func(t *testing.T) {
		segments, _ := ParseSegments("/users/{userId}/posts/{postId}")
		path, err := BuildPath(segments, map[string]string{
			"userId": "1",
			"postId": "99",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if path != "/users/1/posts/99" {
			t.Errorf("expected '/users/1/posts/99', got %q", path)
		}
	})

	t.Run("URL-encodes special characters in parameters", func(t *testing.T) {
		segments, _ := ParseSegments("/search/{query}")
		path, err := BuildPath(segments, map[string]string{"query": "hello world"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if path != "/search/hello%20world" {
			t.Errorf("expected '/search/hello%%20world', got %q", path)
		}
	})

	t.Run("does not encode slashes in wildcard parameters", func(t *testing.T) {
		segments, _ := ParseSegments("/files/{path:.*}")
		path, err := BuildPath(segments, map[string]string{"path": "dir/subdir/file.txt"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Wildcard paths should preserve slashes
		if path != "/files/dir/subdir/file.txt" {
			t.Errorf("expected '/files/dir/subdir/file.txt', got %q", path)
		}
	})

	t.Run("returns error for missing parameter", func(t *testing.T) {
		segments, _ := ParseSegments("/users/{id}")
		_, err := BuildPath(segments, map[string]string{})
		if err == nil {
			t.Error("expected error for missing parameter")
		}
	})

	t.Run("returns error for partially missing parameters", func(t *testing.T) {
		segments, _ := ParseSegments("/users/{userId}/posts/{postId}")
		_, err := BuildPath(segments, map[string]string{"userId": "1"}) // missing postId
		if err == nil {
			t.Error("expected error for missing postId parameter")
		}
	})

	t.Run("builds root path from empty segments", func(t *testing.T) {
		segments, _ := ParseSegments("/")
		path, err := BuildPath(segments, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if path != "/" {
			t.Errorf("expected '/', got %q", path)
		}
	})

	t.Run("handles regex segments same as params for URL generation", func(t *testing.T) {
		segments, _ := ParseSegments("/users/{id:[0-9]+}")
		path, err := BuildPath(segments, map[string]string{"id": "123"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if path != "/users/123" {
			t.Errorf("expected '/users/123', got %q", path)
		}
	})
}

func TestSegmentType_String(t *testing.T) {
	tests := []struct {
		segType  SegmentType
		expected string
	}{
		{SegmentStatic, "static"},
		{SegmentParam, "param"},
		{SegmentRegex, "regex"},
		{SegmentWildcard, "wildcard"},
	}

	for _, tt := range tests {
		if got := tt.segType.String(); got != tt.expected {
			t.Errorf("SegmentType(%d).String() = %q, want %q", tt.segType, got, tt.expected)
		}
	}

	// Test unknown type
	unknown := SegmentType(99)
	if got := unknown.String(); got != "unknown" {
		t.Errorf("unknown SegmentType.String() = %q, want \"unknown\"", got)
	}
}

func TestSegment_Match(t *testing.T) {
	t.Run("static segment matches exact value only", func(t *testing.T) {
		seg := Segment{Type: SegmentStatic, Value: "users"}

		if !seg.Match("users") {
			t.Error("static segment should match exact value")
		}
		if seg.Match("posts") {
			t.Error("static segment should NOT match different value")
		}
		if seg.Match("Users") {
			t.Error("static segment should NOT match case-insensitive")
		}
		if seg.Match("") {
			t.Error("static segment should NOT match empty string")
		}
	})

	t.Run("param segment matches any value", func(t *testing.T) {
		seg := Segment{Type: SegmentParam, Value: "id"}

		if !seg.Match("123") {
			t.Error("param should match numbers")
		}
		if !seg.Match("abc") {
			t.Error("param should match letters")
		}
		if !seg.Match("hello-world") {
			t.Error("param should match with hyphens")
		}
	})

	t.Run("regex segment with nil pattern returns false", func(t *testing.T) {
		seg := Segment{Type: SegmentRegex, Value: "id", Pattern: nil}

		if seg.Match("123") {
			t.Error("regex with nil pattern should NOT match anything")
		}
	})

	t.Run("wildcard segment matches anything", func(t *testing.T) {
		seg := Segment{Type: SegmentWildcard, Value: "path"}

		if !seg.Match("") {
			t.Error("wildcard should match empty string")
		}
		if !seg.Match("file.txt") {
			t.Error("wildcard should match filename")
		}
		if !seg.Match("dir/subdir/file.txt") {
			t.Error("wildcard should match path with slashes")
		}
	})

	t.Run("unknown segment type returns false", func(t *testing.T) {
		seg := Segment{Type: SegmentType(99), Value: "unknown"}

		if seg.Match("anything") {
			t.Error("unknown segment type should NOT match")
		}
	})
}

func TestParseSegments_EdgeCases(t *testing.T) {
	t.Run("path with only parameter", func(t *testing.T) {
		segments, err := ParseSegments("/{id}")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(segments) != 1 {
			t.Fatalf("expected 1 segment, got %d", len(segments))
		}
		if segments[0].Type != SegmentParam {
			t.Errorf("expected param segment")
		}
	})

	t.Run("consecutive slashes normalized", func(t *testing.T) {
		segments, _ := ParseSegments("/api//users")
		// Empty segments should be filtered
		for _, seg := range segments {
			if seg.Value == "" && seg.Type == SegmentStatic {
				t.Error("empty static segment should be filtered")
			}
		}
	})
}

func TestBuildPath_EdgeCases(t *testing.T) {
	t.Run("nil params for static path", func(t *testing.T) {
		segments, _ := ParseSegments("/api/users")
		path, err := BuildPath(segments, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if path != "/api/users" {
			t.Errorf("expected /api/users, got %q", path)
		}
	})

	t.Run("extra params ignored", func(t *testing.T) {
		segments, _ := ParseSegments("/users/{id}")
		path, err := BuildPath(segments, map[string]string{
			"id":    "123",
			"extra": "ignored",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if path != "/users/123" {
			t.Errorf("expected /users/123, got %q", path)
		}
	})

	t.Run("missing wildcard param returns error", func(t *testing.T) {
		segments, _ := ParseSegments("/files/{path:.*}")
		_, err := BuildPath(segments, nil) // Missing path param
		if err == nil {
			t.Error("expected error for missing wildcard param")
		}
	})

	t.Run("missing regex param returns error", func(t *testing.T) {
		segments, _ := ParseSegments("/users/{id:[0-9]+}")
		_, err := BuildPath(segments, nil) // Missing id param
		if err == nil {
			t.Error("expected error for missing regex param")
		}
	})
}
