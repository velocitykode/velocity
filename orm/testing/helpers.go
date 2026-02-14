package testing

import (
	"testing"

	"github.com/velocitykode/velocity/orm"
)

// Setup creates a TestCase and automatically runs RefreshDatabase
// This is the recommended way to setup database tests
//
// Usage:
//
//	func TestExample(t *testing.T) {
//	    tc := ormtesting.Setup(t, manager)
//	    // Database already refreshed - ready to test
//	}
//
// If you don't need database refresh, use NewTestCase(t, manager) directly instead
func Setup(t *testing.T, manager *orm.Manager) *TestCase {
	tc := NewTestCase(t, manager)
	tc.RefreshDatabase()
	return tc
}
