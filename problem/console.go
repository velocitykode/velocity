package problem

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/velocitykode/velocity/contract"
)

// HandleConsole reports err for a console command through the same report
// gate as requests (no render rules apply), writes one line
// "error: <message>" to stderr and returns the process exit code: the
// ExitCoder's code (a negative code becomes 1), otherwise 1, and 0 for a nil
// err. The message is the full Error() in debug mode; otherwise the
// HTTPError message or status title for status errors, and Error() for
// anything else.
func (h *Handler) HandleConsole(stderr io.Writer, err error) int {
	if err == nil {
		return 0
	}
	s := h.snap()
	h.report(s, err, NewErrorContext(), nil)
	if stderr != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", consoleMessage(err, s.debug))
	}
	return exitCode(err)
}

// consoleMessage returns the one-line message HandleConsole prints.
func consoleMessage(err error, debug bool) string {
	msg := err.Error()
	if !debug {
		if status, _, ok := contract.StatusOf(err); ok {
			msg = statusTitle(status)
			var he *contract.HTTPError
			if errors.As(err, &he) && he.StatusCode() == status && he.Message != "" {
				msg = he.Message
			}
		}
	}
	return strings.Join(strings.Fields(msg), " ")
}

// exitCode returns the ExitCoder code in err's chain, or 1.
func exitCode(err error) int {
	var coder contract.ExitCoder
	if errors.As(err, &coder) {
		if code := coder.ExitCode(); code >= 0 {
			return code
		}
	}
	return 1
}
