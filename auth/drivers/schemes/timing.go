package schemes

import (
	"github.com/velocitykode/velocity/auth"
)

// dummyHashForHasher returns a dummy bcrypt hash sized to match the
// configured cost of h. When h is a *auth.BcryptHasher, the hash is
// generated at that cost (memoised via auth.GetDummyBcryptHash). When h
// is some other Hasher implementation that does not expose Cost(), we
// fall back to the package default; the trade-off is one of CPU-cost
// matching, not correctness.
//
// Used by the missing-user branch of SessionScheme.Attempt and
// JWTScheme.Attempt so the timing defense from H-09 still holds when
// the operator configures BcryptCost > 10 (the package default).
// Without cost-matching, a configured cost of 14 would make the real
// verify ~5x slower than the dummy verify, reopening the username
// enumeration timing channel (F2 fix).
func dummyHashForHasher(h auth.Hasher) []byte {
	type costReporter interface {
		Cost() int
	}
	if cr, ok := h.(costReporter); ok {
		return auth.GetDummyBcryptHash(cr.Cost())
	}
	return auth.GetDummyBcryptHash(0) // 0 -> bcrypt.DefaultCost in GetDummyBcryptHash
}
