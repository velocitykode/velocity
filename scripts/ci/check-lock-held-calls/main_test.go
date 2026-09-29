package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestCheck runs the checker over testdata/src and compares what it
// reports with the want comments there: every line with one is reported
// with that kind, and no other line is.
func TestCheck(t *testing.T) {
	dir := filepath.Join("testdata", "src")
	hits, err := check(dir, []string{"./..."}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, h := range hits {
		parts := strings.SplitN(h, ": ", 3)
		if len(parts) < 3 {
			t.Fatalf("malformed hit %q", h)
		}
		got[parts[0]] = parts[1]
	}
	want := map[string]string{}
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		rel, _ := filepath.Rel(dir, path)
		s := bufio.NewScanner(f)
		for n := 1; s.Scan(); n++ {
			if _, kind, ok := strings.Cut(s.Text(), "// want "); ok {
				want[fmt.Sprintf("%s:%d", filepath.ToSlash(rel), n)] = strings.Fields(kind)[0]
			}
		}
		return s.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	for pos, kind := range want {
		if got[pos] != kind {
			problems = append(problems, fmt.Sprintf("%s: want %s, got %q", pos, kind, got[pos]))
		}
	}
	for pos, kind := range got {
		if _, ok := want[pos]; !ok {
			problems = append(problems, fmt.Sprintf("%s: unexpected %s", pos, kind))
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("checker output differs:\n%s\nall hits:\n%s", strings.Join(problems, "\n"), strings.Join(hits, "\n"))
	}
}

// TestScope names only the root package: the package it imports is read,
// so reach through it is still found, but its own lines are not reported.
func TestScope(t *testing.T) {
	hits, err := check(filepath.Join("testdata", "src"), []string{"."}, false)
	if err != nil {
		t.Fatal(err)
	}
	reach := false
	for _, h := range hits {
		if strings.HasPrefix(h, "sub/") {
			t.Errorf("hit outside the named package: %s", h)
		}
		reach = reach || strings.Contains(h, "CallHook")
	}
	if !reach {
		t.Errorf("reach through the imported package not found:\n%s", strings.Join(hits, "\n"))
	}
}

// TestStaleMarkers lists, with -all only, the markers that suppress no
// call under a lock, and never the markers of an excluded package.
func TestStaleMarkers(t *testing.T) {
	dir := filepath.Join("testdata", "src")
	var stale []string
	hits, err := check(dir, []string{"./..."}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if strings.Contains(h, ": stale: ") {
			stale = append(stale, h)
		}
	}
	if len(stale) != 1 || !strings.HasPrefix(stale[0], "rules.go:") {
		t.Errorf("stale markers = %q, want the one in rules.go StaleMarker", stale)
	}
	plain, err := check(dir, []string{"./..."}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range plain {
		if strings.Contains(h, ": stale: ") {
			t.Errorf("stale marker reported without -all: %s", h)
		}
	}
}

// TestHints names a fix for each kind reported, and the marker syntax.
func TestHints(t *testing.T) {
	out := hints([]string{"a.go:1: logger: l.Warn while holding mu.Lock", "b.go:2: reach: f: func g while holding mu.Lock"})
	for _, want := range []string{"2 call(s)", "logger: ", "reach: ", "//lock-held-ok: <rationale"} {
		if !strings.Contains(out, want) {
			t.Errorf("hints lack %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"func: ", "format: "} {
		if strings.Contains(out, "  "+not) {
			t.Errorf("hints name %q, which was not reported:\n%s", not, out)
		}
	}
}

func TestExcluded(t *testing.T) {
	const mod = "example.com/m"
	for path, want := range map[string]bool{
		mod:                       false,
		mod + "/cache":            false,
		mod + "/cache/cachetest":  true,
		mod + "/orm/testing":      true,
		mod + "/internal/hostile": true,
		mod + "/scripts/ci/x":     true,
		mod + "/testingutil":      false,
		mod + "/internal/latency": false,
	} {
		if got := excluded(mod, path); got != want {
			t.Errorf("excluded(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestMarker(t *testing.T) {
	for line, want := range map[string]bool{
		"f() //lock-held-ok: set only by tests":  true,
		"f() //lock-held-ok: x":                  false,
		"f() //lock-held-ok:":                    false,
		"f() // lock-held-ok: set only by tests": false,
	} {
		if got := markerRE.MatchString(line); got != want {
			t.Errorf("marker %q = %v, want %v", line, got, want)
		}
	}
}
