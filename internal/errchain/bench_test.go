package errchain

import (
	"errors"
	"fmt"
	"io"
	"testing"
)

var (
	benchWrapped = fmt.Errorf("a: %w", fmt.Errorf("b: %w", fmt.Errorf("c: %w", &codeErr{1})))
	benchSink    bool
)

func BenchmarkIs(b *testing.B) {
	for _, c := range []struct {
		name string
		err  error
	}{{"identical", io.EOF}, {"miss", benchWrapped}, {"wrapped", fmt.Errorf("w: %w", io.EOF)}} {
		b.Run("errchain/"+c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				benchSink = Is(c.err, io.EOF)
			}
		})
		b.Run("errors/"+c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				benchSink = errors.Is(c.err, io.EOF)
			}
		})
	}
}

func BenchmarkAs(b *testing.B) {
	for _, c := range []struct {
		name string
		err  error
	}{{"itself", &codeErr{1}}, {"3 deep", benchWrapped}, {"miss", io.EOF}} {
		b.Run("errchain/"+c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, benchSink = As[*codeErr](c.err)
			}
		})
		b.Run("errors/"+c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var t *codeErr
				benchSink = errors.As(c.err, &t)
			}
		})
	}
}

func BenchmarkText(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		benchSink = Text(io.EOF) == ""
	}
}
