package skipped

import "testing"

func TestSkipped(t *testing.T) { _ = testing.AllocsPerRun(1, func() {}) }
