package drivers

import (
	"context"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// SetLogger may run while the driver executes statements: velocity.New
// hands the app logger at every lifecycle boundary, and a module Start can
// already be running queries by then. Run under -race.
func TestBaseDriverSetLogger_WhileQueryingIsSafe(t *testing.T) {
	driver := connectLoggingSQLite(t)
	ctx := context.Background()
	if _, err := driver.ExecContext(ctx, "CREATE TABLE raced (id INTEGER)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	aware := driver.(contract.LoggerAware)
	aware.SetLogger(&queryLog{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if _, err := driver.ExecContext(ctx, "INSERT INTO raced (id) VALUES (?)", i); err != nil {
				t.Errorf("insert: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			aware.SetLogger(&queryLog{})
		}
	}()
	wg.Wait()
}
