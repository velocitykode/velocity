package grpc

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"

	"github.com/velocitykode/velocity/grpc/interceptors"
)

// Re-export types from interceptors package for convenience
type (
	// Claims represents authenticated user claims.
	// Applications can implement this interface with their own claims structure.
	Claims = interceptors.Claims

	// BasicClaims is a simple implementation of the Claims interface
	BasicClaims = interceptors.BasicClaims
)

// ContextWithClaims adds claims to the context.
// This is a convenience wrapper around interceptors.ContextWithClaims.
func ContextWithClaims(ctx context.Context, claims Claims) context.Context {
	return interceptors.ContextWithClaims(ctx, claims)
}

// ClaimsFromContext extracts claims from the context.
// Returns nil if no claims are present.
// This is a convenience wrapper around interceptors.ClaimsFromContext.
func ClaimsFromContext(ctx context.Context) Claims {
	return interceptors.ClaimsFromContext(ctx)
}

// MustClaimsFromContext extracts claims from the context.
// Panics if no claims are present.
func MustClaimsFromContext(ctx context.Context) Claims {
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		panic("no claims in context")
	}
	return claims
}

// UserIDFromContext extracts the user ID from context claims.
// Returns 0 if no claims are present.
func UserIDFromContext(ctx context.Context) uint {
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		return 0
	}
	return claims.GetUserID()
}

// TeamIDFromContext extracts the team ID from context claims.
// Returns 0 if no claims are present.
func TeamIDFromContext(ctx context.Context) uint {
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		return 0
	}
	return claims.GetTeamID()
}

// contextKey is a type for context keys to avoid collisions
type contextKey string

const (
	requestIDKey contextKey = "grpc_request_id"
	methodKey    contextKey = "grpc_method"
)

// ContextWithRequestID adds a request ID to the context
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// RequestIDFromContext extracts the request ID from the context.
// Returns empty string if no request ID is present.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// ContextWithMethod adds the gRPC method to the context
func ContextWithMethod(ctx context.Context, method string) context.Context {
	return context.WithValue(ctx, methodKey, method)
}

// MethodFromContext extracts the gRPC method from the context.
// Returns empty string if no method is present.
func MethodFromContext(ctx context.Context) string {
	method, _ := ctx.Value(methodKey).(string)
	return method
}

// GenerateRequestID generates a new request ID
func GenerateRequestID() string {
	return uuid.New().String()
}

// ExtractBearerToken extracts a bearer token from gRPC metadata.
// Returns empty string if no token is found.
func ExtractBearerToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}

	authHeaders := md.Get("authorization")
	if len(authHeaders) == 0 {
		return ""
	}

	token := authHeaders[0]
	const bearerPrefix = "Bearer "
	if len(token) > len(bearerPrefix) && token[:len(bearerPrefix)] == bearerPrefix {
		return token[len(bearerPrefix):]
	}

	return ""
}

// ExtractMetadata extracts a specific metadata value from context.
// Returns empty string if not found.
func ExtractMetadata(ctx context.Context, key string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}

	values := md.Get(key)
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

// ExtractAllMetadata extracts all values for a metadata key.
// Returns nil if not found.
func ExtractAllMetadata(ctx context.Context, key string) []string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil
	}

	return md.Get(key)
}
