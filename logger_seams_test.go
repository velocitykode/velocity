package velocity

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/bond"
	"github.com/velocitykode/velocity/broadcast"
	broadcastdrivers "github.com/velocitykode/velocity/broadcast/drivers"
	"github.com/velocitykode/velocity/bus"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/console"
	"github.com/velocitykode/velocity/contract"
	cryptodrivers "github.com/velocitykode/velocity/crypto/drivers"
	"github.com/velocitykode/velocity/csrf"
	csrfstores "github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
	notificationbroadcast "github.com/velocitykode/velocity/notification/broadcast"
	"github.com/velocitykode/velocity/orm"
	ormdrivers "github.com/velocitykode/velocity/orm/drivers"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/problem/routerbridge"
	"github.com/velocitykode/velocity/queue"
	queueredis "github.com/velocitykode/velocity/queue/redis"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/scheduler"
	"github.com/velocitykode/velocity/trace"
	"github.com/velocitykode/velocity/view"
	"github.com/velocitykode/velocity/websocket"
)

// newLoggerSeamApp returns a test app and its logger, the contract.Logger
// every framework logger seam must accept unchanged.
func newLoggerSeamApp(t *testing.T) contract.Logger {
	t.Helper()
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	return a.Log
}

// Every framework type with a logger setter implements contract.LoggerAware
// and takes the app logger through it. One entry per type; the file
// compiles only while each one does.
func TestAppLog_PassesToEveryLoggerAwareType(t *testing.T) {
	log := newLoggerSeamApp(t)
	engine, err := view.NewEngine(view.Config{})
	if err != nil {
		t.Fatalf("view.NewEngine: %v", err)
	}
	seams := []struct {
		name string
		seam contract.LoggerAware
	}{
		{"router.VelocityRouterV2", router.New()},
		{"auth.Manager", auth.NewManager()},
		{"auth.BcryptHasher", &auth.BcryptHasher{}},
		{"schemes.SessionScheme", &schemes.SessionScheme{}},
		{"websocket.Server", &websocket.Server{}},
		{"bond.Bond", &bond.Bond{}},
		{"view.Engine", engine},
		{"scheduler.Scheduler", scheduler.New()},
		{"scheduler.Manager", scheduler.NewManager()},
		{"queue.MemoryDriver", &queue.MemoryDriver{}},
		{"queue/redis.RedisDriver", &queueredis.RedisDriver{}},
		{"orm.Manager", &orm.Manager{}},
		{"orm.Relay", &orm.Relay{}},
		{"orm/drivers.BaseDriver", &ormdrivers.BaseDriver{}},
		{"orm/drivers.SQLiteDriver", &ormdrivers.SQLiteDriver{}},
		{"broadcast.BroadcastManager", &broadcast.BroadcastManager{}},
		{"broadcast/drivers.WebSocketDriver", &broadcastdrivers.WebSocketDriver{}},
		{"crypto/drivers.AESDriver", &cryptodrivers.AESDriver{}},
		{"csrf.CSRF", &csrf.CSRF{}},
		{"csrf/stores.SessionBagStore", &csrfstores.SessionBagStore{}},
		{"cache.Manager", &cache.Manager{}},
		{"mail.LogDriver", mail.NewLogDriver()},
		{"notification.Manager", notification.NewManager()},
		{"notification/broadcast.BroadcastChannel", notificationbroadcast.NewBroadcastChannel()},
	}
	for _, s := range seams {
		t.Run(s.name, func(t *testing.T) {
			s.seam.SetLogger(log)
		})
	}
}

// Every framework logger seam that is a function, an option or a field
// takes the app logger unchanged. One line per seam; the file compiles only
// while each one does.
func TestAppLog_PassesToEveryLoggerOption(t *testing.T) {
	log := newLoggerSeamApp(t)

	prevAsync, prevTrace := async.GetLogger(), trace.GetLogger()
	t.Cleanup(func() {
		async.SetLogger(prevAsync)
		trace.SetLogger(prevTrace)
	})

	done := make(chan struct{})
	seams := []struct {
		name string
		pass func()
	}{
		{"async.SetLogger", func() { async.SetLogger(log) }},
		{"trace.SetLogger", func() { trace.SetLogger(log) }},
		{"cache.StoreConfig.Logger", func() { _ = cache.StoreConfig{Logger: log} }},
		{"async.GoWithLogger", func() { async.GoWithLogger(log, "logger-seam", func() { close(done) }); <-done }},
		{"queue.SetSigningLogger", func() { queue.SetSigningLogger(log) }},
		{"queue.QueueConfig.Logger", func() { _ = queue.QueueConfig{Logger: log} }},
		{"queue.WithWorkerLogger", func() { _ = queue.WithWorkerLogger(log) }},
		{"console.QueueWorkOptions.Logger", func() { _ = console.QueueWorkOptions{Logger: log} }},
		{"bus.LoggingMiddleware", func() { _ = bus.LoggingMiddleware(log) }},
		{"velocity.WithMaintenanceLogger", func() { _ = WithMaintenanceLogger(log) }},
		{"routerbridge.WithLogger", func() { _ = routerbridge.WithLogger(log) }},
		{"problem.WithLogger", func() { _ = problem.WithLogger(log) }},
		{"problem.WithHandlerLogger", func() { _ = problem.WithHandlerLogger(log) }},
		{"grpc.WithLogger", func() { _ = grpc.WithLogger(log) }},
		{"grpc.GatewayWithLogger", func() { _ = grpc.GatewayWithLogger(log) }},
		{"interceptors.WithLogger", func() { _ = interceptors.WithLogger(log) }},
		{"interceptors.CallConfig.Logger", func() { _ = interceptors.CallConfig{Logger: log} }},
	}
	for _, s := range seams {
		t.Run(s.name, func(t *testing.T) {
			s.pass()
		})
	}
}
