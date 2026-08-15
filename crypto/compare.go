package crypto

import "crypto/subtle"

// Equal reports whether a and b are equal in constant time. Use it for any
// secret comparison (MACs, tokens, signatures) where a short-circuiting ==
// would leak a timing oracle.
func Equal(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// EqualString is Equal for strings.
func EqualString(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
