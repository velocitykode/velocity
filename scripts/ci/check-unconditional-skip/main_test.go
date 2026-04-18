package main

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestScan_Golden runs scan() against each fixture in testdata/ and
// compares the set of offending line numbers to an explicit wantLines
// list. Whole-message comparison is too brittle (renderSnippet grabs the
// source line verbatim), so we assert on the line numbers — that's what
// reviewers care about when the checker fires.
func TestScan_Golden(t *testing.T) {
	cases := []struct {
		file      string // relative to testdata/
		wantLines []int  // lines that MUST be flagged, in sorted order
	}{
		{
			file:      "unguarded_test.go",
			wantLines: []int{9, 18, 22, 40}, // TestNakedSkip, TestSkipNowUnguarded, TestSkipfUnguarded, TestSkipFarFromAnyConditional
		},
		{
			file:      "guarded_test.go",
			wantLines: nil, // every skip here is guarded
		},
		{
			file:      "non_testing_receiver_test.go",
			wantLines: []int{28}, // the one real t.Skip; the c.Skip(2) / j.Skip(func) above must NOT be flagged
		},
		{
			file:      "subtest_closure_test.go",
			wantLines: []int{30}, // closure's naked skip flagged; outer guard doesn't reach it
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("testdata", tc.file)
			findings, err := scan(path)
			if err != nil {
				t.Fatalf("scan(%s): %v", path, err)
			}

			gotLines := extractLines(t, findings)
			sort.Ints(gotLines)
			wantLines := append([]int(nil), tc.wantLines...)
			sort.Ints(wantLines)

			if !equalInts(gotLines, wantLines) {
				t.Errorf("scan(%s)\n got lines: %v\nwant lines: %v\nfull findings:\n  %s",
					path, gotLines, wantLines, strings.Join(findings, "\n  "))
			}
		})
	}
}

// TestIsSkipCall_ReceiverFilter locks in the rule that only conventional
// testing.TB receiver names (t, b, tb) are treated as test-skip calls.
// A regression here (e.g. widening to match any receiver) would
// reintroduce the false-positive class that flagged collect.Skip /
// scheduler.Job.Skip in the earlier sed-based checker.
func TestIsSkipCall_ReceiverFilter(t *testing.T) {
	// We exercise isSkipCall indirectly by scanning the fixture —
	// parsing lets us build real SelectorExpr nodes without manually
	// constructing AST. non_testing_receiver_test.go has three .Skip
	// calls: c.Skip, j.Skip, and t.Skip. Only the t.Skip should fire.
	findings, err := scan(filepath.Join("testdata", "non_testing_receiver_test.go"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	lines := extractLines(t, findings)
	if len(lines) != 1 || lines[0] != 28 {
		t.Errorf("non-testing receiver filter broken: got %v, want [28]", lines)
	}
}

// extractLines parses findings of the form "path:line:col: snippet" and
// returns the line numbers.
func extractLines(t *testing.T, findings []string) []int {
	t.Helper()
	var out []int
	for _, f := range findings {
		parts := strings.SplitN(f, ":", 4)
		if len(parts) < 3 {
			t.Fatalf("malformed finding %q", f)
		}
		line, err := strconv.Atoi(parts[1])
		if err != nil {
			t.Fatalf("bad line number in %q: %v", f, err)
		}
		out = append(out, line)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
