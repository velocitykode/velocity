package console

import (
	"testing"
)

func TestCacheClear_NilCache(t *testing.T) {
	err := CacheClear(nil)
	if err != nil {
		t.Fatalf("CacheClear(nil) returned error: %v", err)
	}
}
