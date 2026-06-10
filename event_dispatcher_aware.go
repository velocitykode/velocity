package velocity

import (
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/bond"
	"github.com/velocitykode/velocity/bus"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	cryptodrv "github.com/velocitykode/velocity/crypto/drivers"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/scheduler"
	"github.com/velocitykode/velocity/view"
	"github.com/velocitykode/velocity/websocket"
)

// Compile-time checks that every subsystem exposing SetEventDispatcher
// satisfies contract.EventDispatcherAware. Signature drift (typed vs. any)
// fails the build here before bootstrap tries to wire it up.
var (
	_ contract.EventDispatcherAware = (*auth.Manager)(nil)
	_ contract.EventDispatcherAware = (*bond.Bond)(nil)
	_ contract.EventDispatcherAware = (*bus.Bus)(nil)
	_ contract.EventDispatcherAware = (*cache.Manager)(nil)
	_ contract.EventDispatcherAware = (*cryptodrv.AESDriver)(nil)
	_ contract.EventDispatcherAware = (*csrf.CSRF)(nil)
	_ contract.EventDispatcherAware = (*mail.Manager)(nil)
	_ contract.EventDispatcherAware = (*notification.Manager)(nil)
	_ contract.EventDispatcherAware = (*orm.Manager)(nil)
	_ contract.EventDispatcherAware = (*queue.DatabaseDriver)(nil)
	_ contract.EventDispatcherAware = (*queue.MemoryDriver)(nil)
	// The redis driver lives in the queue/redis leaf (to keep go-redis out of
	// core); its EventDispatcherAware conformance is asserted there.
	_ contract.EventDispatcherAware = (*queue.Worker)(nil)
	_ contract.EventDispatcherAware = (*router.VelocityRouterV2)(nil)
	_ contract.EventDispatcherAware = (*scheduler.Scheduler)(nil)
	// grpc.Server's EventDispatcherAware conformance is asserted in the grpc
	// package, which keeps grpc and its protobuf/gateway deps out of this
	// package's import graph.
	_ contract.EventDispatcherAware = (*view.Engine)(nil)
)

// Compile-time checks that subsystems holding background goroutines or
// connections implement contract.ShutdownAware so App.Shutdown can thread
// its deadline through every layer.
var (
	// grpc.Server's ShutdownAware conformance is asserted in grpc/server.go.
	_ contract.ShutdownAware = (*websocket.Server)(nil)
)
