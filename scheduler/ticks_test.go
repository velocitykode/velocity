package scheduler

// ticksIdle returns a channel closed once every tick and task s has
// admitted has finished, as a stop would wait for them: those of its
// current or last run once that run is stopping, or those of the ticks
// run outside Run (runDueJobs on a scheduler that never ran), whose run
// it closes, so later such ticks start a run of their own.
func ticksIdle(s *Scheduler) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != nil {
		return s.run.run.Idle()
	}
	if s.adhoc == nil {
		s.adhoc = s.own.NewRun()
	}
	run := s.adhoc
	s.adhoc = nil
	run.Close()
	return run.Idle()
}

// waitTicks waits until ticksIdle(s) is closed.
func waitTicks(s *Scheduler) {
	<-ticksIdle(s)
}
