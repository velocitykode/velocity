package drivers

import (
	"database/sql/driver"
	"testing"
	"time"
)

// Bound values are copied into a statement's event when it runs: a []byte
// is cloned, so a caller reusing its buffer after the statement returned
// does not change the event, while nil and empty slices keep their shape
// and immutable values pass as they are.
func TestNamedValuesToAny_SnapshotsByteSlices(t *testing.T) {
	buf := []byte("original")
	empty := []byte{}
	var nilBytes []byte
	at := time.Unix(1, 0)
	out := namedValuesToAny([]driver.NamedValue{
		{Ordinal: 1, Value: buf},
		{Ordinal: 2, Value: empty},
		{Ordinal: 3, Value: nilBytes},
		{Ordinal: 4, Value: "text"},
		{Ordinal: 5, Value: int64(7)},
		{Ordinal: 6, Value: at},
		{Ordinal: 7, Value: nil},
	})
	copy(buf, "OVERRIDE")
	if b := out[0].([]byte); string(b) != "original" {
		t.Errorf("out[0] = %q, want the bytes at capture", b)
	}
	if b := out[1].([]byte); b == nil || len(b) != 0 {
		t.Errorf("out[1] = %#v, want a non-nil empty slice", b)
	}
	if b := out[2].([]byte); b != nil {
		t.Errorf("out[2] = %#v, want a nil slice", b)
	}
	if out[3] != "text" || out[4] != int64(7) || out[5] != at || out[6] != nil {
		t.Errorf("immutable values changed: %#v", out[3:])
	}
	if namedValuesToAny(nil) != nil {
		t.Error("no arguments must give nil")
	}
}
