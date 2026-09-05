package markdown

import (
	"bytes"
	"fmt"

	"go.yaml.in/yaml/v3"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// ParseFrontMatter splits a YAML front matter block from the start of src.
// The block opens with a line holding only "---" and closes with a line
// holding only "---" or "...". meta is the decoded mapping and body is the
// source that follows the block. When src carries no front matter meta is an
// empty map and body is src unchanged; a leading UTF-8 byte order mark is
// dropped in both cases. Malformed YAML, or a block whose top level is not a
// mapping, returns an error.
func ParseFrontMatter(src []byte) (meta map[string]any, body []byte, err error) {
	src = bytes.TrimPrefix(src, utf8BOM)
	meta = map[string]any{}

	rest, ok := cutFrontMatterFence(src)
	if !ok {
		return meta, src, nil
	}

	offset := 0
	for offset <= len(rest) {
		end := bytes.IndexByte(rest[offset:], '\n')
		var line []byte
		next := len(rest)
		if end < 0 {
			line = rest[offset:]
		} else {
			line = rest[offset : offset+end]
			next = offset + end + 1
		}
		if isFrontMatterFence(line, true) {
			raw := rest[:offset]
			if len(bytes.TrimSpace(raw)) > 0 {
				if err := yaml.Unmarshal(raw, &meta); err != nil {
					return nil, nil, fmt.Errorf("velocity/markdown: front matter: %w", err)
				}
				if meta == nil {
					meta = map[string]any{}
				}
			}
			return meta, rest[next:], nil
		}
		if end < 0 {
			break
		}
		offset = next
	}
	// An opening fence with no closing fence is a thematic break, not front
	// matter.
	return meta, src, nil
}

// cutFrontMatterFence returns the bytes after the opening "---" line.
func cutFrontMatterFence(src []byte) ([]byte, bool) {
	if !bytes.HasPrefix(src, []byte("---")) {
		return nil, false
	}
	end := bytes.IndexByte(src, '\n')
	if end < 0 {
		return nil, false
	}
	if !isFrontMatterFence(src[:end], false) {
		return nil, false
	}
	return src[end+1:], true
}

func isFrontMatterFence(line []byte, allowDots bool) bool {
	line = bytes.TrimRight(line, " \t\r")
	if bytes.Equal(line, []byte("---")) {
		return true
	}
	return allowDots && bytes.Equal(line, []byte("..."))
}
