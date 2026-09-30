package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestScan_Golden runs scan against each fixture in testdata/ and compares
// the flagged line numbers with the lines the fixture marks.
func TestScan_Golden(t *testing.T) {
	cases := []struct {
		file      string
		wantLines []int
	}{
		// a test, a helper, a package-level reference, a subtest closure,
		// a Benchmark-named method and a lower-case Benchmarkx
		{"flagged_test.go", []int{8, 15, 18, 22, 29, 33}},
		// benchmarks (with subbenchmarks, bare Benchmark, Benchmark_Under)
		// and a field that shares the name
		{"allowed_test.go", nil},
		{"alias_test.go", []int{7}},
		// the test, not the benchmark nor the field declared and assigned
		{"dot_test.go", []int{7}},
		{"notesting_test.go", nil},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			findings, err := scan(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			got := lineNumbers(t, findings)
			if !equalInts(got, tc.wantLines) {
				t.Errorf("flagged lines %v, want %v\nfindings:\n  %s", got, tc.wantLines, strings.Join(findings, "\n  "))
			}
		})
	}
}

// TestScanTree_WalksEveryGoFileButTestdata requires the walk to scan test
// files and plain .go helpers alike and to skip a nested testdata
// directory.
func TestScanTree_WalksEveryGoFileButTestdata(t *testing.T) {
	findings, err := scanTree(filepath.Join("testdata", "tree"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join("testdata", "tree", "a_test.go"), filepath.Join("testdata", "tree", "sub", "helper.go")}
	if len(findings) != len(want) {
		t.Fatalf("findings %v, want one in each of %v", findings, want)
	}
	for i, f := range findings {
		if !strings.HasPrefix(f, want[i]+":") {
			t.Errorf("finding %d = %q, want one in %s", i, f, want[i])
		}
	}
}

// TestScan_ParseError reports a file that cannot be parsed as an error,
// never as a clean file.
func TestScan_ParseError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken_test.go")
	if err := writeFile(path, "package x\nfunc {"); err != nil {
		t.Fatal(err)
	}
	if _, err := scan(path); err == nil {
		t.Error("scan of an unparsable file returned no error")
	}
}

func lineNumbers(t *testing.T, findings []string) []int {
	t.Helper()
	var lines []int
	for _, f := range findings {
		parts := strings.SplitN(f, ":", 4)
		if len(parts) < 4 {
			t.Fatalf("malformed finding %q", f)
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			t.Fatalf("malformed line in %q", f)
		}
		lines = append(lines, n)
	}
	return lines
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

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
