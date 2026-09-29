// Package callhook lets the grpc package hand its Server's event emitter
// to the call lifecycle interceptor Build installs by default, without an
// exported option that names an internal type.
package callhook

import "github.com/velocitykode/velocity/internal/eventemit"

// WithEmitter returns an interceptors.CallOption (typed any to avoid an
// import cycle) that makes the call lifecycle interceptor dispatch its
// events through e, reading e's dispatcher on each call. Package
// interceptors sets it in its init.
var WithEmitter func(e *eventemit.Emitter) any
