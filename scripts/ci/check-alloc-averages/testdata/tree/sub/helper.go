package sub

import "testing"

func Measure(f func()) float64 { return testing.AllocsPerRun(1, f) }
