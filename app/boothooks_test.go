package app

import (
	"errors"
	"testing"
)

func TestOnBoot_RunsInRegistrationOrder(t *testing.T) {
	t.Cleanup(ResetBootHooks)
	ResetBootHooks()

	var order []int
	OnBoot(func(s *Services) error { order = append(order, 1); return nil })
	OnBoot(func(s *Services) error { order = append(order, 2); return nil })
	OnBoot(nil) // must be ignored, not panic

	s := &Services{}
	if err := RunBootHooks(s); err != nil {
		t.Fatalf("RunBootHooks: %v", err)
	}
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("hooks ran in order %v, want [1 2]", order)
	}
}

func TestRunBootHooks_StopsAtFirstError(t *testing.T) {
	t.Cleanup(ResetBootHooks)
	ResetBootHooks()

	boom := errors.New("boom")
	ran := false
	OnBoot(func(s *Services) error { return boom })
	OnBoot(func(s *Services) error { ran = true; return nil })

	if err := RunBootHooks(&Services{}); !errors.Is(err, boom) {
		t.Fatalf("RunBootHooks error = %v, want %v", err, boom)
	}
	if ran {
		t.Fatal("hook after failing hook must not run")
	}
}

func TestRunBootHooks_EmptyIsNoop(t *testing.T) {
	t.Cleanup(ResetBootHooks)
	ResetBootHooks()

	if err := RunBootHooks(&Services{}); err != nil {
		t.Fatalf("RunBootHooks with no hooks: %v", err)
	}
}

func TestOnBoot_ConcurrentRegistration(t *testing.T) {
	t.Cleanup(ResetBootHooks)
	ResetBootHooks()

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			OnBoot(func(s *Services) error { return nil })
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	if err := RunBootHooks(&Services{}); err != nil {
		t.Fatalf("RunBootHooks: %v", err)
	}
}
