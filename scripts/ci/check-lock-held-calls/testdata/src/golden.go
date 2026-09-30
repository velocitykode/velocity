package lockheld

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"sync"
)

// Golden cases: the three places where the framework called user code
// under a lock that the checker used to miss, reduced to their shape, each
// beside the shape of its fix, which the checker must pass.

// Local storage PutStream: the stream is copied inside withRoot, under the
// root's read lock.

type storageDriver struct {
	rootMu sync.RWMutex
	root   *os.Root
}

func (d *storageDriver) withRoot(fn func(root *os.Root) error) error {
	d.rootMu.RLock()
	defer d.rootMu.RUnlock()
	if d.root == nil {
		return errors.New("closed")
	}
	return fn(d.root)
}

func (d *storageDriver) PutStreamBase(name string, stream io.Reader) error {
	return d.withRoot(func(root *os.Root) error {
		f, err := root.Create(name)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, stream) // want callback
		return err
	})
}

type storageDriverFixed struct {
	rootMu sync.RWMutex
	root   *os.Root
}

func (d *storageDriverFixed) withRoot(fn func(root *os.Root) error) error {
	d.rootMu.RLock()
	defer d.rootMu.RUnlock()
	if d.root == nil {
		return errors.New("closed")
	}
	return fn(d.root)
}

func (d *storageDriverFixed) PutStreamFixed(name string, stream io.Reader) error {
	var f *os.File
	if err := d.withRoot(func(root *os.Root) error {
		var err error
		f, err = root.Create(name)
		return err
	}); err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, stream); err != nil {
		return err
	}
	return d.withRoot(func(root *os.Root) error { return f.Sync() })
}

// Database queue pop: the job factory runs under the worker-path lock a
// helper takes and hands back as its unlock func.

var factories = map[string]func([]byte) (any, error){}

type queueDriver struct {
	mu   sync.Mutex
	rows bool
}

func (d *queueDriver) lockWorkerPath() (unlock func()) {
	if d.rows {
		return func() {}
	}
	d.mu.Lock()
	return d.mu.Unlock
}

func hydrate(kind string, data []byte) (any, error) {
	return factories[kind](data)
}

func (d *queueDriver) PopBase(kind string, data []byte) (any, error) {
	unlock := d.lockWorkerPath()
	defer unlock()
	return hydrate(kind, data) // want reach
}

func (d *queueDriver) PopFixed(kind string, data []byte) (any, error) {
	reserve := func() {
		unlock := d.lockWorkerPath()
		defer unlock()
	}
	reserve()
	return hydrate(kind, data)
}

// File cache CompareAndSwap: the expected value is encoded to compare it,
// under the key lock and the store mutex.

type fileStore struct {
	mu     sync.Mutex
	stored []byte
}

func matchesStored(stored []byte, expected any) (bool, error) {
	want, err := json.Marshal(expected)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(stored, want), nil
}

func (s *fileStore) SwapBase(expected any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	same, _ := matchesStored(s.stored, expected) // want reach
	return same
}

func (s *fileStore) SwapFixed(expected any) bool {
	want, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return reflect.DeepEqual(s.stored, want)
}
