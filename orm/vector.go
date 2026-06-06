package orm

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
)

// Vector is a dense float32 embedding stored in a PostgreSQL pgvector column.
//
// It implements driver.Valuer and sql.Scanner so it round-trips through the
// ORM with no special-casing in the hydration path: on write it renders the
// pgvector text literal "[1,2,3]" (bound as a normal parameter, which the
// vector column or an explicit ::vector cast converts server-side); on read it
// parses that same literal back from the driver's text representation.
//
// Declare a model field as orm.Vector and tag the column type:vector(N) so the
// migration emits a vector(N) column:
//
//	type Document struct {
//	    orm.Model[Document]
//	    Embedding orm.Vector `orm:"type:vector(1536)"`
//	}
//
// A nil Vector renders as SQL NULL; use a non-nil empty Vector only if the
// column genuinely permits a zero-dimension vector (pgvector does not, so a nil
// Vector against a NOT NULL column will surface a database error on insert).
type Vector []float32

// Value implements driver.Valuer. A nil Vector becomes SQL NULL; otherwise it
// renders the pgvector text literal "[v0,v1,...]" using the shortest decimal
// representation that round-trips each float32.
func (v Vector) Value() (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	return v.String(), nil
}

// String returns the pgvector text literal for v, e.g. "[1,2,3]". A nil Vector
// returns "[]".
func (v Vector) String() string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// Scan implements sql.Scanner. It accepts the pgvector text representation as a
// string, []byte, or nil (NULL, which yields a nil Vector). Any other source
// type, or a malformed literal, returns an error rather than silently
// corrupting the field.
func (v *Vector) Scan(src any) error {
	if src == nil {
		*v = nil
		return nil
	}

	var s string
	switch t := src.(type) {
	case string:
		s = t
	case []byte:
		s = string(t)
	default:
		return fmt.Errorf("orm.Vector: cannot scan %T into Vector", src)
	}

	parsed, err := parseVector(s)
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}

// parseVector parses a pgvector text literal "[v0,v1,...]" into a Vector. It
// tolerates surrounding whitespace and whitespace around elements. An empty
// literal ("[]") parses to a non-nil, zero-length Vector.
func parseVector(s string) (Vector, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, fmt.Errorf("orm.Vector: malformed literal %q: expected [..]", s)
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return Vector{}, nil
	}
	parts := strings.Split(inner, ",")
	out := make(Vector, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, fmt.Errorf("orm.Vector: malformed element %q: %w", p, err)
		}
		out[i] = float32(f)
	}
	return out, nil
}
