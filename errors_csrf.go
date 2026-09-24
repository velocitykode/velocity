package velocity

import (
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/problem"
)

// installCSRFErrorRules installs the framework default render rule for a
// rejected CSRF token that owns the response status. The
// Config.ErrorHandler of the CSRF instance that rejected the request
// answers when it has one (the error carries it, so a custom instance
// mounted on a group or swapped in by a module is honoured). Otherwise the
// rule falls through to negotiation, which renders the
// *csrf.TokenMismatchError itself at 419 with that instance's
// Config.ErrorMessage as the detail: problem+json for JSON clients, the
// Inertia answer for Inertia requests, HTML otherwise.
func installCSRFErrorRules(h *problem.Handler) {
	problem.FrameworkRenderFor[*csrf.TokenMismatchError](h, csrf.RenderTokenMismatch)
}
