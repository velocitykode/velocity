package interceptors_test

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/internal/errchain"
)

// panickingError is a panic value whose Error panics.
type panickingError struct{}

func (panickingError) Error() string { panic("Error called") }

// PanicRecovered carries the panic's text; a panic value whose own Error
// panics is contained and the event carries the fixed unreadable text.
func TestCallLifecycle_PanicRecoveredCarriesThePanicText(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"string", "boom", "panic: boom"},
		{"error", context.Canceled, "panic: context canceled"},
		{"struct", struct{ Code int }{7}, "panic: {7}"},
		{"hostile error", panickingError{}, errchain.Unreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := &eventCollector{}
			pair := interceptors.CallLifecycle(interceptors.WithEventDispatcher(collector.dispatch))
			_, _ = pair.Unary(context.Background(), nil, mockUnaryServerInfo("/test.Service/Boom"), func(context.Context, interface{}) (interface{}, error) {
				panic(tc.value)
			})
			var seen *grpcevents.PanicRecovered
			for _, ev := range collector.snapshot() {
				if pe, ok := ev.(*grpcevents.PanicRecovered); ok {
					seen = pe
				}
			}
			if seen == nil {
				t.Fatal("PanicRecovered not dispatched")
			}
			if seen.PanicValue != tc.want {
				t.Errorf("PanicValue = %q, want %q", seen.PanicValue, tc.want)
			}
		})
	}
}
