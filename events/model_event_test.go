package events

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/trace"
)

// modelEventCapture records the ModelEvents a listener is handed.
type modelEventCapture struct {
	mu  sync.Mutex
	evs []*ModelEvent
}

func (c *modelEventCapture) Handle(_ context.Context, event interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if me, ok := event.(*ModelEvent); ok {
		c.evs = append(c.evs, me)
	}
	return nil
}

func (c *modelEventCapture) Async() bool { return false }

func (c *modelEventCapture) all() []*ModelEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*ModelEvent(nil), c.evs...)
}

// A nil model is an error from FireModelEvent and from the registry's
// Fire, never a panic, and reaches no observer and no listener.
// ObserveModel with a nil model does not panic either.
func TestFireModelEvent_NilModel(t *testing.T) {
	d := NewObservableDispatcher()
	obs := NewTestUserObserver()
	d.Observe("", obs)
	capture := &modelEventCapture{}
	d.Listen(OfType[*ModelEvent](), capture)

	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("FireModelEvent(nil) panicked: %v", p)
			}
		}()
		if err := d.FireModelEvent(context.Background(), "created", nil); !errors.Is(err, errNilModel) {
			t.Errorf("FireModelEvent(nil) = %v, want errNilModel", err)
		}
		if err := d.registry.Fire(context.Background(), "created", nil); !errors.Is(err, errNilModel) {
			t.Errorf("Fire(nil) = %v, want errNilModel", err)
		}
		d.ObserveModel(nil, obs)
	}()
	if len(obs.events) != 0 {
		t.Errorf("observer saw %v for a nil model", obs.events)
	}
	if got := capture.all(); len(got) != 0 {
		t.Errorf("listener saw %d ModelEvents for a nil model", len(got))
	}
}

// panickyModel is a user model whose every method panics. Naming the
// event reads the model's type only, so none of them runs.
type panickyModel struct{ ID int }

func (*panickyModel) Name() string                 { panic("Name ran") }
func (*panickyModel) String() string               { panic("String ran") }
func (*panickyModel) GetModelName() string         { panic("GetModelName ran") }
func (*panickyModel) TableName() string            { panic("TableName ran") }
func (*panickyModel) MarshalJSON() ([]byte, error) { panic("MarshalJSON ran") }
func (*panickyModel) Error() string                { panic("Error ran") }

func TestFireModelEvent_ModelWhoseMethodsPanic(t *testing.T) {
	d := NewObservableDispatcher()
	capture := &modelEventCapture{}
	d.Listen("panickymodel.updated", capture)

	for _, model := range []any{&panickyModel{ID: 1}, panickyModel{ID: 2}} {
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("FireModelEvent(%T) ran a model method: %v", model, p)
				}
			}()
			if err := d.FireModelEvent(context.Background(), "updated", model); err != nil {
				t.Fatalf("FireModelEvent(%T) = %v", model, err)
			}
		}()
	}
	got := capture.all()
	if len(got) != 2 {
		t.Fatalf("listener saw %d events, want 2", len(got))
	}
	for _, ev := range got {
		if ev.ModelType != "panickyModel" || ev.Action != "updated" || ev.Name() != "panickymodel.updated" {
			t.Errorf("event = %+v, want panickyModel updated as panickymodel.updated", ev)
		}
	}
}

// A ModelEvent carries the envelope of the context it was fired under,
// and a JSON round trip (what the queue does for a queued listener) keeps
// every field with its type: the name, action and model type as strings,
// the trace ids, and At.
func TestModelEvent_EnvelopeSurvivesTheQueueCodec(t *testing.T) {
	ctx, traceID, spanID := trace.StartTrace(context.Background())
	d := NewObservableDispatcher()
	capture := &modelEventCapture{}
	d.Listen("testuser.created", capture)
	if err := d.FireModelEvent(ctx, "created", &TestUser{ID: 1}); err != nil {
		t.Fatalf("FireModelEvent: %v", err)
	}
	got := capture.all()
	if len(got) != 1 {
		t.Fatalf("listener saw %d events, want 1", len(got))
	}
	ev := got[0]
	var m contract.EventMeta = ev.Meta()
	if m.Context != ctx || m.TraceID != traceID || m.SpanID != spanID || m.At.IsZero() {
		t.Fatalf("envelope = %+v, want the firing context, trace %q, span %q and a time", m, traceID, spanID)
	}

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ModelEvent
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Name() != "testuser.created" || back.Action != "created" || back.ModelType != "TestUser" {
		t.Errorf("after the codec: name %q action %q type %q", back.Name(), back.Action, back.ModelType)
	}
	if back.TraceID != ev.TraceID || back.SpanID != ev.SpanID || back.ParentID != ev.ParentID || !back.At.Equal(ev.At) {
		t.Errorf("after the codec the envelope reads %+v, want %+v", back.EventMeta, ev.EventMeta)
	}
	if back.Context != nil {
		t.Errorf("the context crossed the codec")
	}
}
