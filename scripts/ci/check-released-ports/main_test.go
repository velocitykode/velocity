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

// TestCheck runs the checker over testdata and compares what it reports
// with the want comments there: every line with one is reported, and no
// other line is.
func TestCheck(t *testing.T) {
	hits, err := check("testdata")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range hits {
		parts := strings.SplitN(h, ": ", 2)
		got = append(got, parts[0])
	}
	var want []string
	err = filepath.Walk("testdata", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		rel, _ := filepath.Rel("testdata", path)
		s := bufio.NewScanner(f)
		for n := 1; s.Scan(); n++ {
			if strings.HasSuffix(strings.TrimSpace(s.Text()), "// want") {
				want = append(want, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), n))
			}
		}
		return s.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("reported %v, want %v\nall hits:\n%s", got, want, strings.Join(hits, "\n"))
	}
}
