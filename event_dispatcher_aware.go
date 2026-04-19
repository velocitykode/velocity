package velocity

import (
	"github.com/velocitykode/velocity/bond"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	cryptodrv "github.com/velocitykode/velocity/crypto/drivers"
	"github.com/velocitykode/velocity/csrf"
	velgrpc "github.com/velocitykode/velocity/grpc"
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
	_ contract.EventDispatcherAware = (*bond.Bond)(nil)
	_ contract.EventDispatcherAware = (*cache.Manager)(nil)
	_ contract.EventDispatcherAware = (*cryptodrv.AESDriver)(nil)
	_ contract.EventDispatcherAware = (*csrf.CSRF)(nil)
	_ contract.EventDispatcherAware = (*mail.Manager)(nil)
	_ contract.EventDispatcherAware = (*notification.Manager)(nil)
	_ contract.EventDispatcherAware = (*orm.Manager)(nil)
	_ contract.EventDispatcherAware = (*queue.DatabaseDriver)(nil)
	_ contract.EventDispatcherAware = (*queue.MemoryDriver)(nil)
	_ contract.EventDispatcherAware = (*queue.RedisDriver)(nil)
	_ contract.EventDispatcherAware = (*queue.Worker)(nil)
	_ contract.EventDispatcherAware = (*router.VelocityRouterV2)(nil)
	_ contract.EventDispatcherAware = (*scheduler.Scheduler)(nil)
	_ contract.EventDispatcherAware = (*velgrpc.Server)(nil)
	_ contract.EventDispatcherAware = (*view.Engine)(nil)
)

// Compile-time checks that subsystems holding background goroutines or
// connections implement contract.ShutdownAware so App.Shutdown can thread
// its deadline through every layer.
var (
	_ contract.ShutdownAware = (*velgrpc.Server)(nil)
	_ contract.ShutdownAware = (*websocket.Server)(nil)
)
