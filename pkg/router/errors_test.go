package router

import (
	"errors"
	"net/http"
	"testing"
)

func TestNewHTTPError(t *testing.T) {
	t.Run("default message from status code", func(t *testing.T) {
		err := NewHTTPError(http.StatusNotFound)
		if err.Code != http.StatusNotFound {
			t.Errorf("expected code %d, got %d", http.StatusNotFound, err.Code)
		}
		if err.Message != "Not Found" {
			t.Errorf("expected message 'Not Found', got %q", err.Message)
		}
		if err.Error() != "Not Found" {
			t.Errorf("expected Error() 'Not Found', got %q", err.Error())
		}
	})

	t.Run("custom message", func(t *testing.T) {
		err := NewHTTPError(http.StatusBadRequest, "invalid email")
		if err.Code != http.StatusBadRequest {
			t.Errorf("expected code %d, got %d", http.StatusBadRequest, err.Code)
		}
		if err.Message != "invalid email" {
			t.Errorf("expected message 'invalid email', got %q", err.Message)
		}
	})

	t.Run("empty custom message uses status text", func(t *testing.T) {
		err := NewHTTPError(http.StatusForbidden, "")
		if err.Message != "Forbidden" {
			t.Errorf("expected message 'Forbidden', got %q", err.Message)
		}
	})

	t.Run("internal error unwrap", func(t *testing.T) {
		inner := errors.New("database connection failed")
		err := NewHTTPError(http.StatusInternalServerError)
		err.Internal = inner

		if !errors.Is(err, inner) {
			t.Error("expected errors.Is to find internal error")
		}
	})

	t.Run("nil internal error", func(t *testing.T) {
		err := NewHTTPError(http.StatusOK)
		if err.Unwrap() != nil {
			t.Error("expected nil Unwrap for no internal error")
		}
	})
}
