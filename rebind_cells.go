package velocity

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
)

// bindingCell holds the instance the framework's forwarders reach now.
// New hands a collaborator in another package (the session scheme, the
// cookie session store, the queue payload sealer, the notification mail
// channel, the server session store) a forwarder bound to a cell instead
// of the field's value; rebind stores the replacement into the cell when a
// module replaces the field. A collaborator a module gave its own value
// holds no forwarder of ours, so it is never touched: the forwarder in the
// holder is the mark of a framework-installed binding.
//
// One atomic load per use; the zero value holds nothing.
type bindingCell[T any] struct {
	p atomic.Pointer[cellValue[T]]
}

// cellValue boxes a cell's instance for its atomic pointer.
type cellValue[T any] struct{ v T }

func (c *bindingCell[T]) load() (v T) {
	if b := c.p.Load(); b != nil {
		return b.v
	}
	return v
}

func (c *bindingCell[T]) store(v T) { c.p.Store(&cellValue[T]{v}) }

// errNoBoundEncryptor is a forwarded encryptor's error once its cell holds
// none; rebind refuses the swap that would leave it so (see
// rebindEncryptor), so only a zero cell reaches it.
var errNoBoundEncryptor = errors.New("velocity: no encryptor is configured (Services.Crypto)")

// appEncryptor forwards to the encryptor Services.Crypto held at the last
// boundary. It satisfies contract.Encryptor and crypto.Encryptor (the same
// method set), so the session scheme, the cookie session store and the
// queue payload sealer take it in place of the value.
type appEncryptor struct {
	cell *bindingCell[contract.Encryptor]
}

func (e appEncryptor) enc() (contract.Encryptor, error) {
	enc := e.cell.load()
	if enc == nil {
		return nil, errNoBoundEncryptor
	}
	return enc, nil
}

func (e appEncryptor) Encrypt(plaintext string) (string, error) {
	enc, err := e.enc()
	if err != nil {
		return "", err
	}
	return enc.Encrypt(plaintext)
}

func (e appEncryptor) EncryptBytes(plaintext []byte) (string, error) {
	enc, err := e.enc()
	if err != nil {
		return "", err
	}
	return enc.EncryptBytes(plaintext)
}

func (e appEncryptor) Decrypt(payload string) (string, error) {
	enc, err := e.enc()
	if err != nil {
		return "", err
	}
	return enc.Decrypt(payload)
}

func (e appEncryptor) DecryptBytes(payload string) ([]byte, error) {
	enc, err := e.enc()
	if err != nil {
		return nil, err
	}
	return enc.DecryptBytes(payload)
}

func (e appEncryptor) EncryptBytesWithAAD(plaintext, aad []byte) (string, error) {
	enc, err := e.enc()
	if err != nil {
		return "", err
	}
	return enc.EncryptBytesWithAAD(plaintext, aad)
}

func (e appEncryptor) DecryptBytesWithAAD(payload string, aad []byte) ([]byte, error) {
	enc, err := e.enc()
	if err != nil {
		return nil, err
	}
	return enc.DecryptBytesWithAAD(payload, aad)
}

func (e appEncryptor) GenerateKey() (string, error) {
	enc, err := e.enc()
	if err != nil {
		return "", err
	}
	return enc.GenerateKey()
}

// errNoBoundMailer is the forwarded mailer's error while Services.Mail
// holds none, the error the notification mail channel gives without one.
var errNoBoundMailer = errors.New("notification: mail channel has no mailer configured")

// appMailer forwards to the mailer Services.Mail held at the last
// boundary. The notification mail channel New builds sends through it.
type appMailer struct{ cell *bindingCell[contract.Mailer] }

func (m appMailer) Send(ctx context.Context, msg *contract.Message) error {
	mailer := m.cell.load()
	if mailer == nil {
		return errNoBoundMailer
	}
	return mailer.Send(ctx, msg)
}

// sessionCacheBackend is what the server session store needs from the
// cache: the base operations, compare-and-swap and the set operations.
type sessionCacheBackend interface {
	contract.Cache
	contract.CacheSwapper
	contract.CacheSetStore
}

// appSessionCache forwards to the default store of the cache
// Services.Cache held at the last boundary. The server session store New
// builds (SESSION_STORE=server) keeps its records through it. rebind
// refuses a replacement cache whose default store lacks a capability the
// store needs, so the cell never holds nil once bound.
type appSessionCache struct {
	cell *bindingCell[sessionCacheBackend]
}

func (c appSessionCache) b() sessionCacheBackend { return c.cell.load() }

func (c appSessionCache) GetCtx(ctx context.Context, key string) (interface{}, bool) {
	return c.b().GetCtx(ctx, key)
}
func (c appSessionCache) Get(key string) (interface{}, bool) { return c.b().Get(key) }
func (c appSessionCache) GetStringCtx(ctx context.Context, key string) (string, bool) {
	return c.b().GetStringCtx(ctx, key)
}
func (c appSessionCache) GetString(key string) (string, bool) { return c.b().GetString(key) }
func (c appSessionCache) PutCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	return c.b().PutCtx(ctx, key, value, ttl)
}
func (c appSessionCache) Put(key string, value interface{}, ttl time.Duration) error {
	return c.b().Put(key, value, ttl)
}
func (c appSessionCache) AddCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error) {
	return c.b().AddCtx(ctx, key, value, ttl)
}
func (c appSessionCache) Add(key string, value interface{}, ttl time.Duration) (bool, error) {
	return c.b().Add(key, value, ttl)
}
func (c appSessionCache) ForeverCtx(ctx context.Context, key string, value interface{}) error {
	return c.b().ForeverCtx(ctx, key, value)
}
func (c appSessionCache) Forever(key string, value interface{}) error {
	return c.b().Forever(key, value)
}
func (c appSessionCache) ForgetCtx(ctx context.Context, key string) error {
	return c.b().ForgetCtx(ctx, key)
}
func (c appSessionCache) Forget(key string) error            { return c.b().Forget(key) }
func (c appSessionCache) FlushCtx(ctx context.Context) error { return c.b().FlushCtx(ctx) }
func (c appSessionCache) Flush() error                       { return c.b().Flush() }
func (c appSessionCache) IncrementCtx(ctx context.Context, key string, value int64) (int64, error) {
	return c.b().IncrementCtx(ctx, key, value)
}
func (c appSessionCache) Increment(key string, value int64) (int64, error) {
	return c.b().Increment(key, value)
}
func (c appSessionCache) DecrementCtx(ctx context.Context, key string, value int64) (int64, error) {
	return c.b().DecrementCtx(ctx, key, value)
}
func (c appSessionCache) Decrement(key string, value int64) (int64, error) {
	return c.b().Decrement(key, value)
}
func (c appSessionCache) Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error) {
	return c.b().Remember(key, ttl, callback)
}
func (c appSessionCache) RememberForever(key string, callback func() interface{}) (interface{}, error) {
	return c.b().RememberForever(key, callback)
}
func (c appSessionCache) ManyCtx(ctx context.Context, keys []string) map[string]interface{} {
	return c.b().ManyCtx(ctx, keys)
}
func (c appSessionCache) Many(keys []string) map[string]interface{} { return c.b().Many(keys) }
func (c appSessionCache) PutManyCtx(ctx context.Context, items map[string]interface{}, ttl time.Duration) error {
	return c.b().PutManyCtx(ctx, items, ttl)
}
func (c appSessionCache) PutMany(items map[string]interface{}, ttl time.Duration) error {
	return c.b().PutMany(items, ttl)
}
func (c appSessionCache) HasCtx(ctx context.Context, key string) bool { return c.b().HasCtx(ctx, key) }
func (c appSessionCache) Has(key string) bool                         { return c.b().Has(key) }
func (c appSessionCache) CompareAndSwapCtx(ctx context.Context, key string, expected, value interface{}, ttl time.Duration) (bool, error) {
	return c.b().CompareAndSwapCtx(ctx, key, expected, value, ttl)
}
func (c appSessionCache) SetAddCtx(ctx context.Context, key string, ttl time.Duration, members ...string) error {
	return c.b().SetAddCtx(ctx, key, ttl, members...)
}
func (c appSessionCache) SetRemoveCtx(ctx context.Context, key string, members ...string) error {
	return c.b().SetRemoveCtx(ctx, key, members...)
}
func (c appSessionCache) SetMembersCtx(ctx context.Context, key string) ([]string, error) {
	return c.b().SetMembersCtx(ctx, key)
}

// rebindEncryptor moves the encryptor forwarder New handed the session
// scheme, the cookie session store and the queue payload sealer to the
// encryptor Services.Crypto holds now, so their cookies and payloads are
// sealed under the replacement's key from then on. A replacement of none
// is refused while one of them needs an encryptor: New refuses to build
// them without one.
func rebindEncryptor(a *App, _, cur ownedSet) (func(), error) {
	if a.cryptoCell.p.Load() == nil {
		return nil, nil
	}
	next, _ := cur[fieldCrypto].(contract.Encryptor)
	if next == nil && len(a.cryptoConsumers) > 0 {
		return nil, errchain.Errorf("velocity: Services.Crypto replaced with none, but %s needs an encryptor", strings.Join(a.cryptoConsumers, " and "))
	}
	return func() { a.cryptoCell.store(next) }, nil
}

// rebindMailer moves the mailer forwarder the notification mail channel
// sends through to the mailer Services.Mail holds now; with none, the
// channel's sends fail as they do without a mailer.
func rebindMailer(a *App, _, cur ownedSet) (func(), error) {
	next, _ := cur[fieldMail].(contract.Mailer)
	return func() { a.mailCell.store(next) }, nil
}

// rebindSessionCache moves the server session store's cache forwarder to
// the default store of the cache Services.Cache holds now. A replacement
// whose default store is missing, or lacks compare-and-swap or the set
// operations, cannot keep session records: it is refused, as New refuses
// it for SESSION_STORE=server.
func rebindSessionCache(a *App, _, cur ownedSet) (func(), error) {
	if a.sessionCacheCell.p.Load() == nil {
		return nil, nil
	}
	cm, _ := cur[fieldCache].(contract.CacheManager)
	if cm == nil {
		return nil, errchain.Errorf("velocity: Services.Cache replaced with none, but the server session store (SESSION_STORE=server) needs a cache store: %w", session.ErrCacheStoreNilBackend)
	}
	store, err := cm.DefaultStore()
	if err != nil {
		return nil, errchain.Errorf("velocity: Services.Cache: the server session store (SESSION_STORE=server) needs a default cache store: %w", err)
	}
	full, ok := store.(sessionCacheBackend)
	if !ok {
		return nil, errchain.Errorf("velocity: Services.Cache: the server session store (SESSION_STORE=server) needs a default store with compare-and-swap (contract.CacheSwapper) and set operations (contract.CacheSetStore), and %T has not: %w", store, session.ErrCacheStoreUnsupported)
	}
	return func() { a.sessionCacheCell.store(full) }, nil
}
