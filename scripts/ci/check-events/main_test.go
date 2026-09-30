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
// under that rule, and no other line is.
func TestCheck(t *testing.T) {
	dir := filepath.Join("testdata", "src")
	r, err := check(dir, []string{"./..."})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, h := range r.hits {
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
			if _, rule, ok := strings.Cut(s.Text(), "// want "); ok {
				want[fmt.Sprintf("%s:%d", filepath.ToSlash(rel), n)] = strings.Fields(rule)[0]
			}
		}
		return s.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	for pos, rule := range want {
		if got[pos] != rule {
			problems = append(problems, fmt.Sprintf("%s: want %s, got %q", pos, rule, got[pos]))
		}
	}
	for pos, rule := range got {
		if _, ok := want[pos]; !ok {
			problems = append(problems, fmt.Sprintf("%s: unexpected %s", pos, rule))
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("checker output differs:\n%s\nall hits:\n%s", strings.Join(problems, "\n"), strings.Join(r.hits, "\n"))
	}
}

// TestEvents lists exactly the types the field rule treats as events:
// exported structs with a Name() string method or the envelope, directly
// or through an embedded struct.
func TestEvents(t *testing.T) {
	r, err := check(filepath.Join("testdata", "src"), []string{"./..."})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range r.events {
		_, name, _ := strings.Cut(e, ": ")
		got = append(got, name)
	}
	want := []string{"m.Base", "m.Embedding", "m.Generic", "m.Named", "m.Typed", "m.Untyped", "m.WithErrCodec"}
	sort.Strings(got)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("events = %v, want %v", got, want)
	}
}
