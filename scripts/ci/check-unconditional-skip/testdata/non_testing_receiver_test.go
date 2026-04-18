// Fixture confirming the checker does NOT flag .Skip() calls on non-
// testing receivers. Real-world offenders observed: collect.Collection's
// Skip(n int) (a List.Skip abstraction), scheduler.Job.Skip(func() bool)
// (a job filter). These share the method name but have nothing to do
// with testing.TB.
package fixture

import "testing"

type collection struct{ items []int }

func (c *collection) Skip(n int) *collection { return c }

type job struct{}

func (j *job) Skip(fn func() bool) {}

func TestNonTestingSkipsIgnored(t *testing.T) {
	c := &collection{items: []int{1, 2, 3}}
	_ = c.Skip(2)

	j := &job{}
	j.Skip(func() bool { return true })

	// A real t.Skip below is unguarded — it MUST still be flagged. This
	// confirms the receiver-name filter doesn't accidentally suppress
	// real offenders in the same file.
	t.Skip("this one must flag")
}
