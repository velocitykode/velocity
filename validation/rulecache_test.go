package validation

import (
	"sync"
	"testing"
)

// TestRuleCache_ParamsNotSharedAcrossCalls guards the parse cache (ruleCache):
// a custom rule that mutates its params slice in place must not corrupt the
// params seen by a later validation using the same rule set. Before the cache,
// each call parsed a fresh params slice; the cache must preserve that isolation
// via the defensive copy in validateField.
func TestRuleCache_ParamsNotSharedAcrossCalls(t *testing.T) {
	v := NewValidator().(*defaultValidator)

	var seen [][]string
	v.RegisterRule("capture_mutate", func(field string, value interface{}, params []string, data map[string]interface{}) error {
		// Record what we received, then mutate in place (a hostile/normalizing
		// custom rule). With the defensive copy this can never leak.
		snapshot := append([]string(nil), params...)
		seen = append(seen, snapshot)
		for i := range params {
			params[i] = "MUTATED"
		}
		return nil
	})

	rules := Rules{"field": {"capture_mutate:a,b,c"}}
	data := map[string]interface{}{"field": "x"}

	for i := 0; i < 3; i++ {
		if _, err := v.Validate(data, rules); err != nil {
			t.Fatalf("validate %d: %v", i, err)
		}
	}

	for i, got := range seen {
		if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
			t.Fatalf("call %d saw mutated params %v, want [a b c] - cache leaked a mutated slice", i, got)
		}
	}
}

// TestRuleCache_ConcurrentMutatingRule runs a param-mutating custom rule from
// many goroutines over the same cached rule set. Without the per-call copy this
// races on the shared backing array (run with -race).
func TestRuleCache_ConcurrentMutatingRule(t *testing.T) {
	v := NewValidator().(*defaultValidator)
	v.RegisterRule("concurrent_mutate", func(field string, value interface{}, params []string, data map[string]interface{}) error {
		for i := range params {
			params[i] = params[i] + "x"
		}
		return nil
	})

	rules := Rules{"field": {"concurrent_mutate:a,b,c,d"}}
	data := map[string]interface{}{"field": "x"}

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if _, err := v.Validate(data, rules); err != nil {
					t.Errorf("validate: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
