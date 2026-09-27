//go:build !race

package router

// raceEnabled reports whether the race detector is on. It drops items
// from sync.Pool at random, so allocation counts are not exact under it.
const raceEnabled = false
