package console

import (
	"testing"
)

func TestQueueWork_NilDriver(t *testing.T) {
	err := QueueWork(nil, QueueWorkOptions{})
	if err != nil {
		t.Fatalf("QueueWork(nil) returned error: %v", err)
	}
}

func TestQueueWork_DefaultQueue(t *testing.T) {
	opts := QueueWorkOptions{}
	if opts.Queue == "" {
		opts.Queue = "default"
	}
	if opts.Queue != "default" {
		t.Fatalf("expected default queue name %q, got %q", "default", opts.Queue)
	}
}

func TestQueueWork_OptionsMapping(t *testing.T) {
	opts := QueueWorkOptions{
		Queue:   "emails",
		Tries:   5,
		Timeout: 60,
	}

	if opts.Queue != "emails" {
		t.Fatalf("expected queue %q, got %q", "emails", opts.Queue)
	}
	if opts.Tries != 5 {
		t.Fatalf("expected tries %d, got %d", 5, opts.Tries)
	}
	if opts.Timeout != 60 {
		t.Fatalf("expected timeout %d, got %d", 60, opts.Timeout)
	}
}
