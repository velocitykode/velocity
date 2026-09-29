package hostile

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Deadline is the default time Within allows before it calls a call hung.
const Deadline = 5 * time.Second

// Within runs fn on a goroutine of its own and waits for it. When fn has
// not returned within d, it fails the test with every goroutine's stack
// and returns; fn keeps running. When fn panics, Within recovers and
// returns the panic value (nil when fn returned), so the test decides
// whether a panic reaching the caller is expected.
func Within(t testing.TB, d time.Duration, fn func()) (panicked any) {
	t.Helper()
	type result struct{ p any }
	done := make(chan result, 1)
	go func() { //safe-goroutine: hands fn's panic back to the test through done
		var r result
		defer func() {
			if p := recover(); p != nil {
				r.p = p
			}
			done <- r
		}()
		fn()
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.p
	case <-timer.C:
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Errorf("hostile: call did not return within %v (hung); goroutines:\n%s", d, buf[:n])
		return nil
	}
}

// isolatedEnv names the test a child process started by Isolated runs fn
// for.
const isolatedEnv = "VELOCITY_HOSTILE_ISOLATED"

// Isolated runs the calling test again in a child process that runs only
// that test, and fails the test with the child's output when the child
// fails or exits abnormally: a panic that escapes on any goroutine fails
// this one test instead of killing the whole package run. In the child,
// Isolated runs fn. The child is the same test binary, so -race carries
// over; -test.timeout is passed on, coverage flags are not.
func Isolated(t *testing.T, fn func()) {
	t.Helper()
	if os.Getenv(isolatedEnv) == t.Name() {
		fn()
		return
	}
	out, err := runIsolated(t.Name())
	if err != nil {
		t.Fatalf("hostile: isolated run of %s failed: %v\n%s", t.Name(), err, out)
	}
}

// runIsolated runs the test named name in a child process and returns its
// output, and an error when the child failed or never ran the test.
func runIsolated(name string) ([]byte, error) {
	args := []string{"-test.run=" + runPattern(name), "-test.count=1", "-test.v"}
	if f := flag.Lookup("test.timeout"); f != nil {
		args = append(args, "-test.timeout="+f.Value.String())
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), isolatedEnv+"="+name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, err
	}
	if !strings.Contains(string(out), "--- PASS: "+name) {
		return out, fmt.Errorf("the child process did not run %s", name)
	}
	return out, nil
}

// runPattern returns the -test.run pattern that selects exactly the test
// or subtest name.
func runPattern(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}
