package router

import (
	"net/http"
	"testing"
)

func TestStatusCodes(t *testing.T) {
	tests := []struct {
		name     string
		got      int
		expected int
	}{
		// 1xx Informational
		{"StatusContinue", StatusContinue, http.StatusContinue},
		{"StatusSwitchingProtocols", StatusSwitchingProtocols, http.StatusSwitchingProtocols},
		{"StatusProcessing", StatusProcessing, http.StatusProcessing},
		{"StatusEarlyHints", StatusEarlyHints, http.StatusEarlyHints},

		// 2xx Success
		{"StatusOK", StatusOK, http.StatusOK},
		{"StatusCreated", StatusCreated, http.StatusCreated},
		{"StatusAccepted", StatusAccepted, http.StatusAccepted},
		{"StatusNonAuthoritativeInfo", StatusNonAuthoritativeInfo, http.StatusNonAuthoritativeInfo},
		{"StatusNoContent", StatusNoContent, http.StatusNoContent},
		{"StatusResetContent", StatusResetContent, http.StatusResetContent},
		{"StatusPartialContent", StatusPartialContent, http.StatusPartialContent},
		{"StatusMultiStatus", StatusMultiStatus, http.StatusMultiStatus},
		{"StatusAlreadyReported", StatusAlreadyReported, http.StatusAlreadyReported},
		{"StatusIMUsed", StatusIMUsed, http.StatusIMUsed},

		// 3xx Redirection
		{"StatusMultipleChoices", StatusMultipleChoices, http.StatusMultipleChoices},
		{"StatusMovedPermanently", StatusMovedPermanently, http.StatusMovedPermanently},
		{"StatusFound", StatusFound, http.StatusFound},
		{"StatusSeeOther", StatusSeeOther, http.StatusSeeOther},
		{"StatusNotModified", StatusNotModified, http.StatusNotModified},
		{"StatusUseProxy", StatusUseProxy, http.StatusUseProxy},
		{"StatusTemporaryRedirect", StatusTemporaryRedirect, http.StatusTemporaryRedirect},
		{"StatusPermanentRedirect", StatusPermanentRedirect, http.StatusPermanentRedirect},

		// 4xx Client Errors
		{"StatusBadRequest", StatusBadRequest, http.StatusBadRequest},
		{"StatusUnauthorized", StatusUnauthorized, http.StatusUnauthorized},
		{"StatusPaymentRequired", StatusPaymentRequired, http.StatusPaymentRequired},
		{"StatusForbidden", StatusForbidden, http.StatusForbidden},
		{"StatusNotFound", StatusNotFound, http.StatusNotFound},
		{"StatusMethodNotAllowed", StatusMethodNotAllowed, http.StatusMethodNotAllowed},
		{"StatusNotAcceptable", StatusNotAcceptable, http.StatusNotAcceptable},
		{"StatusProxyAuthRequired", StatusProxyAuthRequired, http.StatusProxyAuthRequired},
		{"StatusRequestTimeout", StatusRequestTimeout, http.StatusRequestTimeout},
		{"StatusConflict", StatusConflict, http.StatusConflict},
		{"StatusGone", StatusGone, http.StatusGone},
		{"StatusLengthRequired", StatusLengthRequired, http.StatusLengthRequired},
		{"StatusPreconditionFailed", StatusPreconditionFailed, http.StatusPreconditionFailed},
		{"StatusRequestEntityTooLarge", StatusRequestEntityTooLarge, http.StatusRequestEntityTooLarge},
		{"StatusRequestURITooLong", StatusRequestURITooLong, http.StatusRequestURITooLong},
		{"StatusUnsupportedMediaType", StatusUnsupportedMediaType, http.StatusUnsupportedMediaType},
		{"StatusRequestedRangeNotSatisfiable", StatusRequestedRangeNotSatisfiable, http.StatusRequestedRangeNotSatisfiable},
		{"StatusExpectationFailed", StatusExpectationFailed, http.StatusExpectationFailed},
		{"StatusTeapot", StatusTeapot, http.StatusTeapot},
		{"StatusMisdirectedRequest", StatusMisdirectedRequest, http.StatusMisdirectedRequest},
		{"StatusUnprocessableEntity", StatusUnprocessableEntity, http.StatusUnprocessableEntity},
		{"StatusLocked", StatusLocked, http.StatusLocked},
		{"StatusFailedDependency", StatusFailedDependency, http.StatusFailedDependency},
		{"StatusTooEarly", StatusTooEarly, http.StatusTooEarly},
		{"StatusUpgradeRequired", StatusUpgradeRequired, http.StatusUpgradeRequired},
		{"StatusPreconditionRequired", StatusPreconditionRequired, http.StatusPreconditionRequired},
		{"StatusTooManyRequests", StatusTooManyRequests, http.StatusTooManyRequests},
		{"StatusRequestHeaderFieldsTooLarge", StatusRequestHeaderFieldsTooLarge, http.StatusRequestHeaderFieldsTooLarge},
		{"StatusUnavailableForLegalReasons", StatusUnavailableForLegalReasons, http.StatusUnavailableForLegalReasons},

		// 5xx Server Errors
		{"StatusInternalServerError", StatusInternalServerError, http.StatusInternalServerError},
		{"StatusNotImplemented", StatusNotImplemented, http.StatusNotImplemented},
		{"StatusBadGateway", StatusBadGateway, http.StatusBadGateway},
		{"StatusServiceUnavailable", StatusServiceUnavailable, http.StatusServiceUnavailable},
		{"StatusGatewayTimeout", StatusGatewayTimeout, http.StatusGatewayTimeout},
		{"StatusHTTPVersionNotSupported", StatusHTTPVersionNotSupported, http.StatusHTTPVersionNotSupported},
		{"StatusVariantAlsoNegotiates", StatusVariantAlsoNegotiates, http.StatusVariantAlsoNegotiates},
		{"StatusInsufficientStorage", StatusInsufficientStorage, http.StatusInsufficientStorage},
		{"StatusLoopDetected", StatusLoopDetected, http.StatusLoopDetected},
		{"StatusNotExtended", StatusNotExtended, http.StatusNotExtended},
		{"StatusNetworkAuthenticationRequired", StatusNetworkAuthenticationRequired, http.StatusNetworkAuthenticationRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("router.%s = %d, want %d", tt.name, tt.got, tt.expected)
			}
		})
	}
}
