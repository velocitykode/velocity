package grpc_test

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/grpc"
)

// A gateway a stop ended does not start again: Start and StartAsync
// return http.ErrServerClosed without marking it running.
func TestGatewayStart_AfterAStopReturnsErrServerClosed(t *testing.T) {
	for name, start := range map[string]func(*grpc.Gateway) error{
		"Start":      (*grpc.Gateway).Start,
		"StartAsync": (*grpc.Gateway).StartAsync,
	} {
		t.Run(name, func(t *testing.T) {
			sg := startSlowGateway(t, &gatewayLogger{gateway: &atomic.Pointer[grpc.Gateway]{}})
			sg.g.Stop()
			var err error
			within(t, 2*time.Second, name, func() { err = start(sg.g) })
			if !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("%s after Stop = %v, want http.ErrServerClosed", name, err)
			}
			if sg.g.IsRunning() {
				t.Error("gateway marked running after a refused start")
			}
		})
	}
}
