package contract

import (
	"reflect"
	"testing"
)

// pairsLogger records the key-value pairs of every line written to it.
type pairsLogger struct{ lines *[][]any }

func (p pairsLogger) Debug(_ string, kvs ...any) { *p.lines = append(*p.lines, kvs) }
func (p pairsLogger) Info(_ string, kvs ...any)  { *p.lines = append(*p.lines, kvs) }
func (p pairsLogger) Warn(_ string, kvs ...any)  { *p.lines = append(*p.lines, kvs) }
func (p pairsLogger) Error(_ string, kvs ...any) { *p.lines = append(*p.lines, kvs) }
func (p pairsLogger) Fatal(_ string, kvs ...any) { *p.lines = append(*p.lines, kvs) }
func (p pairsLogger) With(kvs ...any) Logger     { return BindFields(p, kvs...) }

func TestBindFields_WritesBoundPairsFirst(t *testing.T) {
	var lines [][]any
	l := BindFields(pairsLogger{lines: &lines}, "request_id", "r1").With("job_id", "j1")

	l.Debug("d", "k", 1)
	l.Info("i")
	l.Warn("w", "k", 2)
	l.Error("e", "k", 3)
	l.Fatal("f", "k", 4)

	want := [][]any{
		{"request_id", "r1", "job_id", "j1", "k", 1},
		{"request_id", "r1", "job_id", "j1"},
		{"request_id", "r1", "job_id", "j1", "k", 2},
		{"request_id", "r1", "job_id", "j1", "k", 3},
		{"request_id", "r1", "job_id", "j1", "k", 4},
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("lines = %v, want %v", lines, want)
	}
}

// A trailing key without a value is dropped so the line's own pairs keep
// their pairing, and binding never changes the logger bound from.
func TestBindFields_DropsADanglingKeyAndLeavesTheReceiver(t *testing.T) {
	var lines [][]any
	base := BindFields(pairsLogger{lines: &lines}, "a", 1, "dangling")
	child := base.With("b", 2)

	base.Info("m", "k", "v")
	child.Info("m", "k", "v")

	want := [][]any{{"a", 1, "k", "v"}, {"a", 1, "b", 2, "k", "v"}}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("lines = %v, want %v", lines, want)
	}
}

// The slice a bound line is written with is a copy: a logger that changes
// it cannot change the bound pairs.
func TestBindFields_LineSliceIsACopy(t *testing.T) {
	var lines [][]any
	l := BindFields(pairsLogger{lines: &lines}, "a", 1)

	l.Info("m")
	lines[0][1] = "changed"
	l.Info("m")

	if got := lines[1][1]; got != 1 {
		t.Errorf("bound value = %v after a written slice changed, want 1", got)
	}
}

// A bound logger compares by identity: comparing two never panics.
func TestBindFields_Comparable(t *testing.T) {
	var lines [][]any
	a := BindFields(pairsLogger{lines: &lines}, "k", "v")
	b := BindFields(pairsLogger{lines: &lines}, "k", "v")
	held := []Logger{a}
	if held[0] != a || a == b || a.With("x", 1) == a {
		t.Error("bound loggers do not compare by identity")
	}
}

func TestBindFields_Nil(t *testing.T) {
	if BindFields(nil, "k", "v") != nil {
		t.Error("BindFields(nil) != nil")
	}
}
