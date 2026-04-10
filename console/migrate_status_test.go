package console

import (
	"testing"
)

func TestMigrateStatus_NilDB(t *testing.T) {
	err := MigrateStatus(nil)
	if err != nil {
		t.Fatalf("MigrateStatus(nil) returned error: %v", err)
	}
}
