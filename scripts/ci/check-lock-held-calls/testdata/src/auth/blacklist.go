package auth

import "time"

// BlacklistStore is a pluggable JTI blacklist: an interface.
type BlacklistStore interface {
	Add(jti string, expiresAt time.Time) bool
	IsBlacklisted(jti string) bool
}

// counter is another interface with an Add: not a blacklist.
type counter interface {
	Add(name string, at time.Time) bool
	IsBlacklisted(name string) bool
}

// memoryBlacklist is a concrete store: its own methods, not a pluggable one.
type memoryBlacklist struct{}

func (memoryBlacklist) Add(string, time.Time) bool { return true }
func (memoryBlacklist) IsBlacklisted(string) bool  { return false }

type manager struct {
	store BlacklistStore
	mem   memoryBlacklist
	count counter
}

func (m *manager) blStore() BlacklistStore { return m.store }

// Read, then blind write of the same JTI: two refreshes both read "not
// blacklisted" and both issue. Flagged at the write.
func (m *manager) refresh(jti string, exp time.Time) bool {
	if m.store.IsBlacklisted(jti) {
		return false
	}
	m.store.Add(jti, exp) // want rmw
	return true
}

// The same pair through an accessor, the write's result used: the read
// still decided first.
func (m *manager) refreshThroughAccessor(jti string, exp time.Time) bool {
	if m.blStore().IsBlacklisted(jti) {
		return false
	}
	return m.blStore().Add(jti, exp) // want rmw
}

// The write inside a nested literal still follows the read.
func (m *manager) refreshLater(jti string, exp time.Time) func() bool {
	_ = m.store.IsBlacklisted(jti)
	return func() bool {
		return m.store.Add(jti, exp) // want rmw
	}
}

// The consume alone, branching on what Add reports: not flagged.
func (m *manager) consume(jti string, exp time.Time) bool {
	return m.store.Add(jti, exp)
}

// A write of another JTI, a write before the read, a write through
// another receiver: not a read-then-write of one key.
func (m *manager) rotate(oldJTI, newJTI string, exp time.Time, other BlacklistStore) {
	_ = m.store.Add(oldJTI, exp)
	_ = m.store.IsBlacklisted(oldJTI)
	_ = m.store.Add(newJTI, exp)
	_ = other.Add(oldJTI, exp)
}

// A concrete store, and an interface that is not a BlacklistStore: not
// flagged.
func (m *manager) others(jti string, exp time.Time) {
	_ = m.mem.IsBlacklisted(jti)
	_ = m.mem.Add(jti, exp)
	_ = m.count.IsBlacklisted(jti)
	_ = m.count.Add(jti, exp)
}

// A suppressed pair, and a stale marker.
func (m *manager) suppressed(jti string, exp time.Time) {
	_ = m.store.IsBlacklisted(jti)
	_ = m.store.Add(jti, exp) //store-rmw-ok: revocation is idempotent here
	_ = jti                   //store-rmw-ok: nothing on this line any more // want stale
}
