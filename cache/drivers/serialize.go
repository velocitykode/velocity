package drivers

import (
	"encoding/json"
	"fmt"
	"reflect"
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
//
// Buffer pooling (as applied to crypto's SerializePayload) is deliberately NOT
// used here. Pooling only pays off when the marshaled bytes are consumed and
// discarded inside the same call, so the scratch buffer can be returned to the
// pool before it returns; SerializePayload qualifies because it base64-encodes
// the JSON locally and never lets it escape. MarshalValue instead returns the
// raw JSON bytes to the calling store driver (redis SET, file write), so the
// slice escapes the function and could not be safely returned to a pool. On top
// of that, Go's json.Marshal already pools its own internal scratch buffer; the
// only remaining allocation is the result slice itself, which must escape by
// contract. There is no per-call allocation left here for pooling to remove.
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

// MatchesStoredValue reports whether stored, the bytes a serializing store
// holds for a key, carry the same value as expected, a value a read of that
// key returned. Both sides are compared in the shape a read produces
// (UnmarshalValue), not byte for byte: a struct is stored in field order but
// read back as a map whose keys re-serialize sorted, and a number is read
// back as float64, whose digits may differ from the stored ones. Comparing
// raw bytes would therefore refuse the unchanged value a read returned.
//
// Two stored values that no read of a serializing store can tell apart
// compare equal, so this is the equality only for the serializing stores,
// whose reads return that decoded shape. A store that returns the stored
// value itself (memory) compares it with reflect.DeepEqual instead, since
// its reads distinguish values this match merges, such as integers past
// float64 precision. A caller that must also guard against such a write
// swaps on the stored bytes it matched, so the write still lands only
// while the key holds exactly them.
func MatchesStoredValue(stored []byte, expected interface{}) (bool, error) {
	have, err := UnmarshalValue(stored)
	if err != nil {
		return false, fmt.Errorf("decode stored value: %w", err)
	}
	encoded, err := MarshalValue(expected)
	if err != nil {
		return false, fmt.Errorf("encode expected value: %w", err)
	}
	want, err := UnmarshalValue(encoded)
	if err != nil {
		return false, fmt.Errorf("decode expected value: %w", err)
	}
	return reflect.DeepEqual(have, want), nil
}
