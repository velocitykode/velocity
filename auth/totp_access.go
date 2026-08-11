package auth

// RequireTwoFactor returns an Access BeforeCallback that denies any ability
// when the supplied status function reports the user has not completed a
// 2FA challenge. When status reports true (2FA satisfied) the callback
// returns nil so subsequent abilities and policies run normally.
//
// Register on an Access via access.Before(auth.RequireTwoFactor(fn)). The
// status function receives the Authenticatable as an `any` so consumer
// code can type-assert to its own user model without auth depending on
// it.
//
// When getStatus is nil the callback is a no-op (returns nil) so it does
// not accidentally lock every user out.
func RequireTwoFactor(getStatus func(actor any) bool) BeforeCallback {
	return func(user Authenticatable, ability string, args ...interface{}) *bool {
		if getStatus == nil || user == nil {
			return nil
		}
		if getStatus(user) {
			// 2FA satisfied: defer to the actual access / policy.
			return nil
		}
		deny := false
		return &deny
	}
}
