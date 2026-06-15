package testing

import (
	"reflect"
	"testing"
)

// cacheAsserter is the minimal read surface the assertion helpers need. Both
// the store types returned by FakeMemory/FakeRedis (cache.Store) and the
// *cache.Manager returned by FakeManager/FakeManagerMemory satisfy it.
type cacheAsserter interface {
	Has(key string) bool
	Get(key string) (interface{}, bool)
}

// AssertHas fails the test if key is not present in the cache.
func AssertHas(t testing.TB, c cacheAsserter, key string) {
	t.Helper()
	if !c.Has(key) {
		t.Errorf("AssertHas: expected key %q to be present, but it was missing", key)
	}
}

// AssertMissing fails the test if key is present in the cache.
func AssertMissing(t testing.TB, c cacheAsserter, key string) {
	t.Helper()
	if c.Has(key) {
		t.Errorf("AssertMissing: expected key %q to be missing, but it was present", key)
	}
}

// AssertForgotten fails the test if key is still present in the cache. Use it
// to verify a Forget/Flush removed the key.
func AssertForgotten(t testing.TB, c cacheAsserter, key string) {
	t.Helper()
	if c.Has(key) {
		t.Errorf("AssertForgotten: expected key %q to be forgotten, but it was still present", key)
	}
}

// AssertHasValue fails the test if key is missing or its stored value is not
// deeply equal to want.
//
// Value fidelity depends on the driver: the in-memory store returns the exact
// concrete type that was Put, while the serializing stores (redis, file)
// round-trip through JSON and hand back decoded shapes (map[string]interface{},
// float64). Compare against the shape the driver under test actually returns.
func AssertHasValue(t testing.TB, c cacheAsserter, key string, want interface{}) {
	t.Helper()
	got, found := c.Get(key)
	if !found {
		t.Errorf("AssertHasValue: expected key %q to be present, but it was missing", key)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AssertHasValue: key %q = %#v, want %#v", key, got, want)
	}
}
