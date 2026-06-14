package grpc

import (
	"context"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"github.com/velocitykode/velocity/grpc/interceptors"
)

// warnCaptureLogger records every Warn message so a test can assert whether the
// unauthenticated-surface warning fired. All methods are no-ops except Warn,
// which appends under a mutex so the concurrent Build/serve paths cannot race.
type warnCaptureLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *warnCaptureLogger) Debug(string, ...any) {}
func (l *warnCaptureLogger) Info(string, ...any)  {}
func (l *warnCaptureLogger) Error(string, ...any) {}
func (l *warnCaptureLogger) Fatal(string, ...any) {}

func (l *warnCaptureLogger) Warn(msg string, _ ...any) {
	l.mu.Lock()
	l.warns = append(l.warns, msg)
	l.mu.Unlock()
}

// unauthWarned reports whether the unauthenticated-surface warning was emitted.
func (l *warnCaptureLogger) unauthWarned() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, w := range l.warns {
		if strings.Contains(w, "serving all RPCs unauthenticated") {
			return true
		}
	}
	return false
}

// noopValidator satisfies interceptors.AuthValidator without doing real work;
// it is only needed so interceptors.Auth produces a pair to wire.
type noopValidator struct{}

func (noopValidator) ValidateToken(context.Context, string) (interceptors.Claims, error) {
	return &interceptors.BasicClaims{}, nil
}

// regNoop registers a do-nothing service so Build has something to warn about.
// The underlying *grpc.Server accepts the empty service description.
func regNoop(srv any) {
	srv.(*grpc.Server).RegisterService(&grpc.ServiceDesc{ServiceName: "test.Noop"}, nil)
}

// TestBuild_WarnsWhenServiceUnauthenticated: a server that registers
// services without any auth interceptor must emit a clear one-time warning that
// every RPC is served unauthenticated, and must stay silent once auth is wired
// (via UseAll(Auth(...)) or MarkAuthConfigured) or when nothing is registered.
func TestBuild_WarnsWhenServiceUnauthenticated(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(s *Server)
		wantWarn bool
	}{
		{
			name:     "service registered, no auth: warns",
			setup:    func(s *Server) { s.RegisterService(regNoop) },
			wantWarn: true,
		},
		{
			name: "auth via UseAll(Auth): silent",
			setup: func(s *Server) {
				s.RegisterService(regNoop)
				s.UseAll(interceptors.Auth(noopValidator{}, interceptors.WithPublicMethods()))
			},
			wantWarn: false,
		},
		{
			name: "auth via bare Use + MarkAuthConfigured: silent",
			setup: func(s *Server) {
				s.RegisterService(regNoop)
				auth := interceptors.Auth(noopValidator{}, interceptors.WithPublicMethods())
				s.Use(auth.Unary).UseStream(auth.Stream).MarkAuthConfigured()
			},
			wantWarn: false,
		},
		{
			name: "non-auth interceptors only: warns",
			setup: func(s *Server) {
				s.RegisterService(regNoop)
				s.UseAll(interceptors.Recovery(), interceptors.Logging())
			},
			wantWarn: true,
		},
		{
			name:     "no services registered: silent (nothing to protect)",
			setup:    func(s *Server) {},
			wantWarn: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &warnCaptureLogger{}
			s := NewServer(WithPort("0"), WithLogger(logger))
			tt.setup(s)

			if err := s.Build(); err != nil {
				t.Fatalf("Build: unexpected error %v", err)
			}
			defer s.Stop()

			if got := logger.unauthWarned(); got != tt.wantWarn {
				t.Errorf("unauth warning fired = %v, want %v (warns: %v)", got, tt.wantWarn, logger.warns)
			}
		})
	}
}

// TestBuild_UnauthWarningIsOneShot verifies the warning fires once: Build is
// idempotent (early-returns once the server is built), so a second Build does
// not append a duplicate warning.
func TestBuild_UnauthWarningIsOneShot(t *testing.T) {
	logger := &warnCaptureLogger{}
	s := NewServer(WithPort("0"), WithLogger(logger))
	s.RegisterService(regNoop)

	if err := s.Build(); err != nil {
		t.Fatalf("first Build: %v", err)
	}
	if err := s.Build(); err != nil {
		t.Fatalf("second Build: %v", err)
	}
	defer s.Stop()

	var count int
	logger.mu.Lock()
	for _, w := range logger.warns {
		if strings.Contains(w, "serving all RPCs unauthenticated") {
			count++
		}
	}
	logger.mu.Unlock()

	if count != 1 {
		t.Errorf("unauth warning fired %d times, want exactly 1", count)
	}
}
