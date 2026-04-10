package console

import (
	"testing"
)

func TestDBWipe_NilDB(t *testing.T) {
	err := DBWipe(nil)
	if err != nil {
		t.Fatalf("DBWipe(nil) returned error: %v", err)
	}
}
