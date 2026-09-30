package interceptors

import (
	"context"
	"fmt"
	"reflect"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/internal/errchain"
)

// outcome is what the error a call ended with means, classified once by
// classify: the status code the client gets, the error handed to grpc-go,
// and whether the error is the call's own context ending.
type outcome struct {
	// code is the gRPC status code the call ends with.
	code codes.Code
	// wire is the error the owner returns to grpc-go: nil for a call
	// without an error, else an error of grpc-go's own status type (or,
	// for a status whose code is OK, statusOK), whose methods are safe
	// for grpc-go to call.
	wire error
	// ctxEnded is set when the error is the call's context error: the
	// client cancelled or the deadline passed.
	ctxEnded bool
	// reason is set when the error could not be classified (see the
	// reason constants): the call ends with codes.Internal, and the
	// request line and the report carry it, so an operator can tell such
	// a call from one whose handler returned codes.Internal itself.
	reason string
}

// The reasons a call's error could not be classified.
const (
	// reasonPanicked means a method of the error panicked.
	reasonPanicked = "classification_panicked"
	// reasonBudgetExhausted means the error's chain holds more errors than
	// errchain.Max and no status was found among those looked at.
	reasonBudgetExhausted = "classification_budget_exhausted"
)

// reasonKey is the key a reason is written under, in the request line and
// in the report's Extra.
const reasonKey = "reason"

// reportable reports whether the call's error is an internal error to
// report: its code is Internal or Unknown, the codes the request line
// writes at error level, and the call's own context did not end it.
func (o outcome) reportable() bool {
	return o.wire != nil && !o.ctxEnded && (o.code == codes.Internal || o.code == codes.Unknown)
}

// grpcStatus is the method grpc-go reads a status from.
type grpcStatus interface{ GRPCStatus() *status.Status }

// statusErrorType is the dynamic type of grpc-go's own status error
// (status.Error, status.Errorf, (*status.Status).Err): the framework
// trusts its methods.
var statusErrorType = reflect.TypeOf(status.Error(codes.Unknown, ""))

// internalError is what a call whose error cannot be classified ends with.
var internalError = status.Error(codes.Internal, "internal server error")

// classify classifies err, the error a call under ctx ended with, once,
// as grpc-go would derive the status it sends (status.FromError, then
// status.FromContextError for an error without a status), and returns
// the outcome with an error grpc-go can inspect safely.
//
// err's methods (Unwrap, Is, As, GRPCStatus, Error) are user code: they
// run here, contained, and never again after the owner returns. An error
// of grpc-go's own status type is trusted and returned as it is. Any
// other is walked by errchain.Walk, bounded and breadth first: the first
// error of the chain that carries a status (by its type, or by its As
// method, as errors.As finds it) gives the code; its status as it is when
// it is err itself, else with err's text as the message, as grpc-go
// does. Breadth first differs from errors.As only for a chain with
// several statuses in different branches: the shallowest one wins. An
// error without a status (or whose GRPCStatus returns nil) is
// DeadlineExceeded when context.DeadlineExceeded is in its chain, else
// Canceled when context.Canceled is, else Unknown, with err's text as
// the message.
//
// A call whose error's methods panic, or whose chain is cut short before
// a status is found, ends with codes.Internal and a generic message, and
// the outcome names the reason: its status cannot be known, and grpc-go
// never sees the error.
func classify(ctx context.Context, err error) (o outcome) {
	if err == nil {
		return outcome{code: codes.OK}
	}
	if reflect.TypeOf(err) == statusErrorType {
		return outcome{code: err.(grpcStatus).GRPCStatus().Code(), wire: err}
	}
	defer func() {
		if recover() != nil {
			o = unclassifiable(reasonPanicked)
		}
	}()
	ctxErr := ctx.Err()
	var (
		found                            *status.Status
		matched, atRoot                  bool
		deadline, canceled, ctxErrInside bool
		visited                          int
	)
	walked := errchain.Walk(err, func(e error) bool {
		visited++
		if !matched {
			if gs, ok := e.(grpcStatus); ok {
				// The walk visits err itself first.
				matched, atRoot, found = true, visited == 1, gs.GRPCStatus()
			} else if x, ok := e.(interface{ As(any) bool }); ok {
				var gs grpcStatus
				if x.As(&gs) && gs != nil {
					matched, found = true, gs.GRPCStatus()
				}
			}
		}
		deadline = deadline || errchain.Matches(e, context.DeadlineExceeded)
		canceled = canceled || errchain.Matches(e, context.Canceled)
		ctxErrInside = ctxErrInside || (ctxErr != nil && errchain.Matches(e, ctxErr))
		return false
	})
	switch {
	case walked == errchain.Panicked:
		return unclassifiable(reasonPanicked)
	case walked == errchain.Truncated && !matched:
		return unclassifiable(reasonBudgetExhausted)
	}
	var st *status.Status
	switch {
	case found != nil && atRoot:
		st = found
	case found != nil:
		p := found.Proto()
		p.Message = err.Error()
		st = status.FromProto(p)
	case deadline:
		st = status.New(codes.DeadlineExceeded, err.Error())
	case canceled:
		st = status.New(codes.Canceled, err.Error())
	default:
		st = status.New(codes.Unknown, err.Error())
	}
	o = outcome{code: st.Code(), ctxEnded: ctxErrInside}
	if o.code == codes.OK {
		o.wire = statusOK{st}
	} else {
		o.wire = st.Err()
	}
	return o
}

// unclassifiable is the outcome of an error classify could not read, for
// reason.
func unclassifiable(reason string) outcome {
	return outcome{code: codes.Internal, wire: internalError, reason: reason}
}

// statusOK carries a status whose code is OK for a call that returned a
// non-nil error with such a status: (*status.Status).Err would be nil,
// and grpc-go sends the status it reads from the error, so the client
// gets OK as it would from the original error.
type statusOK struct{ s *status.Status }

func (e statusOK) Error() string {
	return fmt.Sprintf("rpc error: code = %s desc = %s", e.s.Code(), e.s.Message())
}

func (e statusOK) GRPCStatus() *status.Status { return e.s }
