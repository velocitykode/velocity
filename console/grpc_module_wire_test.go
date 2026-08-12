package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyModule mirrors the shape of an existing grpc_module.go that
// predates the marker convention (e.g. one hand-written before the
// gen grpc service command existed). The wire helper must treat it as
// read-only and emit a manual snippet instead of mutating it.
const legacyModule = `package modules

import (
	"context"
	"os"

	foov1 "acme/app/api/gen/go/foo/v1"
	"acme/app/internal/grpc/services"

	"github.com/velocitykode/velocity"
	velgrpc "github.com/velocitykode/velocity/grpc"
	googleGrpc "google.golang.org/grpc"
)

type GRPCModule struct {
	server *velgrpc.Server
}

func (p *GRPCModule) Init(s *velocity.Services) error {
	p.server = velgrpc.NewServer(velgrpc.WithLogger(s.Log))

	foo := services.NewFooService()
	p.server.RegisterService(func(srv interface{}) {
		foov1.RegisterFooServiceServer(srv.(*googleGrpc.Server), foo)
	})

	return nil
}

func (p *GRPCModule) Start(s *velocity.Services) error { return p.server.Build() }
func (p *GRPCModule) Shutdown(ctx context.Context) error { return p.server.Shutdown(ctx) }
`

func writeModule(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join("internal", "modules")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir module dir: %v", err)
	}
	path := filepath.Join(dir, "grpc_module.go")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write module: %v", err)
	}
	return path
}

func TestGenGRPCService_NoMarkersDoesNotMutateModule(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")
	modulePath := writeModule(t, legacyModule)

	original, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatalf("read module: %v", err)
	}

	if err := GenGRPCService("Bar", GenGRPCServiceOptions{}); err != nil {
		t.Fatalf("GenGRPCService: %v", err)
	}

	after, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatalf("re-read module: %v", err)
	}
	if string(after) != string(original) {
		t.Errorf("module was mutated despite missing markers.\n--- before ---\n%s\n--- after ---\n%s", original, after)
	}

	// Service + proto must still be scaffolded so the user only has to do
	// the module wire manually.
	if _, err := os.Stat(filepath.Join("api", "proto", "bar", "v1", "bar.proto")); err != nil {
		t.Errorf("expected proto to be created even when module has no markers: %v", err)
	}
	if _, err := os.Stat(filepath.Join("internal", "grpc", "services", "bar.go")); err != nil {
		t.Errorf("expected service impl to be created even when module has no markers: %v", err)
	}
}

func TestWireGRPCModule_SkipsWhenServiceAlreadyRegistered(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	// Module has markers AND already registers FooService. The wire helper
	// must detect the duplicate by RegisterFooServiceServer signature and
	// leave the file untouched.
	module := strings.Replace(legacyModule,
		"foo := services.NewFooService()",
		"// vel:grpc:imports\n\t// vel:grpc:services\n\tfoo := services.NewFooService()",
		1)
	modulePath := writeModule(t, module)
	original, _ := os.ReadFile(modulePath)

	if err := wireGRPCModule(grpcScaffold{
		ServiceName: "FooService", Leaf: "foo", Version: "v1", Alias: "foopb",
		GenPkgName: "foov1", ModulePath: "acme/app",
		ServicesImport: "acme/app/internal/grpc/services", VarName: "foo",
	}); err != nil {
		t.Fatalf("wireGRPCModule: %v", err)
	}

	after, _ := os.ReadFile(modulePath)
	if string(after) != string(original) {
		t.Errorf("module was mutated for an already-registered service.\n--- before ---\n%s\n--- after ---\n%s", original, after)
	}
	if c := strings.Count(string(after), "RegisterFooServiceServer("); c != 1 {
		t.Errorf("expected RegisterFooServiceServer to remain at 1 occurrence, got %d", c)
	}
}

func TestWireGRPCModule_InjectsAtMarkersInOrder(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	// Module with markers but no service registrations yet. wireGRPCModule
	// should inject both the import and registration in the expected slots.
	module := strings.Replace(legacyModule,
		`foov1 "acme/app/api/gen/go/foo/v1"`,
		`// vel:grpc:imports`,
		1)
	module = strings.Replace(module,
		"foo := services.NewFooService()\n\tp.server.RegisterService(func(srv interface{}) {\n\t\tfoov1.RegisterFooServiceServer(srv.(*googleGrpc.Server), foo)\n\t})",
		"// vel:grpc:services",
		1)
	modulePath := writeModule(t, module)

	if err := wireGRPCModule(grpcScaffold{
		ServiceName: "FooService", Leaf: "foo", Version: "v1", Alias: "foopb",
		GenPkgName: "foov1", ModulePath: "acme/app",
		ServicesImport: "acme/app/internal/grpc/services", VarName: "foo",
	}); err != nil {
		t.Fatalf("wireGRPCModule: %v", err)
	}

	after, _ := os.ReadFile(modulePath)
	s := string(after)

	importIdx := strings.Index(s, grpcImportsMarker)
	importLineIdx := strings.Index(s, `foopb "acme/app/api/gen/go/foo/v1"`)
	servicesIdx := strings.Index(s, grpcServicesMarker)
	registerIdx := strings.Index(s, "RegisterFooServiceServer(")

	if importIdx < 0 || importLineIdx < 0 || servicesIdx < 0 || registerIdx < 0 {
		t.Fatalf("missing expected substrings in module:\n%s", s)
	}
	if importIdx >= importLineIdx {
		t.Errorf("import was not inserted after // vel:grpc:imports marker")
	}
	if servicesIdx >= registerIdx {
		t.Errorf("registration was not inserted after // vel:grpc:services marker")
	}
}
