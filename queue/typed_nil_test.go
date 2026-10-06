package queue

import "testing"

type nilBatchRepository struct{ BatchRepository }

// The default batch repository setters refuse a typed nil as they refuse
// nil, and leave the installed repository in place.
func TestDefaultBatchRepositorySetters_RefuseATypedNil(t *testing.T) {
	var typed *nilBatchRepository
	before := defaultBatchRepo.Load()
	for name, set := range map[string]func(){
		"SetDefaultBatchRepository":    func() { SetDefaultBatchRepository(typed) },
		"EnsureDefaultBatchRepository": func() { EnsureDefaultBatchRepository(typed) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s(typed nil) did not panic", name)
				}
			}()
			set()
		}()
	}
	if got := defaultBatchRepo.Load(); got != before {
		t.Error("a refused typed nil replaced the default batch repository")
	}
}
