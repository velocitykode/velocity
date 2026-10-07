package queue_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/queue/queuetest"
)

// liveStateJob keeps its behaviour in an unexported closure. Its exported
// state marshals and its type is registered, so every driver accepts and
// redelivers it; what differs is whether the closure arrives.
type liveStateJob struct {
	ID   string `json:"id"`
	live func() string
}

func (j *liveStateJob) Handle() error { return nil }
func (*liveStateJob) Failed(error)    {}
func (j *liveStateJob) JobID() string { return j.ID }

func init() {
	queue.RegisterJob(func(data []byte) (*liveStateJob, error) {
		j := &liveStateJob{}
		return j, json.Unmarshal(data, j)
	})
}

// TestDrivers_PreservesJobInstance_LiveState pushes a job whose behaviour is
// an unexported closure through each first-party driver and holds what comes
// back to the driver's own answer: a driver answering true delivers the
// pushed value with the closure set. For a driver answering false the test
// asserts only the answer and logs what was delivered.
func TestDrivers_PreservesJobInstance_LiveState(t *testing.T) {
	drivers := []struct {
		name string
		want bool
		make func(t *testing.T) queue.Driver
	}{
		{"memory", true, func(t *testing.T) queue.Driver {
			d := queue.NewMemoryDriver()
			t.Cleanup(func() { _ = d.Shutdown(contractShutdownCtx(t)) })
			return d
		}},
		{"fake", true, func(*testing.T) queue.Driver { return queuetest.NewFakeQueue() }},
		{"database", false, newSQLiteContractDriver},
		{"redis", false, newMiniredisContractDriver},
	}
	for _, tc := range drivers {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.make(t)
			if got := d.PreservesJobInstance(); got != tc.want {
				t.Fatalf("PreservesJobInstance = %v; want %v", got, tc.want)
			}
			ctx := context.Background()
			in := &liveStateJob{ID: "live-1", live: func() string { return "alive" }}
			if err := d.PushCtx(ctx, in, "q-live"); err != nil {
				t.Fatalf("push: %v", err)
			}
			popped, err := d.PopCtx(ctx, "q-live")
			if err != nil {
				t.Fatalf("pop: %v", err)
			}
			out, ok := popped.(*liveStateJob)
			if !ok {
				t.Fatalf("popped %T; want *liveStateJob", popped)
			}
			t.Logf("%s: same value = %v, closure set = %v, ID = %q", tc.name, out == in, out.live != nil, out.ID)
			if !tc.want {
				return
			}
			if out != in {
				t.Fatalf("popped %p; want the pushed value %p", out, in)
			}
			if out.live == nil || out.live() != "alive" {
				t.Fatal("popped job lost its closure")
			}
		})
	}
}

// Jobs for the memory driver's acceptance table. Each differs only in how
// its live state is declared.
type (
	unexportedLiveJob struct {
		ID   string
		fn   func()
		done chan struct{}
	}
	exportedFuncJob struct {
		ID string
		Fn func()
	}
	exportedChanJob struct {
		ID   string
		Done chan struct{}
	}
	exportedIfaceFuncJob struct {
		ID    string
		State any
	}
	ignoredFuncJob struct {
		ID string
		Fn func() `json:"-"`
	}
	omitzeroFuncJob struct {
		ID string
		Fn func() `json:",omitzero"`
	}
	omitemptyFuncJob struct {
		ID string
		Fn func() `json:",omitempty"`
	}
	marshalErrJob   struct{ ID string }
	unregisteredJob struct{ ID string }
)

func (*unexportedLiveJob) Handle() error    { return nil }
func (*unexportedLiveJob) Failed(error)     {}
func (*exportedFuncJob) Handle() error      { return nil }
func (*exportedFuncJob) Failed(error)       {}
func (*exportedChanJob) Handle() error      { return nil }
func (*exportedChanJob) Failed(error)       {}
func (*exportedIfaceFuncJob) Handle() error { return nil }
func (*exportedIfaceFuncJob) Failed(error)  {}
func (*ignoredFuncJob) Handle() error       { return nil }
func (*ignoredFuncJob) Failed(error)        {}
func (*omitzeroFuncJob) Handle() error      { return nil }
func (*omitzeroFuncJob) Failed(error)       {}
func (*omitemptyFuncJob) Handle() error     { return nil }
func (*omitemptyFuncJob) Failed(error)      {}
func (*marshalErrJob) Handle() error        { return nil }
func (*marshalErrJob) Failed(error)         {}
func (*unregisteredJob) Handle() error      { return nil }
func (*unregisteredJob) Failed(error)       {}

var errMarshalRefused = errors.New("marshal refused")

func (*marshalErrJob) MarshalJSON() ([]byte, error) { return nil, errMarshalRefused }

// TestMemoryDriver_PushAcceptance records which jobs the memory driver
// accepts on push. It keeps the pushed value, but it also marshals the job
// (createJobWrapper), so a job json.Marshal refuses is rejected even though
// its payload is never read back. Live state is accepted when json.Marshal
// does not see it (unexported, tagged "-", or a nil field tagged omitzero)
// and rejected when it does. A rejected push stores nothing.
func TestMemoryDriver_PushAcceptance(t *testing.T) {
	fn := func() {}
	cases := []struct {
		name    string
		job     queue.Job
		wantErr string // substring of the push error; empty means accepted
	}{
		{"unexported closure and channel", &unexportedLiveJob{ID: "a", fn: fn, done: make(chan struct{})}, ""},
		{"exported closure tagged json:\"-\"", &ignoredFuncJob{ID: "b", Fn: fn}, ""},
		{"exported nil closure tagged omitzero", &omitzeroFuncJob{ID: "c"}, ""},
		{"type with no registered factory", &unregisteredJob{ID: "d"}, ""},
		{"exported closure", &exportedFuncJob{ID: "e", Fn: fn},
			"velocity/queue: failed to marshal job *queue_test.exportedFuncJob: json: unsupported type: func()"},
		{"exported nil closure", &exportedFuncJob{ID: "f"},
			"velocity/queue: failed to marshal job *queue_test.exportedFuncJob: json: unsupported type: func()"},
		{"exported nil closure tagged omitempty", &omitemptyFuncJob{ID: "g"},
			"velocity/queue: failed to marshal job *queue_test.omitemptyFuncJob: json: unsupported type: func()"},
		{"exported set closure tagged omitzero", &omitzeroFuncJob{ID: "h", Fn: fn},
			"velocity/queue: failed to marshal job *queue_test.omitzeroFuncJob: json: unsupported type: func()"},
		{"exported channel", &exportedChanJob{ID: "i", Done: make(chan struct{})},
			"velocity/queue: failed to marshal job *queue_test.exportedChanJob: json: unsupported type: chan struct {}"},
		{"exported interface holding a closure", &exportedIfaceFuncJob{ID: "j", State: fn},
			"velocity/queue: failed to marshal job *queue_test.exportedIfaceFuncJob: json: unsupported type: func()"},
		{"MarshalJSON returning an error", &marshalErrJob{ID: "k"},
			"velocity/queue: failed to marshal job *queue_test.marshalErrJob: json: error calling MarshalJSON for type *queue_test.marshalErrJob: marshal refused"},
	}
	pushes := []struct {
		name string
		push func(d *queue.MemoryDriver, job queue.Job, q string) error
	}{
		{"PushCtx", func(d *queue.MemoryDriver, job queue.Job, q string) error {
			return d.PushCtx(context.Background(), job, q)
		}},
		{"PushDelayedCtx", func(d *queue.MemoryDriver, job queue.Job, q string) error {
			return d.PushDelayedCtx(context.Background(), job, 0, q)
		}},
		{"PushIfNotExistsCtx", func(d *queue.MemoryDriver, job queue.Job, q string) error {
			return d.PushIfNotExistsCtx(context.Background(), job, "acceptance-key", q)
		}},
	}
	for _, tc := range cases {
		for _, p := range pushes {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				d := queue.NewMemoryDriver()
				t.Cleanup(func() { _ = d.Shutdown(contractShutdownCtx(t)) })
				const q = "q-accept"
				err := p.push(d, tc.job, q)
				if tc.wantErr == "" {
					if err != nil {
						t.Fatalf("push refused: %v", err)
					}
					return
				}
				if err == nil {
					t.Fatal("push accepted; want it refused")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("push error = %q; want it to contain %q", err, tc.wantErr)
				}
				if n, _ := d.Size(q); n != 0 {
					t.Fatalf("refused push left %d job(s) in the queue", n)
				}
				if job, _ := d.PopCtx(context.Background(), q); job != nil {
					t.Fatalf("refused push is poppable: %T", job)
				}
			})
		}
	}
}
