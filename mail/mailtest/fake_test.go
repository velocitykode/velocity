package mailtest

import (
	"context"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

func msg(subject string) *contract.Message {
	return contract.NewMessage().
		From("from@example.com").
		To("to@example.com").
		Subject(subject).
		TextBody("body")
}

func hasSubject(s string) func(*contract.Message) bool {
	return func(m *contract.Message) bool { return m.GetSubject() == s }
}

func TestFakeMailer(t *testing.T) {
	tests := []struct {
		name     string
		subjects []string
		check    func(t *testing.T, f *FakeMailer)
	}{
		{
			name:     "AssertSent_matches",
			subjects: []string{"welcome", "receipt"},
			check: func(t *testing.T, f *FakeMailer) {
				f.AssertSent(t, hasSubject("welcome"))
				f.AssertSent(t, hasSubject("receipt"))
			},
		},
		{
			name:     "AssertSent_nilMatchesAny",
			subjects: []string{"anything"},
			check: func(t *testing.T, f *FakeMailer) {
				f.AssertSent(t, nil)
			},
		},
		{
			name:     "AssertSentTimes_counts",
			subjects: []string{"dup", "dup", "other"},
			check: func(t *testing.T, f *FakeMailer) {
				f.AssertSentTimes(t, 2, hasSubject("dup"))
				f.AssertSentTimes(t, 1, hasSubject("other"))
				f.AssertSentTimes(t, 3, nil)
			},
		},
		{
			name:     "AssertNotSent_absent",
			subjects: []string{"present"},
			check: func(t *testing.T, f *FakeMailer) {
				f.AssertNotSent(t, hasSubject("missing"))
			},
		},
		{
			name:     "AssertNothingSent_empty",
			subjects: nil,
			check: func(t *testing.T, f *FakeMailer) {
				f.AssertNothingSent(t)
			},
		},
		{
			name:     "GetSent_returnsCopy",
			subjects: []string{"a", "b"},
			check: func(t *testing.T, f *FakeMailer) {
				got := f.GetSent()
				if len(got) != 2 {
					t.Fatalf("GetSent len = %d, want 2", len(got))
				}
				got[0] = nil // mutate the copy; must not affect the fake
				if again := f.GetSent(); again[0] == nil {
					t.Errorf("GetSent did not return a defensive copy")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFakeMailer()
			for _, s := range tt.subjects {
				if err := f.Send(context.Background(), msg(s)); err != nil {
					t.Fatalf("Send: %v", err)
				}
			}
			tt.check(t, f)
		})
	}
}

func TestFakeMailerConcurrentSend(t *testing.T) {
	f := NewFakeMailer()
	const n = 50

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = f.Send(context.Background(), msg("concurrent"))
			_ = f.GetSent()
		}()
	}
	wg.Wait()

	f.AssertSentTimes(t, n, hasSubject("concurrent"))
}
