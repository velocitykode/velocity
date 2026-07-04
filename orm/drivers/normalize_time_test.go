package drivers

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

type stubTimeValuer struct{ t time.Time }

func (s stubTimeValuer) Value() (driver.Value, error) { return s.t, nil }

type stubStringValuer struct{ s string }

func (s stubStringValuer) Value() (driver.Value, error) { return s.s, nil }

type stubErrValuer struct{}

func (stubErrValuer) Value() (driver.Value, error) { return nil, errors.New("boom") }

type stubPtrValuer struct{ t time.Time }

func (p *stubPtrValuer) Value() (driver.Value, error) { return p.t, nil }

func TestNormalizeTimeArgs(t *testing.T) {
	karachi, err := time.LoadLocation("Asia/Karachi")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	local := time.Date(2026, 7, 4, 14, 15, 0, 0, karachi)
	utc := local.In(time.UTC)

	t.Run("time.Time rebased to UTC", func(t *testing.T) {
		out := NormalizeTimeArgs([]any{"x", local, 42})
		got, ok := out[1].(time.Time)
		if !ok {
			t.Fatalf("out[1] = %T, want time.Time", out[1])
		}
		if got.Location() != time.UTC {
			t.Errorf("Location = %v, want UTC", got.Location())
		}
		if !got.Equal(local) {
			t.Errorf("instant changed: got %v, want %v", got, local)
		}
		if got.Hour() != utc.Hour() {
			t.Errorf("wall clock hour = %d, want %d", got.Hour(), utc.Hour())
		}
		if out[0] != "x" || out[2] != 42 {
			t.Errorf("non-time args altered: %v", out)
		}
	})

	t.Run("pointer rebased without mutating pointee", func(t *testing.T) {
		p := &local
		out := NormalizeTimeArgs([]any{p})
		got, ok := out[0].(*time.Time)
		if !ok {
			t.Fatalf("out[0] = %T, want *time.Time", out[0])
		}
		if got == p {
			t.Error("pointer not replaced; caller's pointee at risk of mutation")
		}
		if got.Location() != time.UTC || !got.Equal(local) {
			t.Errorf("got %v (%v), want same instant in UTC", got, got.Location())
		}
		if p.Location() != karachi {
			t.Errorf("caller's pointee mutated: location %v", p.Location())
		}
	})

	t.Run("nil pointer untouched", func(t *testing.T) {
		var p *time.Time
		out := NormalizeTimeArgs([]any{p})
		if out[0] != any(p) {
			t.Errorf("nil *time.Time altered: %v", out[0])
		}
	})

	t.Run("NullTime valid rebased", func(t *testing.T) {
		out := NormalizeTimeArgs([]any{sql.NullTime{Time: local, Valid: true}})
		got := out[0].(sql.NullTime)
		if got.Time.Location() != time.UTC || !got.Time.Equal(local) || !got.Valid {
			t.Errorf("got %+v, want same instant in UTC, Valid", got)
		}
	})

	t.Run("NullTime invalid untouched", func(t *testing.T) {
		in := sql.NullTime{Time: local, Valid: false}
		out := NormalizeTimeArgs([]any{in})
		if out[0].(sql.NullTime) != in {
			t.Errorf("invalid NullTime altered: %+v", out[0])
		}
	})

	t.Run("Valuer yielding non-UTC time is resolved and rebased", func(t *testing.T) {
		out := NormalizeTimeArgs([]any{stubTimeValuer{t: local}})
		got, ok := out[0].(time.Time)
		if !ok {
			t.Fatalf("out[0] = %T, want resolved time.Time", out[0])
		}
		if got.Location() != time.UTC || !got.Equal(local) {
			t.Errorf("got %v (%v), want same instant in UTC", got, got.Location())
		}
	})

	t.Run("Valuer yielding UTC time untouched", func(t *testing.T) {
		v := stubTimeValuer{t: utc}
		out := NormalizeTimeArgs([]any{v})
		if out[0].(stubTimeValuer) != v {
			t.Errorf("UTC-yielding Valuer altered: %+v", out[0])
		}
	})

	t.Run("non-time Valuer passes through", func(t *testing.T) {
		v := stubStringValuer{s: "x"}
		out := NormalizeTimeArgs([]any{v})
		if out[0].(stubStringValuer) != v {
			t.Errorf("Valuer altered: %+v", out[0])
		}
	})

	t.Run("erroring Valuer passes through for database/sql to surface", func(t *testing.T) {
		out := NormalizeTimeArgs([]any{stubErrValuer{}})
		if _, ok := out[0].(stubErrValuer); !ok {
			t.Errorf("erroring Valuer altered: %+v", out[0])
		}
	})

	t.Run("typed nil pointer Valuer passes through", func(t *testing.T) {
		var p *stubPtrValuer
		out := NormalizeTimeArgs([]any{p})
		if got, ok := out[0].(*stubPtrValuer); !ok || got != nil {
			t.Errorf("nil pointer Valuer altered: %+v", out[0])
		}
	})

	t.Run("NamedArg with non-UTC time rebased, Name preserved", func(t *testing.T) {
		out := NormalizeTimeArgs([]any{sql.Named("ts", local)})
		got, ok := out[0].(sql.NamedArg)
		if !ok {
			t.Fatalf("out[0] = %T, want sql.NamedArg", out[0])
		}
		if got.Name != "ts" {
			t.Errorf("Name = %q, want ts", got.Name)
		}
		tm, ok := got.Value.(time.Time)
		if !ok {
			t.Fatalf("Value = %T, want time.Time", got.Value)
		}
		if tm.Location() != time.UTC || !tm.Equal(local) {
			t.Errorf("Value = %v (%v), want same instant in UTC", tm, tm.Location())
		}
	})

	t.Run("NamedArg wrapping pointer rebased without mutating pointee", func(t *testing.T) {
		p := &local
		out := NormalizeTimeArgs([]any{sql.Named("ts", p)})
		got := out[0].(sql.NamedArg)
		q, ok := got.Value.(*time.Time)
		if !ok {
			t.Fatalf("Value = %T, want *time.Time", got.Value)
		}
		if q == p {
			t.Error("pointer not replaced inside NamedArg")
		}
		if q.Location() != time.UTC || !q.Equal(local) {
			t.Errorf("Value = %v (%v), want same instant in UTC", q, q.Location())
		}
		if p.Location() != karachi {
			t.Errorf("caller's pointee mutated: location %v", p.Location())
		}
	})

	t.Run("NamedArg with non-time value untouched", func(t *testing.T) {
		in := sql.Named("n", 42)
		out := NormalizeTimeArgs([]any{in})
		got := out[0].(sql.NamedArg)
		if got.Name != "n" || got.Value != 42 {
			t.Errorf("non-time NamedArg altered: %+v", got)
		}
	})

	t.Run("NamedArg with UTC time untouched", func(t *testing.T) {
		args := []any{sql.Named("ts", utc)}
		out := NormalizeTimeArgs(args)
		if &out[0] != &args[0] {
			t.Error("slice copied despite UTC NamedArg")
		}
	})

	t.Run("no time args returns same slice without alloc", func(t *testing.T) {
		args := []any{"a", 1, 2.5, []byte("b"), nil}
		out := NormalizeTimeArgs(args)
		if &out[0] != &args[0] {
			t.Error("slice copied despite no time args")
		}
		allocs := testing.AllocsPerRun(100, func() {
			_ = NormalizeTimeArgs(args)
		})
		if allocs != 0 {
			t.Errorf("fast path allocs = %v, want 0", allocs)
		}
	})

	t.Run("already-UTC values returns same slice", func(t *testing.T) {
		args := []any{utc, sql.NullTime{Time: utc, Valid: true}}
		out := NormalizeTimeArgs(args)
		if &out[0] != &args[0] {
			t.Error("slice copied despite all-UTC args")
		}
	})

	t.Run("input slice never mutated", func(t *testing.T) {
		args := []any{local}
		_ = NormalizeTimeArgs(args)
		if got := args[0].(time.Time); got.Location() != karachi {
			t.Errorf("input slice mutated: location %v", got.Location())
		}
	})
}
