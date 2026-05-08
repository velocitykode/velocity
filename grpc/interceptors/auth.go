package interceptors

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/trace"
)

// Claims represents authenticated user claims.
// Applications can implement this interface with their own claims structure.
type Claims interface {
	// GetUserID returns the authenticated user's ID
	GetUserID() uint
	// GetTeamID returns the authenticated user's team ID (0 if none)
	GetTeamID() uint
}

// BasicClaims is a simple implementation of the Claims interface
type BasicClaims struct {
	UserID uint
	TeamID uint
}

// GetUserID returns the user ID
func (c *BasicClaims) GetUserID() uint {
	return c.UserID
}

// GetTeamID returns the team ID
func (c *BasicClaims) GetTeamID() uint {
	return c.TeamID
}

// contextKey is a type for context keys to avoid collisions
type contextKey string

const claimsKey contextKey = "grpc_claims"

// ContextWithClaims adds claims to the context
func ContextWithClaims(ctx context.Context, claims Claims) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
}

// ClaimsFromContext extracts claims from the context.
// Returns nil if no claims are present.
func ClaimsFromContext(ctx context.Context) Claims {
	claims, _ := ctx.Value(claimsKey).(Claims)
	return claims
}

// AuthValidator is the interface that must be implemented to validate tokens.
// Applications provide their own implementation that validates tokens
// and returns claims.
type AuthValidator interface {
	// ValidateToken validates a bearer token and returns claims.
	// Returns an error if the token is invalid.
	ValidateToken(ctx context.Context, token string) (Claims, error)
}

// AuthConfig configures the auth interceptor
type AuthConfig struct {
	// Validator is the token validator implementation (required)
	Validator AuthValidator

	// PublicMethods are method prefixes that don't require authentication.
	// Examples: "/grpc.health.v1.Health/", "/mypackage.MyService/PublicMethod"
	PublicMethods []string

	// TokenExtractor extracts the token from context.
	// Defaults to extracting from "authorization" header with "Bearer " prefix.
	TokenExtractor func(ctx context.Context) (string, error)

	// OnAuthError is called when authentication fails.
	// Can be used for logging or custom error responses.
	OnAuthError func(ctx context.Context, method string, err error)

	// EventDispatcher routes grpcevents.AuthFailed to a listener when
	// token extraction or validation fails.
	EventDispatcher grpcevents.EventDispatchFunc
}

// AuthOption configures auth behavior
type AuthOption func(*AuthConfig)

// WithPublicMethods sets methods that don't require authentication
func WithPublicMethods(methods ...string) AuthOption {
	return func(c *AuthConfig) {
		c.PublicMethods = methods
	}
}

// WithTokenExtractor sets a custom token extractor
func WithTokenExtractor(extractor func(ctx context.Context) (string, error)) AuthOption {
	return func(c *AuthConfig) {
		c.TokenExtractor = extractor
	}
}

// WithOnAuthError sets a callback for auth errors
func WithOnAuthError(handler func(ctx context.Context, method string, err error)) AuthOption {
	return func(c *AuthConfig) {
		c.OnAuthError = handler
	}
}

// WithAuthEventDispatcher routes grpcevents.AuthFailed to a dispatcher when
// token extraction or validation fails.
func WithAuthEventDispatcher(dispatcher grpcevents.EventDispatchFunc) AuthOption {
	return func(c *AuthConfig) {
		c.EventDispatcher = dispatcher
	}
}

// Auth creates an auth interceptor pair that validates bearer tokens.
// The validator is responsible for validating tokens and returning claims.
func Auth(validator AuthValidator, opts ...AuthOption) InterceptorPair {
	cfg := &AuthConfig{
		Validator: validator,
		PublicMethods: []string{
			"/grpc.health.v1.Health/",
			"/grpc.reflection.v1alpha.ServerReflection/",
			"/grpc.reflection.v1.ServerReflection/",
		},
		TokenExtractor: defaultTokenExtractor,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return InterceptorPair{
		Unary:  authUnary(cfg),
		Stream: authStream(cfg),
	}
}

// AuthInterceptor creates a unary auth interceptor.
// This is a convenience function for when you only need the unary interceptor.
func AuthInterceptor(validator AuthValidator, opts ...AuthOption) grpc.UnaryServerInterceptor {
	return Auth(validator, opts...).Unary
}

// StreamAuthInterceptor creates a stream auth interceptor.
// This is a convenience function for when you only need the stream interceptor.
func StreamAuthInterceptor(validator AuthValidator, opts ...AuthOption) grpc.StreamServerInterceptor {
	return Auth(validator, opts...).Stream
}

func authUnary(cfg *AuthConfig) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Check if method is public
		if isPublicMethod(info.FullMethod, cfg.PublicMethods) {
			return handler(ctx, req)
		}

		// Authenticate
		newCtx, err := authenticate(ctx, info.FullMethod, cfg)
		if err != nil {
			return nil, err
		}

		return handler(newCtx, req)
	}
}

func authStream(cfg *AuthConfig) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		// Check if method is public
		if isPublicMethod(info.FullMethod, cfg.PublicMethods) {
			return handler(srv, ss)
		}

		// Authenticate
		newCtx, err := authenticate(ss.Context(), info.FullMethod, cfg)
		if err != nil {
			return err
		}

		// Wrap stream with authenticated context
		wrapped := &authenticatedServerStream{
			ServerStream: ss,
			ctx:          newCtx,
		}

		return handler(srv, wrapped)
	}
}

func authenticate(ctx context.Context, method string, cfg *AuthConfig) (context.Context, error) {
	// Extract token
	token, err := cfg.TokenExtractor(ctx)
	if err != nil {
		if cfg.OnAuthError != nil {
			cfg.OnAuthError(ctx, method, err)
		}
		dispatchAuthFailed(ctx, method, "", err, cfg)
		return nil, err
	}

	// Validate token
	claims, err := cfg.Validator.ValidateToken(ctx, token)
	if err != nil {
		if cfg.OnAuthError != nil {
			cfg.OnAuthError(ctx, method, err)
		}
		dispatchAuthFailed(ctx, method, token, err, cfg)
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}

	// Add claims to context
	return ContextWithClaims(ctx, claims), nil
}

// dispatchAuthFailed emits grpcevents.AuthFailed with masked token and trace
// context. No-op when no dispatcher is configured.
func dispatchAuthFailed(ctx context.Context, method, token string, err error, cfg *AuthConfig) {
	if cfg.EventDispatcher == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatchEvent(cfg.EventDispatcher, &grpcevents.AuthFailed{
		Method:   method,
		Token:    maskToken(token),
		Reason:   err.Error(),
		Time:     time.Now(),
		Context:  ctx,
		TraceID:  traceID,
		SpanID:   spanID,
		ParentID: parentID,
	})
}

// maskToken returns a redacted form: first 4 + last 4 characters when long
// enough, else the empty string. Avoids leaking full tokens to event sinks.
func maskToken(token string) string {
	if len(token) < 12 {
		return ""
	}
	return token[:4] + "..." + token[len(token)-4:]
}

// isPublicMethod decides whether method is in the allow-list. It deliberately
// does NOT use a bare strings.HasPrefix — that would let an entry like
// "/admin" match "/administrator/DoDangerous" and grant free access. The
// contract is:
//   - entries ending in "/" are treated as service-level prefixes: the method
//     name must start with that exact string (service boundary intact).
//   - entries without a trailing "/" must match the method exactly.
func isPublicMethod(method string, publicMethods []string) bool {
	for _, entry := range publicMethods {
		if entry == "" {
			continue
		}
		if strings.HasSuffix(entry, "/") {
			if strings.HasPrefix(method, entry) {
				return true
			}
			continue
		}
		if method == entry {
			return true
		}
	}
	return false
}

func defaultTokenExtractor(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}

	authHeaders := md.Get("authorization")
	if len(authHeaders) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}

	token := authHeaders[0]
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(token, bearerPrefix) {
		return "", status.Error(codes.Unauthenticated, "invalid authorization format")
	}

	return token[len(bearerPrefix):], nil
}

// authenticatedServerStream wraps a ServerStream with a custom context
type authenticatedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authenticatedServerStream) Context() context.Context {
	return s.ctx
}
