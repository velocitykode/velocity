package subtest

// Stale lives in an excluded package: its marker is never reported.
func Stale() {} //lock-held-ok: excluded packages are not read
