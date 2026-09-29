package grpc_test

import (
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
)

// warnPanicLogger panics on every warning.
type warnPanicLogger struct{}

func (warnPanicLogger) Warn(string, ...any)           { panic("logger broke") }
func (warnPanicLogger) Debug(string, ...any)          {}
func (warnPanicLogger) Info(string, ...any)           {}
func (warnPanicLogger) Error(string, ...any)          {}
func (warnPanicLogger) Fatal(string, ...any)          {}
func (l warnPanicLogger) With(...any) contract.Logger { return l }

// A logger that panics while NewServer writes a configuration warning
// does not escape NewServer: the line falls back and the server is built.
func TestNewServer_PanickingLoggerOnAConfigWarning(t *testing.T) {
	t.Setenv("GRPC_MAX_RECV_SIZE", "not-a-number")
	var s *grpc.Server
	noPanic(t, "NewServer", func() { s = grpc.NewServer(grpc.WithLogger(warnPanicLogger{}), grpc.WithListener(loopback(t))) })
	if s == nil {
		t.Fatal("no server")
	}
}
