package storage

import (
	"context"
	"sync"
	"testing"
)

// Default reads the default disk's name and resolves it under one lock, so
// it races neither SetDefault nor Configure (run under -race).
func TestDefault_ConcurrentWithSetDefaultAndConfigure(t *testing.T) {
	m := NewManager(Config{})
	a, b := NewMemoryDriver(DiskConfig{}), NewMemoryDriver(DiskConfig{})
	m.AddDisk("a", a)
	m.AddDisk("b", b)
	if err := m.SetDefault("a"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := range 200 {
				if (i+j)%2 == 0 {
					_ = m.SetDefault("a")
				} else {
					_ = m.ConfigureWithContext(context.Background(), Config{Default: "b"})
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range 200 {
				d, err := m.Default()
				if err != nil || (d != a && d != b) {
					t.Errorf("Default = %v, %v; want one of the two disks", d, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
