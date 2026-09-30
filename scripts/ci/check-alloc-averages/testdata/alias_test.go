// Package fixture: a renamed import of testing is still testing.
package fixture

import tst "testing"

func TestAlias(t *tst.T) {
	_ = tst.AllocsPerRun(1, func() {})
}
