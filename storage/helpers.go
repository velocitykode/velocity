package storage

import (
	"net/http"
	"net/url"
	"strings"
)

// DetectMimeType detects the MIME type from content using the standard
// library sniffer (net/http.DetectContentType), with a narrow SVG
// correction for markup the sniffer classifies as plain text. Sniffing
// only the first 512 bytes mirrors http.DetectContentType's own contract
// and bounds the work for very large objects.
func DetectMimeType(content []byte) string {
	if len(content) == 0 {
		return "application/octet-stream"
	}
	n := len(content)
	if n > 512 {
		n = 512
	}
	sample := content[:n]
	contentType := http.DetectContentType(sample)
	if strings.HasPrefix(strings.ToLower(contentType), "text/plain") && looksLikeSVG(sample) {
		return "image/svg+xml"
	}
	return contentType
}

func looksLikeSVG(content []byte) bool {
	s := strings.TrimSpace(strings.ToLower(string(content)))
	return strings.HasPrefix(s, "<svg") ||
		(strings.HasPrefix(s, "<?xml") && strings.Contains(s, "<svg"))
}

// EscapeURLPathSegments percent-encodes each `/`-delimited segment of
// path so reserved characters in keys cannot inject query / fragment
// state. `url.PathEscape` does NOT escape `/`, so a blanket call would
// destroy the separators; splitting first preserves the path shape
// while encoding every segment individually.
func EscapeURLPathSegments(path string) string {
	if path == "" {
		return ""
	}
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return strings.Join(segs, "/")
}
