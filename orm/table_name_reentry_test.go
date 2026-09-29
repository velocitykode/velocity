package orm

import (
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// flakyTableModel's TableName panics on its first call and names the
// table on every later one.
type flakyTableModel struct{}

var flakyTableCalls atomic.Int32

func (flakyTableModel) TableName() string {
	if flakyTableCalls.Add(1) == 1 {
		panic("table name not ready")
	}
	return "flaky_models"
}

// prefixedTableModel derives its table from another model's, calling back
// into the table name cache from inside TableName.
type prefixedTableModel struct{}

func (prefixedTableModel) TableName() string {
	return "archived_" + deriveTableName(reflect.TypeOf(b7UserProfile{}))
}

// A TableName that panics reaches its caller, and does not leave the type
// with an empty table name: the next call derives it again.
func TestDeriveTableName_PanicIsNotCached(t *testing.T) {
	typ := reflect.TypeOf(flakyTableModel{})
	tableNameCache.Delete(typ)
	flakyTableCalls.Store(0)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the first TableName panic did not reach the caller")
			}
		}()
		deriveTableName(typ)
	}()
	if got := deriveTableName(typ); got != "flaky_models" {
		t.Fatalf("table name after a panicking first call = %q, want flaky_models", got)
	}
}

// A TableName that derives another type's table, cold, returns.
func TestDeriveTableName_TableNameDerivesAnother(t *testing.T) {
	tableNameCache.Delete(reflect.TypeOf(prefixedTableModel{}))
	tableNameCache.Delete(reflect.TypeOf(b7UserProfile{}))
	done := make(chan string, 1)
	go func() { done <- deriveTableName(reflect.TypeOf(prefixedTableModel{})) }()
	select {
	case got := <-done:
		if want := "archived_" + deriveTableName(reflect.TypeOf(b7UserProfile{})); got != want {
			t.Errorf("table = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deriving a table from inside TableName did not return")
	}
}
