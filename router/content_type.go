package router

import (
	"mime"
	"net/http"
	"strings"
)

// ContentType returns middleware that validates the Content-Type header
// against the list of allowed media types. It only checks requests that
// typically carry a body (POST, PUT, PATCH). GET, HEAD, OPTIONS, and
// bodyless DELETE requests are passed through.
//
// Media types are compared using mime.ParseMediaType, so parameters
// like charset are ignored during matching.
//
// Usage:
//
//	api.Use(router.ContentType("application/json"))
//	uploads.Use(router.ContentType("multipart/form-data", "application/x-www-form-urlencoded"))
func ContentType(allowed ...string) MiddlewareFunc {
	// Normalize allowed types at construction time
	normalized := make([]string, 0, len(allowed))
	for _, a := range allowed {
		mt, _, _ := mime.ParseMediaType(a)
		if mt != "" {
			normalized = append(normalized, strings.ToLower(mt))
		} else {
			normalized = append(normalized, strings.ToLower(strings.TrimSpace(a)))
		}
	}

	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			// Only check methods that typically have a body
			method := c.Request.Method
			if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
				return next(c)
			}

			// For DELETE, only check if there's actually a body.
			// ContentLength is -1 for chunked/unknown encoding, which may
			// carry a body, so we only skip when explicitly zero or no body.
			if method == http.MethodDelete && c.Request.ContentLength == 0 {
				return next(c)
			}

			// If there's a body but no Content-Type, reject.
			// ContentLength is -1 for chunked encoding (body may exist).
			ct := c.Request.Header.Get("Content-Type")
			if ct == "" {
				if c.Request.ContentLength != 0 {
					return c.JSON(http.StatusUnsupportedMediaType, Error{
						Code:    http.StatusUnsupportedMediaType,
						Message: "Unsupported Media Type",
					})
				}
				// No body, no content-type — let it through
				return next(c)
			}

			// Parse the request's content type
			mediaType, _, _ := mime.ParseMediaType(ct)
			mediaType = strings.ToLower(mediaType)

			for _, a := range normalized {
				if mediaType == a {
					return next(c)
				}
			}

			return c.JSON(http.StatusUnsupportedMediaType, Error{
				Code:    http.StatusUnsupportedMediaType,
				Message: "Unsupported Media Type",
			})
		}
	}
}

// ContentTypeJSON returns middleware that only allows application/json requests.
func ContentTypeJSON() MiddlewareFunc {
	return ContentType("application/json")
}

// ContentTypeForm returns middleware that allows form submissions
// (both URL-encoded and multipart).
func ContentTypeForm() MiddlewareFunc {
	return ContentType("application/x-www-form-urlencoded", "multipart/form-data")
}
