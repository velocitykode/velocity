package postgres

import (
	"testing"

	"github.com/velocitykode/velocity/orm/drivers"
)

func TestNewPostgresDriver_ReturnsDriverInterface(t *testing.T) {
	if driver := NewPostgresDriver(); driver == nil {
		t.Error("NewPostgresDriver() returned nil")
	}
}

func TestPostgresDriver_DriverName(t *testing.T) {
	if got := NewPostgresDriver().DriverName(); got != "postgres" {
		t.Errorf("DriverName() = %q, want %q", got, "postgres")
	}
}

func TestPostgresDriver_Grammar(t *testing.T) {
	grammar := NewPostgresDriver().Grammar()
	if grammar == nil {
		t.Fatal("Grammar() returned nil")
	}
	if _, ok := grammar.(*drivers.PostgresGrammar); !ok {
		t.Errorf("Grammar() returned %T, want *drivers.PostgresGrammar", grammar)
	}
}

func TestPostgresDriver_DB_ReturnsNilBeforeConnect(t *testing.T) {
	if NewPostgresDriver().DB() != nil {
		t.Error("DB() should return nil before Connect() is called")
	}
}

func TestPostgresDriver_Ping_ReturnsErrorBeforeConnect(t *testing.T) {
	err := NewPostgresDriver().Ping()
	if err == nil {
		t.Fatal("Ping() should return error before Connect()")
	}
	if err.Error() != "velocity/orm: no database connection" {
		t.Errorf("Ping() error = %q, want %q", err.Error(), "velocity/orm: no database connection")
	}
}

func TestPostgresDriver_Close_NoErrorWhenNotConnected(t *testing.T) {
	if err := NewPostgresDriver().Close(); err != nil {
		t.Errorf("Close() should not error before Connect(), got %v", err)
	}
}
