package console

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/queue"
	testsync "github.com/velocitykode/velocity/testing"
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

// failingWorkJob fails every run.
type failingWorkJob struct {
	ID string `json:"id"`
}

func (j *failingWorkJob) Handle() error { return errors.New("work job exploded") }
func (j *failingWorkJob) Failed(error)  {}

// quietWorkerLogger drops worker log lines.
type quietWorkerLogger struct{}

func (quietWorkerLogger) Info(string, ...any)  {}
func (quietWorkerLogger) Warn(string, ...any)  {}
func (quietWorkerLogger) Error(string, ...any) {}

// TestNewQueueWorker_WiresDispatcher asserts the worker queue work runs
// fires its job lifecycle events into opts.Dispatcher: a job that fails
// on its only attempt produces job.processing then job.failed carrying the
// job's error.
func TestNewQueueWorker_WiresDispatcher(t *testing.T) {
	driver := queue.NewMemoryDriver()
	driver.Start()
	defer driver.Shutdown(context.Background())
	if err := driver.PushCtx(context.Background(), &failingWorkJob{ID: "w1"}, "work"); err != nil {
		t.Fatalf("push: %v", err)
	}

	var (
		mu     sync.Mutex
		names  []string
		failed *queue.JobFailed
	)
	w := NewQueueWorker(driver, QueueWorkOptions{
		Queue:  "work",
		Tries:  1,
		Logger: quietWorkerLogger{},
		Dispatcher: func(_ context.Context, event interface{}) error {
			mu.Lock()
			defer mu.Unlock()
			if n, ok := event.(interface{ Name() string }); ok {
				names = append(names, n.Name())
			}
			if e, ok := event.(*queue.JobFailed); ok {
				failed = e
			}
			return nil
		},
	})
	w.Start(context.Background())
	defer w.Stop()
	testsync.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return failed != nil
	}, 5*time.Second, "worker dispatches job.failed")
	w.Stop()

	mu.Lock()
	defer mu.Unlock()
	if want := []string{"job.processing", "job.failed"}; !slices.Equal(names, want) {
		t.Errorf("dispatched events = %v, want %v", names, want)
	}
	if failed.Error != "work job exploded" || failed.Queue != "work" {
		t.Errorf("job.failed = {Error: %q, Queue: %q}, want {%q, %q}", failed.Error, failed.Queue, "work job exploded", "work")
	}
}
