// Package fixture: a file that does not import testing cannot reference
// its AllocsPerRun, whatever it names.
package fixture

type testing struct{}

func (testing) AllocsPerRun(int, func()) float64 { return 0 }

func use() {
	var tt testing
	_ = tt.AllocsPerRun(1, func() {})
}
