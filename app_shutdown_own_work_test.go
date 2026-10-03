package velocity

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/storage"
)

// A request handler is work the teardown waits for (the router's request
// run): App.Shutdown called from it is refused at once and changes
// nothing, and the request completes.
func TestAppShutdown_FromARequestHandlerIsRefused(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	var fromHandler error
	a.Router.Get("/stop", func(c *router.Context) error {
		fromHandler = a.Shutdown(context.Background())
		return c.NoContent()
	})
	w := httptest.NewRecorder()
	hostile.Within(t, hostile.Deadline, func() {
		a.Router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/stop", nil))
	})
	if !errors.Is(fromHandler, contract.ErrStopFromOwnWork) {
		t.Fatalf("App.Shutdown from a handler = %v, want contract.ErrStopFromOwnWork", fromHandler)
	}
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: the refused Shutdown changed nothing", w.Code)
	}
	hostile.Within(t, hostile.Deadline, func() {
		if err := a.Shutdown(context.Background()); err != nil {
			t.Errorf("App.Shutdown from outside = %v, want nil", err)
		}
	})
}

// stopOnShutdownDisk is a storage disk whose Shutdown calls stop.
type stopOnShutdownDisk struct {
	storage.Driver
	stop func() error
	err  error
}

func (d *stopOnShutdownDisk) Shutdown(context.Context) error {
	d.err = d.stop()
	return nil
}

// A manager closing its children is work the teardown waits for:
// App.Shutdown called from a storage disk's Shutdown is refused at once.
func TestAppShutdown_FromAManagerChildCloseIsRefused(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	m, ok := a.Storage.(*storage.Manager)
	if !ok {
		t.Skipf("storage is %T, not *storage.Manager", a.Storage)
	}
	disk := &stopOnShutdownDisk{stop: func() error { return a.Shutdown(context.Background()) }}
	m.AddDisk("hostile", disk)
	hostile.Within(t, hostile.Deadline, func() {
		if err := a.Shutdown(context.Background()); err != nil {
			t.Errorf("App.Shutdown = %v, want nil", err)
		}
	})
	if !errors.Is(disk.err, contract.ErrStopFromOwnWork) {
		t.Fatalf("App.Shutdown from a disk's Shutdown = %v, want contract.ErrStopFromOwnWork", disk.err)
	}
}

// A typed-nil service is absent: App.Shutdown's own-work check skips it
// instead of calling OwnsCaller on a nil manager.
func TestAppShutdown_TypedNilManagerIsAbsent(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	real := a.Storage
	a.Storage = (*storage.Manager)(nil)
	var p any
	hostile.Within(t, hostile.Deadline, func() {
		defer func() { p = recover() }()
		_ = a.childOwnsCaller()
	})
	a.Storage = real
	if p != nil {
		t.Fatalf("childOwnsCaller with a typed-nil storage manager panicked: %v", p)
	}
	_ = a.Shutdown(context.Background())
}

// stopReader is an upload body whose first Read calls stop.
type stopReader struct {
	stop func() error
	err  error
	read bool
}

func (r *stopReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	r.err = r.stop()
	return copy(p, "x"), nil
}

// A local-disk upload is work the teardown waits for (the disk's Shutdown
// waits for the write in flight): App.Shutdown called from the upload's
// reader is refused at once and changes nothing, and the upload commits.
func TestAppShutdown_FromAnUploadReaderIsRefused(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	m, ok := a.Storage.(*storage.Manager)
	if !ok {
		t.Fatalf("storage is %T, want *storage.Manager", a.Storage)
	}
	disk := storage.NewLocalDriver(storage.DiskConfig{Root: t.TempDir()})
	m.AddDisk("uploads", disk)
	body := &stopReader{stop: func() error { return a.Shutdown(context.Background()) }}
	var putErr error
	hostile.Within(t, hostile.Deadline, func() { putErr = disk.PutStream("obj", body) })
	if !errors.Is(body.err, contract.ErrStopFromOwnWork) {
		t.Fatalf("App.Shutdown from an upload reader = %v, want contract.ErrStopFromOwnWork", body.err)
	}
	if putErr != nil {
		t.Fatalf("PutStream whose reader's Shutdown was refused = %v, want nil", putErr)
	}
	if got, err := disk.Get("obj"); err != nil || string(got) != "x" {
		t.Fatalf("Get after the upload = %q, %v; want %q", got, err, "x")
	}
	hostile.Within(t, hostile.Deadline, func() {
		if err := a.Shutdown(context.Background()); err != nil {
			t.Errorf("App.Shutdown from outside = %v, want nil", err)
		}
	})
}

// gatedStopReader is an upload body whose first Read signals entered,
// waits for gate, then calls stop.
type gatedStopReader struct {
	entered chan struct{}
	gate    chan struct{}
	stop    func() error
	err     error
	read    bool
}

func (r *gatedStopReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	close(r.entered)
	<-r.gate
	r.err = r.stop()
	return copy(p, "x"), nil
}

// A teardown under way has taken the disks out of the storage manager's
// registry while their uploads drain. App.Shutdown called from the reader
// of such an upload is still refused at once: the teardown waits for that
// upload, so the call would wait on itself. The upload commits and the
// teardown from outside completes.
func TestAppShutdown_FromAnUploadReaderOnADrainingDiskIsRefused(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	m, ok := a.Storage.(*storage.Manager)
	if !ok {
		t.Fatalf("storage is %T, want *storage.Manager", a.Storage)
	}
	disk := storage.NewLocalDriver(storage.DiskConfig{Root: t.TempDir()})
	m.AddDisk("uploads", disk)
	body := &gatedStopReader{
		entered: make(chan struct{}),
		gate:    make(chan struct{}),
		stop:    func() error { return a.Shutdown(context.Background()) },
	}
	t.Cleanup(func() {
		select {
		case <-body.gate:
		default:
			close(body.gate)
		}
	})
	put := make(chan error, 1)
	go func() { put <- disk.PutStream("obj", body) }()
	hostile.Within(t, hostile.Deadline, func() { <-body.entered })
	outside := make(chan error, 1)
	go func() { outside <- a.Shutdown(context.Background()) }()
	if !hostile.Eventually(t, hostile.Deadline, "the teardown to reach the storage disks", func() bool {
		_, err := m.Disk("uploads")
		return err != nil
	}) {
		return
	}
	close(body.gate)
	hostile.Within(t, hostile.Deadline, func() {
		if err := <-put; err != nil {
			t.Errorf("PutStream = %v, want nil: the teardown waits for the upload", err)
		}
		if err := <-outside; err != nil {
			t.Errorf("App.Shutdown from outside = %v, want nil", err)
		}
	})
	if !errors.Is(body.err, contract.ErrStopFromOwnWork) {
		t.Fatalf("App.Shutdown from an upload reader on a draining disk = %v, want contract.ErrStopFromOwnWork", body.err)
	}
}

// panicOwnerDisk is a storage disk whose OwnsCaller panics.
type panicOwnerDisk struct {
	storage.Driver
	shutdowns int
}

func (d *panicOwnerDisk) OwnsCaller() bool { panic(hostile.PanicValue) }

func (d *panicOwnerDisk) Shutdown(context.Context) error {
	d.shutdowns++
	return nil
}

// A disk's OwnsCaller is user code: one that panics stays inside the
// own-work check, which takes it as not owning the caller, and the
// teardown runs and shuts the disk down.
func TestAppShutdown_ADiskWhoseOwnsCallerPanicsIsContained(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	m, ok := a.Storage.(*storage.Manager)
	if !ok {
		t.Fatalf("storage is %T, want *storage.Manager", a.Storage)
	}
	disk := &panicOwnerDisk{}
	m.AddDisk("hostile", disk)
	var shutdownErr error
	if p := hostile.Within(t, hostile.Deadline, func() { shutdownErr = a.Shutdown(context.Background()) }); p != nil {
		t.Fatalf("App.Shutdown panicked: %v", p)
	}
	if shutdownErr != nil {
		t.Errorf("App.Shutdown = %v, want nil", shutdownErr)
	}
	if disk.shutdowns != 1 {
		t.Errorf("the disk was shut down %d times, want 1", disk.shutdowns)
	}
}

// panicOwnerStorage is a storage service a module installs around the
// framework's: its OwnsCaller panics.
type panicOwnerStorage struct {
	contract.StorageManager
}

func (s *panicOwnerStorage) OwnsCaller() bool { panic(hostile.PanicValue) }

func (s *panicOwnerStorage) Unwrap() contract.StorageManager { return s.StorageManager }

// panicOwnerModule installs a panicOwnerStorage at Start.
type panicOwnerModule struct{}

func (panicOwnerModule) Init(*app.Services) error { return nil }
func (panicOwnerModule) Start(s *app.Services) error {
	s.Storage = &panicOwnerStorage{StorageManager: s.Storage}
	return nil
}
func (panicOwnerModule) Shutdown(context.Context) error { return nil }

// A service a module installed is user code, and so is its OwnsCaller:
// one that panics stays inside App.Shutdown's own-work check, which takes
// it as not owning the caller and writes one warning for that service,
// however often it is asked; the teardown runs.
func TestAppShutdown_AServiceWhoseOwnsCallerPanicsIsContainedAndReportedOnce(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	a, err := NewTestApp(WithModules(panicOwnerModule{}))
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	if _, ok := a.Storage.(*panicOwnerStorage); !ok {
		t.Fatalf("storage is %T, want the module's *panicOwnerStorage", a.Storage)
	}
	for range 2 {
		var shutdownErr error
		if p := hostile.Within(t, hostile.Deadline, func() { shutdownErr = a.Shutdown(context.Background()) }); p != nil {
			t.Fatalf("App.Shutdown panicked: %v", p)
		}
		if shutdownErr != nil {
			t.Errorf("App.Shutdown = %v, want nil", shutdownErr)
		}
	}
	if n := out.Count("WARN", "velocity: OwnsCaller panicked"); n != 1 {
		t.Fatalf("%d warnings after two Shutdowns, want 1; output:\n%s", n, out)
	}
}
