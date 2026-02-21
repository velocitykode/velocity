package router

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CORSConfig holds configuration for the CORS middleware.
type CORSConfig struct {
	// AllowedOrigins is a list of origins allowed to access the resource.
	// Use []string{"*"} to allow all origins.
	AllowedOrigins []string

	// AllowedMethods is a list of HTTP methods allowed for cross-origin requests.
	AllowedMethods []string

	// AllowedHeaders is a list of headers allowed in cross-origin requests.
	AllowedHeaders []string

	// ExposedHeaders is a list of headers the browser is allowed to access.
	ExposedHeaders []string

	// AllowCredentials indicates whether the request can include credentials.
	AllowCredentials bool

	// MaxAge indicates how long preflight results can be cached.
	MaxAge time.Duration
}

// DefaultCORSConfig returns a CORSConfig with secure defaults. AllowedOrigins
// is empty, which rejects all cross-origin requests until the developer
// explicitly configures allowed origins. Use PermissiveCORSConfig for
// development when you want to allow all origins.
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "X-Requested-With"},
		MaxAge:         12 * time.Hour,
	}
}

// PermissiveCORSConfig returns a CORSConfig that allows all origins. This is
// useful during development but should not be used in production without
// careful consideration.
func PermissiveCORSConfig() CORSConfig {
	return CORSConfig{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "X-Requested-With"},
		MaxAge:         12 * time.Hour,
	}
}

// CORS creates a CORS middleware with the given configuration.
//
// WARNING: Using AllowedOrigins: ["*"] with AllowCredentials: true is dangerous.
// The CORS spec forbids Access-Control-Allow-Origin: * with credentials, so the
// middleware echoes back the request origin instead. This effectively allows any
// site to make credentialed requests to your API. Only use this combination if
// you fully understand the security implications. Prefer listing explicit origins.
func CORS(config CORSConfig) MiddlewareFunc {
	allowAll := false
	for _, o := range config.AllowedOrigins {
		if o == "*" {
			allowAll = true
			break
		}
	}

	if allowAll && config.AllowCredentials {
		log.Println("velocity/cors: WARNING: AllowedOrigins [\"*\"] with AllowCredentials is dangerous — " +
			"the request origin will be echoed back, allowing any site to make credentialed requests. " +
			"Use explicit origins instead.")
	}

	methods := strings.Join(config.AllowedMethods, ", ")
	headers := strings.Join(config.AllowedHeaders, ", ")
	exposed := strings.Join(config.ExposedHeaders, ", ")
	maxAge := strconv.Itoa(int(config.MaxAge.Seconds()))

	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			origin := c.Request.Header.Get("Origin")
			if origin == "" {
				return next(c)
			}

			// Check if the origin is allowed
			allowed := allowAll
			if !allowed {
				for _, o := range config.AllowedOrigins {
					if o == origin {
						allowed = true
						break
					}
				}
			}

			if !allowed {
				return next(c)
			}

			// Set origin header
			if allowAll && !config.AllowCredentials {
				c.SetHeader("Access-Control-Allow-Origin", "*")
			} else {
				c.SetHeader("Access-Control-Allow-Origin", origin)
				c.SetHeader("Vary", "Origin")
			}

			if config.AllowCredentials {
				c.SetHeader("Access-Control-Allow-Credentials", "true")
			}

			if exposed != "" {
				c.SetHeader("Access-Control-Expose-Headers", exposed)
			}

			// Handle preflight OPTIONS requests
			if c.Request.Method == http.MethodOptions {
				c.SetHeader("Access-Control-Allow-Methods", methods)
				c.SetHeader("Access-Control-Allow-Headers", headers)
				if config.MaxAge > 0 {
					c.SetHeader("Access-Control-Max-Age", maxAge)
				}
				return c.NoContent()
			}

			return next(c)
		}
	}
}
