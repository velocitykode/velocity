package velocity

import (
	"errors"
	"reflect"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/problem"
)

// installCSRFErrorRules installs the framework default render rule for a
// rejected CSRF token. The Config.ErrorHandler of the CSRF instance that
// rejected the request answers when it has one (the error carries it, so a
// custom instance mounted on a group or swapped in by a module is honoured).
// Otherwise the rule falls through to negotiation, which renders the
// *csrf.TokenMismatchError itself at 419 with that instance's
// Config.ErrorMessage as the detail: problem+json for JSON clients, the
// Inertia answer for Inertia requests, HTML otherwise.
func installCSRFErrorRules(h *problem.Handler) {
	h.AddFrameworkRenderRule(contract.RenderRule{
		Key: reflect.TypeFor[*csrf.TokenMismatchError](),
		Match: func(err error) bool {
			var tm *csrf.TokenMismatchError
			return errors.As(err, &tm)
		},
		Render: csrf.RenderTokenMismatch,
	})
}
