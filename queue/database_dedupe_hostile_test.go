package queue_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/queue"
)

// hostileMarshalJob is a job whose MarshalJSON runs code: user code the
// driver runs when it builds the payload.
type hostileMarshalJob struct {
	code *hostile.Code
}

func (j *hostileMarshalJob) Handle() error { return nil }

func (j *hostileMarshalJob) MarshalJSON() ([]byte, error) {
	j.code.Run()
	return json.Marshal(struct{}{})
}

var dedupeKeySeq atomic.Int64

func nextDedupeKey() string { return fmt.Sprintf("key-%d", dedupeKeySeq.Add(1)) }

// The database driver's dedupe push runs two pieces of user code: the
// job's own MarshalJSON and the job.queued listener. For every driver
// entry point they may call back into, one that panics, blocks, or makes
// that call must not deadlock the driver, and pushing works afterwards.
func TestDatabaseDriver_DedupePushWithHostileUserCode(t *testing.T) {
	entries := map[string]func(d queue.Driver){
		"Clear": func(d queue.Driver) { _ = d.Clear("q") },
		"Push":  func(d queue.Driver) { _ = d.PushCtx(context.Background(), &plainDedupeJob{}, "q") },
		"PushIfNotExists": func(d queue.Driver) {
			_ = d.(queue.DedupeAwarePusher).PushIfNotExistsCtx(context.Background(), &plainDedupeJob{}, nextDedupeKey(), "q")
		},
	}
	for _, source := range []string{"MarshalJSON", "listener"} {
		for _, mode := range hostile.Modes() {
			for name, entry := range entries {
				t.Run(source+"/"+mode.String()+"/"+name, func(t *testing.T) {
					d := newSQLiteContractDriver(t)
					code := hostile.New(t, mode, func() { entry(d) })
					var job queue.Job = &plainDedupeJob{}
					if source == "MarshalJSON" {
						job = &hostileMarshalJob{code: code}
					} else {
						d.(contract.EventDispatcherAware).SetEventDispatcher(hostile.NewDispatcher(code).Dispatch)
					}
					push := func() {
						_ = d.(queue.DedupeAwarePusher).PushIfNotExistsCtx(context.Background(), job, nextDedupeKey(), "q")
					}
					if mode == hostile.Block {
						go push()
						<-code.Entered()
						if !(source == "listener" && strings.HasPrefix(name, "Push")) {
							hostile.Within(t, hostile.Deadline, func() { entry(d) })
						}
					} else {
						hostile.Within(t, hostile.Deadline, push)
					}
					code.Release()
					code.Disarm()
					hostile.Within(t, hostile.Deadline, func() {
						if err := d.(queue.DedupeAwarePusher).PushIfNotExistsCtx(context.Background(), &plainDedupeJob{}, nextDedupeKey(), "q"); err != nil {
							t.Errorf("PushIfNotExistsCtx after a retry: %v", err)
						}
					})
				})
			}
		}
	}
}

// plainDedupeJob is a job with no user code.
type plainDedupeJob struct{}

func (plainDedupeJob) Handle() error { return nil }

func (plainDedupeJob) Failed(error) {}

func (j *hostileMarshalJob) Failed(error) {}
