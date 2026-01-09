package router

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// SegmentType represents the type of path segment
type SegmentType int

const (
	SegmentStatic   SegmentType = iota // /users
	SegmentParam                       // /{id}
	SegmentRegex                       // /{id:[0-9]+}
	SegmentWildcard                    // /{path:.*}
)

// String returns the string representation of the segment type
func (st SegmentType) String() string {
	switch st {
	case SegmentStatic:
		return "static"
	case SegmentParam:
		return "param"
	case SegmentRegex:
		return "regex"
	case SegmentWildcard:
		return "wildcard"
	default:
		return "unknown"
	}
}

// Segment represents a parsed path segment
type Segment struct {
	Type       SegmentType
	Value      string         // segment value: "users" for static, "id" for param
	Pattern    *regexp.Regexp // compiled regex for SegmentRegex
	RawPattern string         // original pattern string for URL generation
}

// Match checks if the given value matches this segment
func (s *Segment) Match(value string) bool {
	switch s.Type {
	case SegmentStatic:
		return s.Value == value
	case SegmentParam:
		return true // params match any non-empty value
	case SegmentRegex:
		if s.Pattern == nil {
			return false
		}
		return s.Pattern.MatchString(value)
	case SegmentWildcard:
		return true // wildcards match anything including empty
	default:
		return false
	}
}

// ParseSegments parses a path pattern into segments
// Supports: /static, /{param}, /{param:regex}, /{param:.*}
func ParseSegments(path string) ([]Segment, error) {
	// Normalize path by trimming slashes
	path = strings.Trim(path, "/")

	if path == "" {
		return []Segment{}, nil
	}

	parts := strings.Split(path, "/")
	segments := make([]Segment, 0, len(parts))

	for _, part := range parts {
		if part == "" {
			continue
		}

		seg, err := parseSegment(part)
		if err != nil {
			return nil, err
		}
		segments = append(segments, seg)
	}

	return segments, nil
}

// parseSegment parses a single path segment
func parseSegment(part string) (Segment, error) {
	// Check if this is a parameter segment
	if !strings.HasPrefix(part, "{") || !strings.HasSuffix(part, "}") {
		// Static segment
		return Segment{
			Type:  SegmentStatic,
			Value: part,
		}, nil
	}

	// Parameter segment: {name} or {name:pattern}
	inner := part[1 : len(part)-1] // Remove { and }

	colonIdx := strings.Index(inner, ":")
	if colonIdx == -1 {
		// Simple parameter without constraint
		return Segment{
			Type:  SegmentParam,
			Value: inner,
		}, nil
	}

	// Parameter with constraint
	name := inner[:colonIdx]
	pattern := inner[colonIdx+1:]

	// Check for wildcard pattern
	if pattern == ".*" {
		return Segment{
			Type:  SegmentWildcard,
			Value: name,
		}, nil
	}

	// Regex constrained parameter
	re, err := regexp.Compile("^" + pattern + "$")
	if err != nil {
		return Segment{}, fmt.Errorf("invalid regex pattern %q: %w", pattern, err)
	}

	return Segment{
		Type:       SegmentRegex,
		Value:      name,
		Pattern:    re,
		RawPattern: pattern,
	}, nil
}

// BuildPath builds a URL path from segments and parameter values
func BuildPath(segments []Segment, params map[string]string) (string, error) {
	if len(segments) == 0 {
		return "/", nil
	}

	var result strings.Builder

	for _, seg := range segments {
		result.WriteString("/")

		switch seg.Type {
		case SegmentStatic:
			result.WriteString(seg.Value)

		case SegmentParam, SegmentRegex:
			val, ok := params[seg.Value]
			if !ok {
				return "", fmt.Errorf("missing parameter: %s", seg.Value)
			}
			result.WriteString(url.PathEscape(val))

		case SegmentWildcard:
			val, ok := params[seg.Value]
			if !ok {
				return "", fmt.Errorf("missing parameter: %s", seg.Value)
			}
			// Don't escape wildcards - they can contain slashes
			result.WriteString(val)
		}
	}

	return result.String(), nil
}
