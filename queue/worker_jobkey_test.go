package queue

import "testing"

type contentKeyJob struct {
	N int
	S string
}

func (contentKeyJob) Handle() error { return nil }
func (contentKeyJob) Failed(error)  {}

type identifiableKeyJob struct {
	id string
}

func (identifiableKeyJob) Handle() error   { return nil }
func (identifiableKeyJob) Failed(error)    {}
func (j identifiableKeyJob) JobID() string { return j.id }

// A delete-on-pop driver (redis) re-hydrates a fresh pointer on every pop. If
// the attempt counter keyed on pointer identity, a non-Identifiable failing
// job would retry forever (the counter would reset to 1 each pop) and never
// reach failed_jobs. jobKey must derive a stable content key so the counter
// advances and MaxAttempts is enforced.
func TestWorker_jobKey_StableForNonIdentifiableContent(t *testing.T) {
	w := &Worker{}

	a := &contentKeyJob{N: 1, S: "x"}
	b := &contentKeyJob{N: 1, S: "x"}
	if w.jobKey(a) != w.jobKey(b) {
		t.Fatalf("jobKey must be stable across distinct pointers with identical content")
	}

	if w.jobKey(&contentKeyJob{N: 2, S: "x"}) == w.jobKey(a) {
		t.Fatalf("jobKey must differ for different content")
	}

	if got := w.jobKey(identifiableKeyJob{id: "job-1"}); got != "job-1" {
		t.Fatalf("jobKey(Identifiable) = %v, want job-1", got)
	}
}
