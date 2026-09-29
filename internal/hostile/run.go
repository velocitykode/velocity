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

// Deadline is the time a call that should return promptly gets before
// Within, Eventually or AwaitEntered call it hung. It only bounds a
// failure, so it is generous: a call that returns passes as soon as it
// does. It is 5s, doubled under the race detector and doubled again when
// CI is set; VELOCITY_HOSTILE_DEADLINE (a Go duration) overrides it.
var Deadline = deadline(os.Getenv)

// deadlineEnv overrides Deadline.
const deadlineEnv = "VELOCITY_HOSTILE_DEADLINE"

func deadline(getenv func(string) string) time.Duration {
	if d, err := time.ParseDuration(getenv(deadlineEnv)); err == nil && d > 0 {
		return d
	}
	d := 5 * time.Second
	if raceEnabled {
		d *= 2
	}
	if getenv("CI") != "" {
		d *= 2
	}
	return d
}

// clamp shortens d so it ends before the test binary's own timeout, which
// would otherwise end the run with a bare panic in place of the hung
// call's stacks.
func clamp(t testing.TB, d time.Duration) time.Duration {
	dt, ok := t.(interface{ Deadline() (time.Time, bool) })
	if !ok {
		return d
	}
	end, ok := dt.Deadline()
	if !ok {
		return d
	}
	if left := time.Until(end) - time.Second; left < d {
		return max(left, 10*time.Millisecond)
	}
	return d
}

// Eventually waits until cond holds, polling it, and fails the test when
// it has not within d. It is for waiting on evidence (a count reached, a
// goroutine parked, a state published), never for ordering by time.
func Eventually(t testing.TB, d time.Duration, what string, cond func() bool) bool {
	t.Helper()
	end := time.Now().Add(clamp(t, d))
	for pause := 50 * time.Microsecond; !cond(); pause = min(2*pause, 10*time.Millisecond) {
		if time.Now().After(end) {
			t.Errorf("hostile: %s did not happen within %v", what, d)
			return false
		}
		time.Sleep(pause)
	}
	return true
}

// AwaitEntered waits until Run has started, and fails the test when it has
// not within Deadline: the component never called the user code, so a
// test waiting on it would hang to the binary's timeout instead.
func (c *Code) AwaitEntered(t testing.TB) bool {
	t.Helper()
	timer := time.NewTimer(clamp(t, Deadline))
	defer timer.Stop()
	select {
	case <-c.entered:
		return true
	case <-timer.C:
		t.Errorf("hostile: the user code was never called within %v", Deadline)
		return false
	}
}

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
	timer := time.NewTimer(clamp(t, d))
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
