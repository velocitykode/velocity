package testing

import "testing"

// Setup creates a TestCase and automatically runs RefreshDatabase
// This is the recommended way to setup database tests
//
// Usage:
//
//	func TestExample(t *testing.T) {
//	    tc := ormtesting.Setup(t)
//	    // Database already refreshed - ready to test
//	}
//
// If you don't need database refresh, use NewTestCase(t) directly instead
func Setup(t *testing.T) *TestCase {
	tc := NewTestCase(t)
	tc.RefreshDatabase()
	return tc
}
