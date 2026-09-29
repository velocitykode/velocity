package auth

import (
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// loggerScheme is a scheme that takes a logger; its SetLogger runs code
// first. Only SetLogger is used: the embedded Scheme is nil.
type loggerScheme struct {
	Scheme
	code *hostile.Code

	mu     sync.Mutex
	logger contract.Logger
	calls  int
}

func (s *loggerScheme) SetLogger(l contract.Logger) {
	s.code.Run()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = l
	s.calls++
}

func (s *loggerScheme) held() contract.Logger {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logger
}

// The manager hands its logger to a scheme with no lock held: a scheme
// whose SetLogger panics, blocks or calls back into the manager leaves
// every entry point working, and once it behaves a retry reaches every
// scheme.
func TestManager_SchemeSetLoggerIsUserCode(t *testing.T) {
	type entry struct {
		name string
		call func(m *Manager, l contract.Logger)
	}
	entries := []entry{
		{"SetLogger", func(m *Manager, l contract.Logger) { m.SetLogger(l) }},
		{"RegisterScheme", func(m *Manager, l contract.Logger) {
			m.SetLogger(l)
			m.RegisterScheme("late", &loggerScheme{})
		}},
	}
	for _, mode := range hostile.Modes() {
		for _, e := range entries {
			t.Run(mode.String()+"/"+e.name, func(t *testing.T) {
				m := NewManager()
				logs := hostile.NewLogger(nil)
				other := &loggerScheme{}
				m.RegisterScheme("other", other)
				code := hostile.New(t, mode, func() {
					// Every entry point that reaches the logger hand-off.
					m.SetLogger(logs)
					m.RegisterScheme("inner", &loggerScheme{})
					m.SetHasher(NewBcryptHasher(12))
					m.logWarn("from inside a scheme's SetLogger")
				})
				bad := &loggerScheme{code: code}
				if mode == hostile.Block {
					// The blocked hand-off runs first, off the test
					// goroutine; the manager must keep serving meanwhile.
					m.RegisterScheme("bad", bad)
					go func() { //safe-goroutine: the test releases the block below
						defer func() { _ = recover() }()
						m.SetLogger(logs)
					}()
					<-code.Entered()
				} else {
					m.RegisterScheme("bad", bad)
				}
				call := func() { e.call(m, logs) }
				if mode == hostile.Block {
					// A hand-off reaching the blocked scheme waits on it,
					// as any call into user code does; everything else
					// must not.
					call = func() {
						m.RegisterScheme("late", &loggerScheme{})
						m.SetHasher(NewBcryptHasher(12))
						m.logWarn("while a scheme's SetLogger blocks")
					}
				}
				if p := hostile.Within(t, hostile.Deadline, call); p != nil && mode != hostile.Panic {
					t.Fatalf("%s panicked: %v", e.name, p)
				}
				code.Release()
				code.Disarm()

				// State intact: a retry hands every scheme the manager's
				// logger, and a scheme line reaches it.
				hostile.Within(t, hostile.Deadline, func() { m.SetLogger(logs) })
				for name, s := range map[string]*loggerScheme{"other": other, "bad": bad} {
					l := s.held()
					if l == nil {
						t.Fatalf("scheme %q holds no logger after the retry", name)
					}
					l.Warn("probe " + name)
					if logs.Count(hostile.Warn, "probe "+name) != 1 {
						t.Fatalf("scheme %q's logger does not reach the manager's", name)
					}
				}
			})
		}
	}
}

// A hasher's pending cost warning written through a logger that panics
// does not cut the hand-off short: SetLogger returns, and every scheme is
// handed the logger.
func TestManager_SetLoggerSurvivesAPanickingHasherWarning(t *testing.T) {
	m := NewManager()
	m.SetHasher(NewBcryptHasher(4)) // below the minimum: a warning is pending
	s := &loggerScheme{}
	m.RegisterScheme("web", s)
	code := hostile.New(t, hostile.Panic, nil)
	if p := hostile.Within(t, hostile.Deadline, func() { m.SetLogger(hostile.NewLogger(code, hostile.Warn)) }); p != nil {
		t.Fatalf("SetLogger let the hasher warning's panic out: %v", p)
	}
	if code.Calls() == 0 {
		t.Fatal("premise: the hasher wrote no warning")
	}
	if s.held() == nil {
		t.Fatal("the scheme was not handed the logger")
	}
}

// Concurrent SetLogger and RegisterScheme calls leave every scheme writing
// through the logger installed last.
func TestManager_SetLoggerRacingRegisterScheme(t *testing.T) {
	for round := 0; round < 50; round++ {
		m := NewManager()
		loggers := []*hostile.Logger{hostile.NewLogger(nil), hostile.NewLogger(nil)}
		schemes := make([]*loggerScheme, 8)
		var wg sync.WaitGroup
		for i := range schemes {
			schemes[i] = &loggerScheme{}
			wg.Go(func() { m.RegisterScheme(string(rune('a'+i)), schemes[i]) })
			wg.Go(func() { m.SetLogger(loggers[i%2]) })
		}
		wg.Wait()
		last := hostile.NewLogger(nil)
		m.SetLogger(last)
		for i, s := range schemes {
			l := s.held()
			if l == nil {
				t.Fatalf("round %d: scheme %d holds no logger", round, i)
			}
			l.Warn("probe")
		}
		if got := last.Count(hostile.Warn, "probe"); got != len(schemes) {
			t.Fatalf("round %d: %d of %d schemes write to the last logger", round, got, len(schemes))
		}
	}
}
