package router

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

var benchClassifyErrors = []struct {
	name string
	err  error
}{
	{"plain", errors.New("boom")},
	{"http error", &contract.HTTPError{Status: http.StatusNotFound, Message: "missing"}},
	{"3 deep http error", fmt.Errorf("a: %w", fmt.Errorf("b: %w", fmt.Errorf("c: %w", &contract.HTTPError{Status: http.StatusConflict, Message: "taken"})))},
	{"join", errors.Join(errors.New("x"), &contract.HTTPError{Status: http.StatusBadRequest, Message: "bad"})},
}

var benchFacts errorFacts

func BenchmarkClassifyError(b *testing.B) {
	for _, c := range benchClassifyErrors {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				benchFacts = classifyError(c.err)
			}
		})
	}
}

func BenchmarkDefaultErrorHandler(b *testing.B) {
	for _, c := range benchClassifyErrors {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ctx, _ := NewTestContext(http.MethodGet, "/x")
				DefaultErrorHandler(ctx, c.err, ErrorInfo{})
			}
		})
	}
}
