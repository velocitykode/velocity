package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/internal/tracekeys"
)

// RequestIDHeader is the header, and the gRPC metadata key in lowercase,
// that carries a request id across a process edge.
const RequestIDHeader = "X-Request-ID"

// maxRequestIDLen bounds a request id taken from the network.
const maxRequestIDLen = 128

// requestIDKey is the one context key for the request id. The router's
// per-request holder, the gRPC interceptors and the HTTP gateway all store
// the id under it, and httpclient and the gRPC client read it from here.
const requestIDKey = tracekeys.RequestID

// requestCounter distinguishes request ids generated within the same second.
var requestCounter atomic.Uint64

// GenerateRequestID returns a new request id: 20 lowercase hex characters
// encoding, in order, the low 32 bits of the current Unix time in seconds
// (8 characters), the low 16 bits of a per-process counter (4 characters)
// and 32 random bits (8 characters). Example: 66f6a0b2002a9c41e07b.
func GenerateRequestID() string {
	counter := requestCounter.Add(1)
	ts := time.Now().Unix()

	var random [4]byte
	_, _ = rand.Read(random[:])

	return hex.EncodeToString([]byte{
		byte(ts >> 24), byte(ts >> 16), byte(ts >> 8), byte(ts),
		byte(counter >> 8), byte(counter),
		random[0], random[1], random[2], random[3],
	})
}

// ValidRequestID reports whether id can serve as a request id taken from
// the network or sent on an outbound call: 1 to 128 bytes, each an ASCII
// letter, a digit or one of - _ . : @ + = /. The set covers generated ids,
// UUIDs and base64 tokens and excludes whitespace, quotes and control
// characters, so an id can be written to a header or a log line as is.
func ValidRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':', c == '@', c == '+', c == '=', c == '/':
		default:
			return false
		}
	}
	return true
}

// WithRequestID returns ctx carrying id as the request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestIDKey, id)
}

// GetRequestID returns the request id carried by ctx, or the empty string.
// For a context from WithLazyRequestID the first read generates the id.
func GetRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// LazyRequestID defers request id generation until the first read. The id
// (from GenerateRequestID) is computed at most once and cached, so every
// read within a request returns the same value and a request that never
// reads it pays nothing.
type LazyRequestID struct {
	once sync.Once
	id   string
}

// ID generates the request id on first call and returns the cached value
// thereafter. Safe for concurrent use.
func (l *LazyRequestID) ID() string {
	l.once.Do(func() {
		l.id = GenerateRequestID()
	})
	return l.id
}

// lazyRequestIDContext answers the request id key from a LazyRequestID,
// generating the id on first read. Every other key delegates to the wrapped
// context, so GetRequestID and any direct ctx.Value read observe the string
// an eager WithRequestID would have stored.
type lazyRequestIDContext struct {
	context.Context
	lazy *LazyRequestID
}

func (c lazyRequestIDContext) Value(key any) any {
	if key == requestIDKey {
		return c.lazy.ID()
	}
	return c.Context.Value(key)
}

// WithLazyRequestID returns ctx carrying a request id that is generated on
// first read, and the holder, so a caller that needs the id up front (to
// populate an event) can force it with ID().
func WithLazyRequestID(ctx context.Context) (context.Context, *LazyRequestID) {
	if ctx == nil {
		ctx = context.Background()
	}
	lazy := &LazyRequestID{}
	return lazyRequestIDContext{Context: ctx, lazy: lazy}, lazy
}
