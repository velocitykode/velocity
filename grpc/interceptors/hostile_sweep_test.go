package interceptors_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/internal/hostile"
)

// hostileReporter runs its code, then drops the report.
type hostileReporter struct{ code *hostile.Code }

func (r hostileReporter) Report(error, *contract.ErrorContext) { r.code.Run() }

// hostileStream is a server stream whose Context runs its code first.
type hostileStream struct {
	grpc.ServerStream
	ctx  context.Context
	code *hostile.Code
}

func (s *hostileStream) Context() context.Context {
	s.code.Run()
	return s.ctx
}

// The user code a call runs: the logger (its request line), the event
// dispatcher, the error reporter, the extra-fields func, the panic
// handler, an interceptor between the two CallLifecycle occurrences, the
// handler, and for a stream the stream's Context.
var callSites = []string{"logger", "dispatcher", "reporter", "extra", "panichandler", "interceptor", "handler", "streamcontext"}

// callSweep is one call chain with its user code hostile at one site.
type callSweep struct {
	kind, site string
	code       *hostile.Code
	calls      interceptors.InterceptorPair
}

func newCallSweep(t *testing.T, kind, site string, mode hostile.Mode) *callSweep {
	w := &callSweep{kind: kind, site: site}
	w.code = hostile.New(t, mode, func() {
		// A call into the same chain from its own user code.
		_ = w.call(context.Background(), nil)
	})
	at := func(s string) *hostile.Code {
		if s == site {
			return w.code
		}
		return nil
	}
	opts := []interceptors.CallOption{
		interceptors.WithRequestLine(),
		interceptors.WithStackTrace(false),
		interceptors.WithLogger(hostile.NewLogger(at("logger"))),
		interceptors.WithEventDispatcher(hostile.NewDispatcher(at("dispatcher")).Dispatch),
		interceptors.WithReporter(hostileReporter{code: at("reporter")}),
		interceptors.WithExtraFields(func(context.Context) []any {
			at("extra").Run()
			return []any{"extra", 1}
		}),
		interceptors.WithPanicHandler(func(context.Context, any) error {
			at("panichandler").Run()
			return status.Error(codes.Internal, "handled")
		}),
	}
	w.calls = interceptors.CallLifecycle(opts...)
	return w
}

// want is the status code a call ends with once its user code behaves.
func (w *callSweep) want() codes.Code {
	switch w.site {
	case "reporter", "panichandler":
		return codes.Internal
	}
	return codes.OK
}

// call runs one call through the chain. code, when non-nil, is the code
// the stream's Context runs.
func (w *callSweep) call(ctx context.Context, streamCode *hostile.Code) error {
	var mid *hostile.Code
	if w.site == "interceptor" {
		mid = w.code
	}
	var handlerCode *hostile.Code
	if w.site == "handler" {
		handlerCode = w.code
	}
	// The reporter runs for an Internal error, the panic handler for a
	// panic: the handler gives them one.
	work := func() error {
		handlerCode.Run()
		switch w.site {
		case "reporter":
			return status.Error(codes.Internal, "failed")
		case "panichandler":
			panic("handler broke")
		}
		return nil
	}
	if w.kind == "unary" {
		_, err := chainUnary(ctx, func(context.Context, any) (any, error) { return nil, work() },
			w.calls.Unary,
			interceptors.ContainUnary(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
				mid.Run()
				return h(ctx, req)
			}),
			w.calls.Unary)
		return err
	}
	ss := &hostileStream{ctx: ctx, code: streamCode}
	return chainStream(ss, func(any, grpc.ServerStream) error { return work() },
		w.calls.Stream,
		interceptors.ContainStream(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			mid.Run()
			return h(srv, ss)
		}),
		w.calls.Stream)
}

// streamCode is the code the stream's Context runs for this sweep's
// armed call.
func (w *callSweep) streamCode() *hostile.Code {
	if w.site == "streamcontext" {
		return w.code
	}
	return nil
}

// A unary or stream call through CallLifecycle and a contained
// interceptor survives user code that panics, blocks or calls the chain
// again at every site: no panic escapes, the call ends, a call running
// while the code blocks is not held by it, and a call once the code
// behaves ends as it should.
func TestCallLifecycle_HostileUserCodeSweep(t *testing.T) {
	for _, kind := range []string{"unary", "stream"} {
		for _, site := range callSites {
			if site == "streamcontext" && kind == "unary" {
				continue
			}
			for _, mode := range hostile.Modes() {
				t.Run(kind+"/"+site+"/"+mode.String(), func(t *testing.T) {
					body := func() { callSweepCase(t, kind, site, mode) }
					if mode == hostile.Panic {
						hostile.Isolated(t, body)
						return
					}
					body()
				})
			}
		}
	}
}

func callSweepCase(t *testing.T, kind, site string, mode hostile.Mode) {
	w := newCallSweep(t, kind, site, mode)
	done := make(chan struct{})
	var err error
	var panicked any
	go func() {
		defer close(done)
		panicked = hostile.Within(t, 10*time.Second, func() { err = w.call(context.Background(), w.streamCode()) })
	}()
	if mode == hostile.Block {
		select {
		case <-w.code.Entered():
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("the user code never ran")
		}
		// Another call does not wait on the blocked one.
		w.code.Disarm()
		var other error
		if p := hostile.Within(t, 2*time.Second, func() { other = w.call(context.Background(), nil) }); p != nil {
			t.Fatalf("a call beside the blocked one panicked: %v", p)
		}
		if status.Code(other) != w.want() {
			t.Errorf("a call beside the blocked one = %v, want %v", other, w.want())
		}
		w.code.Release()
	}
	select {
	case <-done:
	case <-time.After(hostile.Deadline):
		t.Fatalf("the call did not return within %v", hostile.Deadline)
	}
	if panicked != nil {
		t.Fatalf("a panic escaped the call: %v", panicked)
	}
	if mode == hostile.Panic {
		if c := status.Code(err); c != codes.OK && c != codes.Internal {
			t.Errorf("the call = %v, want OK or Internal", err)
		}
	} else if status.Code(err) != w.want() {
		t.Errorf("the call = %v, want %v", err, w.want())
	}

	w.code.Disarm()
	var again error
	if p := hostile.Within(t, 2*time.Second, func() { again = w.call(context.Background(), w.streamCode()) }); p != nil {
		t.Fatalf("a later call panicked: %v", p)
	}
	if status.Code(again) != w.want() {
		t.Errorf("a later call = %v, want %v", again, w.want())
	}
}
