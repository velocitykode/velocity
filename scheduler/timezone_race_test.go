package scheduler

import (
	"sync"
	"testing"
	"time"
)

// TestTimezone_RaceFree verifies Task 7a: SetTimezone writes under the write
// lock while runDueJobs (which reads s.timezone) snapshots under RLock, so no
// race should be reported by -race.
func TestTimezone_RaceFree(t *testing.T) {
	s := New()

	zones := []*time.Location{
		time.UTC,
		time.FixedZone("X", 3600),
		time.FixedZone("Y", -7200),
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer: rotates the timezone aggressively.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				s.SetTimezone(zones[i%len(zones)])
				i++
			}
		}
	}()

	// Reader: invokes runDueJobs repeatedly so the race detector sees the
	// interleaved access.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.runDueJobs()
			}
		}
	}()

	time.Sleep(30 * time.Millisecond)
	close(stop)
	wg.Wait()
}
