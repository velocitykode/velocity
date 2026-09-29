package interceptors_test

import (
	"context"
	"sync"

	"google.golang.org/grpc"

	"github.com/velocitykode/velocity/contract"
)

// layerReports records the errors it receives, with their contexts.
type layerReports struct {
	mu   sync.Mutex
	errs []error
	ecs  []*contract.ErrorContext
}

func (r *layerReports) Report(err error, ec *contract.ErrorContext) {
	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.ecs = append(r.ecs, ec)
	r.mu.Unlock()
}

func (r *layerReports) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

func (r *layerReports) contexts() []*contract.ErrorContext {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*contract.ErrorContext(nil), r.ecs...)
}

// chainUnary runs the interceptors in order around handler, as grpc-go's
// chained interceptor does.
func chainUnary(ctx context.Context, handler grpc.UnaryHandler, ics ...grpc.UnaryServerInterceptor) (any, error) {
	info := mockUnaryServerInfo("/svc.Work/Do")
	h := handler
	for i := len(ics) - 1; i >= 0; i-- {
		ic, next := ics[i], h
		h = func(ctx context.Context, req any) (any, error) { return ic(ctx, req, info, next) }
	}
	return h(ctx, nil)
}

// chainStream runs the stream interceptors in order around handler.
func chainStream(ss grpc.ServerStream, handler grpc.StreamHandler, ics ...grpc.StreamServerInterceptor) error {
	info := mockStreamServerInfo("/svc.Work/Watch")
	h := handler
	for i := len(ics) - 1; i >= 0; i-- {
		ic, next := ics[i], h
		h = func(srv any, ss grpc.ServerStream) error { return ic(srv, ss, info, next) }
	}
	return h(nil, ss)
}
