package drivers

import "testing"

func TestAllowClientEvent_RateLimit(t *testing.T) {
	d := &WebSocketDriver{}

	// A fresh bucket starts full at clientEventBurst; that many rapid whispers
	// are allowed, then the next is denied (refill over microseconds is < 1).
	for i := 0; i < int(clientEventBurst); i++ {
		if !d.allowClientEvent("c1") {
			t.Fatalf("whisper %d should be allowed within burst", i)
		}
	}
	if d.allowClientEvent("c1") {
		t.Fatalf("whisper beyond burst should be denied")
	}

	// A different client has its own independent bucket.
	if !d.allowClientEvent("c2") {
		t.Fatalf("independent client should not be rate-limited by another's usage")
	}
}

func TestAllowClientEvent_BucketEvictedOnPurge(t *testing.T) {
	d := &WebSocketDriver{}
	_ = d.allowClientEvent("c1")

	d.clientEventMu.Lock()
	_, present := d.clientEventBuckets["c1"]
	d.clientEventMu.Unlock()
	if !present {
		t.Fatalf("bucket should exist after a whisper")
	}

	d.purgeClient("c1")

	d.clientEventMu.Lock()
	_, present = d.clientEventBuckets["c1"]
	d.clientEventMu.Unlock()
	if present {
		t.Fatalf("bucket must be evicted on purgeClient to keep the map bounded")
	}
}
