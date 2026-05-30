package mysql

import "testing"

// TestOperatorRegistry_MySQLNil pins that the MySQL driver declares no
// extension operators in this release.
func TestOperatorRegistry_MySQLNil(t *testing.T) {
	if got := (&MySQLDriver{}).OperatorRegistry(); got != nil {
		t.Errorf("mysql registry: got %v, want nil", got)
	}
}
