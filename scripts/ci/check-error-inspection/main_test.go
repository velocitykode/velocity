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
	hits, err := check(dir, []string{"./..."})
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
			// "/* want kind */" marks a line whose trailing comment is a
			// marker under test.
			line := strings.ReplaceAll(s.Text(), "/* want ", "// want ")
			if _, kind, ok := strings.Cut(line, "// want "); ok {
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

// TestHints names a fix for each kind reported, and the marker syntax.
func TestHints(t *testing.T) {
	out := hints([]string{"a.go:1: is: errors.Is", "b.go:2: text: err.Error", "c.go:3: format: fmt.Errorf", "d.go:4: nil: s == nil"})
	for _, want := range []string{"4 uncontained", "errchain.Is(", "errchain.Text(", "errchain.Errorf", "nilval.Is(v)", "//error-inspection-ok: <rationale"} {
		if !strings.Contains(out, want) {
			t.Errorf("hints lack %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"as: ", "unwrap: ", "stale: "} {
		if strings.Contains(out, "  "+not) {
			t.Errorf("hints name %q, which was not reported:\n%s", not, out)
		}
	}
}

func TestExcluded(t *testing.T) {
	const mod = "example.com/m"
	for path, want := range map[string]bool{
		mod:                         false,
		mod + "/router":             false,
		mod + "/internal/errchain":  true,
		mod + "/internal/errchainx": false,
		mod + "/queue/queuetest":    true,
		mod + "/orm/testing":        true,
		mod + "/internal/hostile":   true,
		mod + "/scripts/ci/x":       true,
		mod + "/internal/eventmeta": false,
	} {
		if got := excluded(mod, path); got != want {
			t.Errorf("excluded(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestMarker(t *testing.T) {
	for line, want := range map[string]bool{
		"f() //error-inspection-ok: a sentinel of ours":  true,
		"f() //error-inspection-ok: x":                   false,
		"f() //error-inspection-ok:":                     false,
		"f() // error-inspection-ok: a sentinel of ours": false,
	} {
		if got := markerRE.MatchString(line); got != want {
			t.Errorf("marker %q = %v, want %v", line, got, want)
		}
	}
}
