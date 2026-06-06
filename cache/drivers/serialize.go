package drivers

import (
	"encoding/json"
	"unicode/utf8"
)

// rawStringFrame is the one-byte prefix marking a payload as raw string bytes
// rather than JSON. It is 0x00 -- a byte that can never begin a JSON value
// (JSON values start with '{', '[', '"', a digit, '-', 't', 'f', 'n', or
// whitespace) -- so a framed string is unambiguously distinguishable from any
// JSON-encoded value, with zero risk of colliding with a user-supplied value.
//
// Only strings that are not valid UTF-8 are framed: encoding/json silently
// rewrites invalid UTF-8 in a plain string to the U+FFFD replacement rune, so
// such a string would not round-trip byte-identically through the serializing
// drivers (redis, file). Framing stores the exact bytes verbatim. Valid
// strings, numbers, bools, maps, and structs are stored as plain JSON, which
// keeps the wire human-readable and -- crucially -- leaves numeric values as
// bare JSON integers so the redis driver's native INCRBY counters keep working.
const rawStringFrame byte = 0x00

// MarshalValue serializes a cache value to bytes for a serializing store.
// Invalid-UTF-8 strings are framed verbatim behind a 0x00 marker byte; every
// other value uses plain JSON.
func MarshalValue(value interface{}) ([]byte, error) {
	if s, ok := value.(string); ok && !utf8.ValidString(s) {
		framed := make([]byte, 0, len(s)+1)
		framed = append(framed, rawStringFrame)
		framed = append(framed, s...)
		return framed, nil
	}
	return json.Marshal(value)
}

// UnmarshalValue reverses MarshalValue: a 0x00-framed payload is returned as
// the exact original string; anything else is JSON-decoded.
func UnmarshalValue(data []byte) (interface{}, error) {
	if len(data) > 0 && data[0] == rawStringFrame {
		return string(data[1:]), nil
	}
	var value interface{}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}
