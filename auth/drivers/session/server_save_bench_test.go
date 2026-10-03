package session

import (
	"context"
	"net/http"
	"testing"
)

// headerOnlyWriter is a response writer that keeps its header map across
// saves, so the benchmark measures the save, not the recorder.
type headerOnlyWriter struct{ h http.Header }

func (w *headerOnlyWriter) Header() http.Header         { return w.h }
func (w *headerOnlyWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *headerOnlyWriter) WriteHeader(int)             {}

// BenchmarkServerStore_Save is one uncontended save of a modified session
// into its record (MemoryStore); BenchmarkServerStore_SaveUnmodified is
// the save of a session nothing changed, which must cost nothing.
func BenchmarkServerStore_Save(b *testing.B) {
	records := NewMemoryStore()
	defer records.Close(context.Background())
	store, err := NewServerStore(testConfig(), records)
	if err != nil {
		b.Fatal(err)
	}
	s, _ := store.Create("")
	s.Put("cart", "one item")
	w := &headerOnlyWriter{h: http.Header{}}
	if err := s.Save(w); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	n := 0
	for b.Loop() {
		n++
		s.Put("n", n)
		clear(w.h)
		if err := s.Save(w); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkServerStore_SaveUnmodified(b *testing.B) {
	records := NewMemoryStore()
	defer records.Close(context.Background())
	store, err := NewServerStore(testConfig(), records)
	if err != nil {
		b.Fatal(err)
	}
	s, _ := store.Create("")
	s.Put("cart", "one item")
	w := &headerOnlyWriter{h: http.Header{}}
	if err := s.Save(w); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := s.Save(w); err != nil {
			b.Fatal(err)
		}
	}
}
