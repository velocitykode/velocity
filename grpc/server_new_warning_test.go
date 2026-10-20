package grpc_test

import (
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/testnet"
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
	var s *grpc.Server
	noPanic(t, "NewServer", func() {
		s = grpc.NewServer(grpc.Config{MaxRecvMsgSize: -5}, grpc.WithLogger(warnPanicLogger{}), grpc.WithListener(testnet.Loopback(t)))
	})
	if s == nil {
		t.Fatal("no server")
	}
}
