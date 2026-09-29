package scheduler

import (
	"fmt"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// namedLogger is a distinct logger per name.
type namedLogger struct{ name string }

func (*namedLogger) Debug(string, ...any)              {}
func (*namedLogger) Info(string, ...any)               {}
func (*namedLogger) Warn(string, ...any)               {}
func (*namedLogger) Error(string, ...any)              {}
func (*namedLogger) Fatal(string, ...any)              {}
func (l *namedLogger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

// Overlapping SetLogger and Add calls leave every scheduler on the
// manager's logger: no handoff read before a later one lands after it.
func TestManagerLoggerHandoff_ConcurrentSettersAndAdds(t *testing.T) {
	for round := 0; round < 50; round++ {
		m := NewManager()
		for i := 0; i < 4; i++ {
			m.Add(fmt.Sprintf("seed-%d", i), New())
		}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				for j := 0; j < 20; j++ {
					m.SetLogger(&namedLogger{name: fmt.Sprintf("l-%d-%d", i, j)})
				}
			}()
			go func() {
				defer wg.Done()
				for j := 0; j < 5; j++ {
					m.Add(fmt.Sprintf("added-%d-%d", i, j), New())
				}
			}()
		}
		wg.Wait()
		want := m.log()
		m.mu.RLock()
		for name, s := range m.schedulers {
			if got := s.log(); got != want {
				t.Fatalf("round %d: scheduler %s logger = %v, manager logger = %v", round, name, got, want)
			}
		}
		m.mu.RUnlock()
	}
}
