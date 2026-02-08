package router

import "net/http"

// HTTP status code constants re-exported from net/http for convenience,
// so handler authors don't need to import net/http just for status codes.
const (
	// 1xx Informational
	StatusContinue           = http.StatusContinue
	StatusSwitchingProtocols = http.StatusSwitchingProtocols
	StatusProcessing         = http.StatusProcessing
	StatusEarlyHints         = http.StatusEarlyHints

	// 2xx Success
	StatusOK                   = http.StatusOK
	StatusCreated              = http.StatusCreated
	StatusAccepted             = http.StatusAccepted
	StatusNonAuthoritativeInfo = http.StatusNonAuthoritativeInfo
	StatusNoContent            = http.StatusNoContent
	StatusResetContent         = http.StatusResetContent
	StatusPartialContent       = http.StatusPartialContent
	StatusMultiStatus          = http.StatusMultiStatus
	StatusAlreadyReported      = http.StatusAlreadyReported
	StatusIMUsed               = http.StatusIMUsed

	// 3xx Redirection
	StatusMultipleChoices   = http.StatusMultipleChoices
	StatusMovedPermanently  = http.StatusMovedPermanently
	StatusFound             = http.StatusFound
	StatusSeeOther          = http.StatusSeeOther
	StatusNotModified       = http.StatusNotModified
	StatusUseProxy          = http.StatusUseProxy
	StatusTemporaryRedirect = http.StatusTemporaryRedirect
	StatusPermanentRedirect = http.StatusPermanentRedirect

	// 4xx Client Errors
	StatusBadRequest                   = http.StatusBadRequest
	StatusUnauthorized                 = http.StatusUnauthorized
	StatusPaymentRequired              = http.StatusPaymentRequired
	StatusForbidden                    = http.StatusForbidden
	StatusNotFound                     = http.StatusNotFound
	StatusMethodNotAllowed             = http.StatusMethodNotAllowed
	StatusNotAcceptable                = http.StatusNotAcceptable
	StatusProxyAuthRequired            = http.StatusProxyAuthRequired
	StatusRequestTimeout               = http.StatusRequestTimeout
	StatusConflict                     = http.StatusConflict
	StatusGone                         = http.StatusGone
	StatusLengthRequired               = http.StatusLengthRequired
	StatusPreconditionFailed           = http.StatusPreconditionFailed
	StatusRequestEntityTooLarge        = http.StatusRequestEntityTooLarge
	StatusRequestURITooLong            = http.StatusRequestURITooLong
	StatusUnsupportedMediaType         = http.StatusUnsupportedMediaType
	StatusRequestedRangeNotSatisfiable = http.StatusRequestedRangeNotSatisfiable
	StatusExpectationFailed            = http.StatusExpectationFailed
	StatusTeapot                       = http.StatusTeapot
	StatusMisdirectedRequest           = http.StatusMisdirectedRequest
	StatusUnprocessableEntity          = http.StatusUnprocessableEntity
	StatusLocked                       = http.StatusLocked
	StatusFailedDependency             = http.StatusFailedDependency
	StatusTooEarly                     = http.StatusTooEarly
	StatusUpgradeRequired              = http.StatusUpgradeRequired
	StatusPreconditionRequired         = http.StatusPreconditionRequired
	StatusTooManyRequests              = http.StatusTooManyRequests
	StatusRequestHeaderFieldsTooLarge  = http.StatusRequestHeaderFieldsTooLarge
	StatusUnavailableForLegalReasons   = http.StatusUnavailableForLegalReasons

	// 5xx Server Errors
	StatusInternalServerError           = http.StatusInternalServerError
	StatusNotImplemented                = http.StatusNotImplemented
	StatusBadGateway                    = http.StatusBadGateway
	StatusServiceUnavailable            = http.StatusServiceUnavailable
	StatusGatewayTimeout                = http.StatusGatewayTimeout
	StatusHTTPVersionNotSupported       = http.StatusHTTPVersionNotSupported
	StatusVariantAlsoNegotiates         = http.StatusVariantAlsoNegotiates
	StatusInsufficientStorage           = http.StatusInsufficientStorage
	StatusLoopDetected                  = http.StatusLoopDetected
	StatusNotExtended                   = http.StatusNotExtended
	StatusNetworkAuthenticationRequired = http.StatusNetworkAuthenticationRequired
)
