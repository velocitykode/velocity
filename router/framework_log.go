package router

import (
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
)

// log returns the logger the router writes its own operational lines to
// (a registration made after serving began): the logger SetLogger
// installed, or the framework's standalone fallback logger when none is
// set. Like the failed-request path, it reads the logger without
// synchronization: SetLogger runs before serving begins.
func (r *VelocityRouterV2) log() contract.Logger {
	return fallbacklog.Resolve(r.logger)
}

// requestLogger returns the logger a line the framework writes while
// serving c goes to: the logger of the router that dispatched the request
// (SetLogger), else the logger of the services c carries, else the
// framework's standalone fallback logger (a Context built outside a
// router, with no services). The logger is bound to c.LogFields, so the
// line names the request, trace and span ids, the method and the route
// the way c.Log() lines do. It is built per call and not cached on c:
// its lines are once-per-middleware warnings and late-panic reports, and a
// Timeout goroutine calls it on its own Context.
func requestLogger(c *Context) contract.Logger {
	if c == nil {
		return fallbacklog.Logger{}
	}
	var l contract.Logger = fallbacklog.Logger{}
	if r := servingRouter(c.Request); r != nil && r.logger != nil {
		l = r.logger
	} else if c.services != nil && c.services.Log != nil {
		l = c.services.Log
	}
	if fields := c.LogFields(); len(fields) > 0 {
		return l.With(fields...)
	}
	return l
}
