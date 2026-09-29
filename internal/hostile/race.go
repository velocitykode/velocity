//go:build race

package hostile

// raceEnabled reports whether the test binary runs under the race
// detector, which slows code several times over.
const raceEnabled = true
