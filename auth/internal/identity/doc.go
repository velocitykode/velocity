// Package identity derives a user's identity from user code in one place:
// the identifier a user's GetAuthIdentifier returns, and its text, which
// the auth package and its schemes use as keys (a JWT subject, a refresh
// generation key, a server session record's owner, a remember credential,
// the session's user id). Both calls run contained, and an identifier that
// cannot be read fails the operation with ErrUnreadable instead of
// standing in as a placeholder text two users could share.
package identity
