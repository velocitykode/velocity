// Package subtest is test infrastructure: out of scope, markers included.
package subtest

import "errors"

func Is(err error) bool { return errors.Is(err, errors.ErrUnsupported) } //error-inspection-ok: a stale marker here is not read
