package grpc_test

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/testnet"
)

// warningStopLogger stops its server from the unauthenticated-surface
// warning Build writes after it published the server.
type warningStopLogger struct{ server *atomic.Pointer[grpc.Server] }

func (l *warningStopLogger) Warn(msg string, _ ...any) {
	if strings.Contains(msg, "unauthenticated") {
		l.server.Load().Stop()
	}
}
func (l *warningStopLogger) Debug(string, ...any)        {}
func (l *warningStopLogger) Info(string, ...any)         {}
func (l *warningStopLogger) Error(string, ...any)        {}
func (l *warningStopLogger) Fatal(string, ...any)        {}
func (l *warningStopLogger) With(...any) contract.Logger { return l }

// A Stop from a warning Build writes after it published the server leaves
// nothing to serve: Start and StartAsync return ErrServerStopped instead
// of serving a nil server.
func TestServerStart_StoppedAfterBuildReturnsErrServerStopped(t *testing.T) {
	for name, start := range map[string]func(*grpc.Server) error{
		"Start":      (*grpc.Server).Start,
		"StartAsync": (*grpc.Server).StartAsync,
	} {
		t.Run(name, func(t *testing.T) {
			var ref atomic.Pointer[grpc.Server]
			s := grpc.NewServer(grpc.WithListener(testnet.Loopback(t)), grpc.WithLogger(&warningStopLogger{server: &ref}))
			ref.Store(s)
			s.RegisterService(regNoopExternal)
			stopOnCleanup(t, s)
			var err error
			if p := hostile.Within(t, 2*time.Second, func() {
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("%s panicked: %v", name, p)
					}
				}()
				err = start(s)
			}); p != nil {
				t.Fatalf("%s panicked: %v", name, p)
			}
			if !errors.Is(err, grpcgo.ErrServerStopped) {
				t.Errorf("%s = %v, want ErrServerStopped", name, err)
			}
			if s.IsRunning() {
				t.Error("server marked running")
			}
		})
	}
}

func regNoopExternal(any) {}

// Builds, Starts and Stops racing on many goroutines never serve a nil
// server or leave a listener open once a last Stop has run.
func TestServerStart_RacingBuildStartStop(t *testing.T) {
	for range 30 {
		lis := &closeTracker{Listener: testnet.Loopback(t)}
		s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
		var wg sync.WaitGroup
		for i := range 9 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("panicked: %v", p)
					}
				}()
				switch i % 3 {
				case 0:
					_ = s.Build()
				case 1:
					_ = s.StartAsync()
				default:
					s.Stop()
				}
			}()
		}
		wg.Wait()
		s.Stop()
		waitFor(t, "the listener to close", lis.closed.Load)
	}
}
