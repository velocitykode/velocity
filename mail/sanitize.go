package mail

import "strings"

// SanitizeHeader drops every C0 control character (U+0000..U+001F) except
// horizontal tab from a header value. The previous per-driver implementations
// stripped only CR/LF, which let NUL and other C0 bytes through. NUL in
// particular can truncate strings in downstream C parsers (e.g. sendmail,
// libesmtp) and enable header-injection vectors a simple CRLF check misses.
// DEL (U+007F) is dropped as well since several older MTAs choke on it.
func SanitizeHeader(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
}

// SanitizeFilename removes characters that could cause injection in MIME
// headers: CR/LF (header splitting), and double-quote / backslash (which would
// break out of a quoted filename parameter in Content-Disposition).
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, "\"", "")
	name = strings.ReplaceAll(name, "\\", "")
	return name
}
