// Package sessionref names a session in a log line or an error without
// carrying its id. A session id is a bearer credential: whoever reads it
// can present it as the session cookie, and logs and error texts reach
// more readers than the cookie does. The auth package, its schemes and its
// session stores write Of(id) wherever a session has to be told apart, and
// never the id.
package sessionref

import (
	"crypto/sha256"
	"encoding/hex"
)

// Of returns the reference of session id: the first twelve hex characters
// of its SHA-256. It is the same on every instance and for the life of
// the session, so lines about one session can be joined, and an operator
// holding a cookie can compute it; the id cannot be recovered from it (a
// framework session id is 32 random bytes). The empty id has the empty
// reference.
func Of(id string) string {
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:6])
}
