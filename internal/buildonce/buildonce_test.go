package buildonce

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// Many goroutines asking for one key at once: one build, one value.
func TestDo_BuildsOnceUnderConcurrency(t *testing.T) {
	var g Group[int]
	var builds atomic.Int32
	release := make(chan struct{})
	build := func() (int, error) {
		builds.Add(1)
		<-release
		return 42, nil
	}
	var wg sync.WaitGroup
	results := make(chan int, 64)
	for range 64 {
		wg.Go(func() {
			v, err := g.Do(context.Background(), "k", build)
			if err != nil {
				t.Error(err)
			}
			results <- v
		})
	}
	hostile.Eventually(t, hostile.Deadline, "every other caller waiting on the build", func() bool {
		return g.Joined("k") == 63
	})
	close(release)
	hostile.Within(t, hostile.Deadline, wg.Wait)
	close(results)
	for v := range results {
		if v != 42 {
			t.Fatalf("a caller got %d, want 42", v)
		}
	}
	// Every caller joined the one build before it finished.
	if n := builds.Load(); n != 1 {
		t.Fatalf("builds = %d, want 1", n)
	}
}

// While one build runs, the other callers wait and do not build.
func TestDo_WaitersDoNotBuild(t *testing.T) {
	var g Group[string]
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = g.Do(context.Background(), "k", func() (string, error) {
			close(started)
			<-release
			return "v", nil
		})
	}()
	<-started
	got := make(chan string)
	go func() {
		v, _ := g.Do(context.Background(), "k", func() (string, error) {
			t.Error("a waiter built the value")
			return "", nil
		})
		got <- v
	}()
	hostile.Eventually(t, hostile.Deadline, "the waiter joining the build", func() bool { return g.Joined("k") == 1 })
	close(release)
	if v := <-got; v != "v" {
		t.Fatalf("waiter got %q, want v", v)
	}
}

// A waiter returns at its ctx; the build carries on.
func TestDo_WaiterHonoursCtx(t *testing.T) {
	var g Group[int]
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		_, _ = g.Do(context.Background(), "k", func() (int, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	hostile.Within(t, hostile.Deadline, func() {
		if _, err := g.Do(ctx, "k", func() (int, error) { return 0, nil }); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want the ctx deadline", err)
		}
	})
}

// A build that asks for its own key gets an error at once instead of
// waiting on itself; other keys build normally.
func TestDo_ReentryReturnsAnError(t *testing.T) {
	var g Group[int]
	hostile.Within(t, hostile.Deadline, func() {
		v, err := g.Do(context.Background(), "k", func() (int, error) {
			if _, err := g.Do(context.Background(), "k", func() (int, error) { return 0, nil }); err == nil ||
				!strings.Contains(err.Error(), "inside its own build") {
				t.Errorf("re-entrant Do err = %v, want the re-entry error", err)
			}
			other, err := g.Do(context.Background(), "other", func() (int, error) { return 2, nil })
			if err != nil || other != 2 {
				t.Errorf("another key from inside a build = %d, %v", other, err)
			}
			return 1, nil
		})
		if err != nil || v != 1 {
			t.Errorf("Do = %d, %v", v, err)
		}
	})
}

// A failed build frees the key: waiters get the error, the next Do builds
// again.
func TestDo_ErrorFreesTheKey(t *testing.T) {
	var g Group[int]
	boom := errors.New("boom")
	if _, err := g.Do(context.Background(), "k", func() (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	v, err := g.Do(context.Background(), "k", func() (int, error) { return 3, nil })
	if err != nil || v != 3 {
		t.Fatalf("retry = %d, %v", v, err)
	}
}

// A panicking build reaches its caller, frees the key, and wakes waiters
// with an error.
func TestDo_PanicFreesTheKey(t *testing.T) {
	var g Group[int]
	started := make(chan struct{})
	release := make(chan struct{})
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		_, _ = g.Do(context.Background(), "k", func() (int, error) {
			close(started)
			<-release
			panic(hostile.PanicValue)
		})
	}()
	<-started
	waiterErr := make(chan error, 1)
	go func() {
		_, err := g.Do(context.Background(), "k", func() (int, error) { return 0, nil })
		waiterErr <- err
	}()
	hostile.Eventually(t, hostile.Deadline, "the waiter joining the build", func() bool { return g.Joined("k") == 1 })
	close(release)
	if p := <-panicked; p != hostile.PanicValue {
		t.Fatalf("the builder's caller got %v, want the panic", p)
	}
	var pe *panicerr.Error
	if err := <-waiterErr; !errors.As(err, &pe) || pe.Recovered() != hostile.PanicValue {
		t.Fatalf("waiter err = %v, want the panic as a *panicerr.Error", err)
	}
	v, err := g.Do(context.Background(), "k", func() (int, error) { return 4, nil })
	if err != nil || v != 4 {
		t.Fatalf("retry = %d, %v", v, err)
	}
}

// A build that panics with a value whose String blocks frees the key: the
// panic value is kept as it is, never formatted before the key is freed,
// so the next Do for the key builds anew.
func TestDo_PanicValueIsNotFormattedBeforeTheKeyIsFreed(t *testing.T) {
	var g Group[int]
	code := hostile.New(t, hostile.Block, nil)
	value := hostile.NewValue(code, "blocking panic value")
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		_, _ = g.Do(context.Background(), "k", func() (int, error) { panic(value) })
	}()
	var p any
	hostile.Within(t, hostile.Deadline, func() { p = <-panicked })
	if p != value {
		t.Fatalf("the builder's caller got %v, want the panic value", p)
	}
	var v int
	var err error
	hostile.Within(t, hostile.Deadline, func() {
		v, err = g.Do(context.Background(), "k", func() (int, error) { return 5, nil })
	})
	if err != nil || v != 5 {
		t.Fatalf("retry = %d, %v", v, err)
	}
	if code.Calls() != 0 {
		t.Errorf("the panic value was formatted %d times by Do", code.Calls())
	}
}

// A build that exits its goroutine (runtime.Goexit, as t.FailNow does)
// frees the key and wakes waiters.
func TestDo_GoexitFreesTheKey(t *testing.T) {
	var g Group[int]
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = g.Do(context.Background(), "k", func() (int, error) {
			runtimeGoexit()
			return 0, nil
		})
	}()
	<-done
	hostile.Within(t, hostile.Deadline, func() {
		v, err := g.Do(context.Background(), "k", func() (int, error) { return 5, nil })
		if err != nil || v != 5 {
			t.Errorf("retry = %d, %v", v, err)
		}
	})
}

func TestDo_NilCtx(t *testing.T) {
	var g Group[int]
	//lint:ignore SA1012 Do accepts a nil ctx, and this test checks it
	v, err := g.Do(nil, "k", func() (int, error) { return 6, nil })
	if err != nil || v != 6 {
		t.Fatalf("Do(nil ctx) = %d, %v", v, err)
	}
}

// TestImports keeps the package a leaf: the standard library,
// internal/goroutine and internal/panicerr only.
func TestImports(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if p == "github.com/velocitykode/velocity/internal/goroutine" || p == "github.com/velocitykode/velocity/internal/panicerr" {
					continue
				}
				if first, _, _ := strings.Cut(p, "/"); strings.Contains(first, ".") {
					t.Errorf("%s imports %s", name, p)
				}
			}
		}
	}
}

func runtimeGoexit() { runtime.Goexit() }

// Joined counts the callers waiting on the build in progress, and is 0
// once it finished.
func TestJoined(t *testing.T) {
	var g Group[int]
	if n := g.Joined("k"); n != 0 {
		t.Fatalf("Joined with no build = %d", n)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = g.Do(context.Background(), "k", func() (int, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	<-started
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() { _, _ = g.Do(context.Background(), "k", nil) })
	}
	hostile.Eventually(t, hostile.Deadline, "three joined", func() bool { return g.Joined("k") == 3 })
	close(release)
	hostile.Within(t, hostile.Deadline, wg.Wait)
	<-done
	if n := g.Joined("k"); n != 0 {
		t.Fatalf("Joined after the build = %d, want 0", n)
	}
}
