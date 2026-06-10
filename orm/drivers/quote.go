package drivers

import "strings"

// quoteQualified quotes a possibly dot-qualified identifier segment by
// segment, so "users.email" compiles to a table/column reference
// (`users`.`email`) rather than one quoted name (`users.email`). Each
// segment is quoted with the grammar's dialect quoting via quoteSegment;
// a bare "*" segment is emitted unquoted so "users.*" stays a valid
// projection (`users`.*).
func quoteQualified(name string, quoteSegment func(string) string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		if p == "*" {
			continue
		}
		parts[i] = quoteSegment(p)
	}
	return strings.Join(parts, ".")
}
