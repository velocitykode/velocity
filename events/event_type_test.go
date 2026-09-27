package events

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

type shipmentFailed struct{}

func (e *shipmentFailed) Name() string                        { return "shop.shipment.failed" }
func (e *shipmentFailed) FailureError() error                 { return errors.New("carrier rejected the parcel") }
func (e *shipmentFailed) FailureSource() contract.ErrorSource { return contract.ErrorSourceJob }

type paymentFailed struct{}

func (e *paymentFailed) Name() string                        { return "shop.payment.failed" }
func (e *paymentFailed) FailureError() error                 { return errors.New("card declined") }
func (e *paymentFailed) FailureSource() contract.ErrorSource { return contract.ErrorSourceJob }

type orderPlaced struct{}

func (e *orderPlaced) Name() string { return "shop.order.placed" }

// shipmentFailedTwin shares shipmentFailed's name but is another type.
type shipmentFailedTwin struct{}

func (e *shipmentFailedTwin) Name() string { return "shop.shipment.failed" }

// dispatchAll dispatches every event through d.
func dispatchAll(t *testing.T, d contract.Dispatcher, evs ...interface{}) {
	t.Helper()
	for _, ev := range evs {
		if err := d.Dispatch(context.Background(), ev); err != nil {
			t.Fatalf("Dispatch(%T): %v", ev, err)
		}
	}
}

// received returns the Go types of the events l handled, in order.
func received(l *firedListener) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.events))
	for i, ev := range l.events {
		out[i] = fmt.Sprintf("%T", ev)
	}
	return out
}

// TestListen_OfTypeSelectsExactlyThatType subscribes by a concrete type: the
// listener receives that type and nothing else, not another type sharing
// its name and not the value form of a pointer type.
func TestListen_OfTypeSelectsExactlyThatType(t *testing.T) {
	d := NewDispatcher()
	l := &firedListener{}
	d.Listen(OfType[*shipmentFailed](), l)

	dispatchAll(t, d, &shipmentFailed{}, &shipmentFailedTwin{}, shipmentFailed{}, &orderPlaced{}, "shop.shipment.failed")

	if got, want := received(l), []string{"*events.shipmentFailed"}; !slices.Equal(got, want) {
		t.Fatalf("received %v, want %v", got, want)
	}
}

// TestListen_OfTypeInterfaceSelectsEveryImplementer subscribes by a facet:
// the listener receives every event implementing it, whatever its name.
func TestListen_OfTypeInterfaceSelectsEveryImplementer(t *testing.T) {
	d := NewDispatcher()
	l := &firedListener{}
	d.Listen(OfType[contract.FailureEvent](), l)

	dispatchAll(t, d, &shipmentFailed{}, &orderPlaced{}, &paymentFailed{}, &AsyncFailed{Error: "boom"}, "shop.refund.failed")

	if got, want := received(l), []string{"*events.shipmentFailed", "*events.paymentFailed", "*events.AsyncFailed"}; !slices.Equal(got, want) {
		t.Fatalf("received %v, want %v", got, want)
	}
}

// TestListen_OfTypeAfterDispatchAndOff registers a type listener after the
// event's resolution was cached and removes it again: both take effect on
// the next dispatch.
func TestListen_OfTypeAfterDispatchAndOff(t *testing.T) {
	d := NewDispatcher()
	named := &firedListener{}
	d.Listen("shop.order.placed", named)
	dispatchAll(t, d, &orderPlaced{}) // prime the cache

	typed := &firedListener{}
	id := d.Listen(OfType[*orderPlaced](), typed)
	dispatchAll(t, d, &orderPlaced{})
	if typed.count() != 1 {
		t.Fatalf("type listener registered after a dispatch fired %d times, want 1", typed.count())
	}
	if !d.HasListeners(&orderPlaced{}) {
		t.Fatal("HasListeners = false with a type listener registered")
	}

	if !d.Off(id) {
		t.Fatal("Off(type listener) = false, want true")
	}
	if d.Off(id) {
		t.Fatal("second Off(type listener) = true, want false")
	}
	dispatchAll(t, d, &orderPlaced{})
	if typed.count() != 1 {
		t.Fatalf("type listener fired %d times after Off, want 1", typed.count())
	}
	if named.count() != 3 {
		t.Fatalf("name listener fired %d times, want 3", named.count())
	}
}

// TestListen_ZeroEventTypePanics rejects a key not built by OfType.
func TestListen_ZeroEventTypePanics(t *testing.T) {
	for _, d := range []contract.Dispatcher{NewDispatcher(), NewFakeDispatcher()} {
		func() {
			defer func() {
				var regErr *contract.RegistrationError
				r := recover()
				if err, ok := r.(error); !ok || !errors.As(err, &regErr) {
					t.Errorf("%T.Listen(EventType{}) recovered %v, want *contract.RegistrationError", d, r)
				}
			}()
			d.Listen(EventType{}, &firedListener{})
		}()
	}
}

// TestFakeDispatcher_OfTypeKeys selects recorded events by type and by facet
// with the same keys Listen takes.
func TestFakeDispatcher_OfTypeKeys(t *testing.T) {
	f := NewFakeDispatcher()
	dispatchAll(t, f, &shipmentFailed{}, &orderPlaced{}, &paymentFailed{})

	if err := f.AssertDispatched(OfType[*shipmentFailed](), nil); err != nil {
		t.Errorf("AssertDispatched(OfType[*shipmentFailed]): %v", err)
	}
	if err := f.AssertDispatchedTimes(OfType[contract.FailureEvent](), 2); err != nil {
		t.Errorf("AssertDispatchedTimes(OfType[contract.FailureEvent], 2): %v", err)
	}
	if err := f.AssertNotDispatched(OfType[*shipmentFailedTwin]()); err != nil {
		t.Errorf("AssertNotDispatched(OfType[*shipmentFailedTwin]): %v", err)
	}
	if err := f.AssertDispatched(OfType[shipmentFailed](), nil); err == nil {
		t.Error("AssertDispatched(OfType[shipmentFailed]) = nil; only the pointer form was dispatched")
	}
	if err := f.AssertDispatched(EventType{}, nil); err == nil {
		t.Error("AssertDispatched(EventType{}) = nil; the zero key selects nothing")
	}
}
