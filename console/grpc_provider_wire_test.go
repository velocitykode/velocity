package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyProvider mirrors the shape of an existing grpc_provider.go that
// predates the marker convention (e.g. one hand-written before the
// gen grpc service command existed). The wire helper must treat it as
// read-only and emit a manual snippet instead of mutating it.
const legacyProvider = `package providers

import (
	"context"
	"os"

	foov1 "acme/app/api/gen/go/foo/v1"
	"acme/app/internal/grpc/services"

	"github.com/velocitykode/velocity"
	velgrpc "github.com/velocitykode/velocity/grpc"
	googleGrpc "google.golang.org/grpc"
)

type GRPCProvider struct {
	server *velgrpc.Server
}

func (p *GRPCProvider) Register(s *velocity.Services) error {
	p.server = velgrpc.NewServer(velgrpc.WithLogger(s.Log))

	foo := services.NewFooService()
	p.server.RegisterService(func(srv interface{}) {
		foov1.RegisterFooServiceServer(srv.(*googleGrpc.Server), foo)
	})

	return nil
}

func (p *GRPCProvider) Boot(s *velocity.Services) error { return p.server.Build() }
func (p *GRPCProvider) Shutdown(ctx context.Context) error { return p.server.Shutdown(ctx) }
`

func writeProvider(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join("internal", "providers")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	path := filepath.Join(dir, "grpc_provider.go")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	return path
}

func TestMakeGRPCService_NoMarkersDoesNotMutateProvider(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")
	providerPath := writeProvider(t, legacyProvider)

	original, err := os.ReadFile(providerPath)
	if err != nil {
		t.Fatalf("read provider: %v", err)
	}

	if err := MakeGRPCService("Bar", MakeGRPCServiceOptions{}); err != nil {
		t.Fatalf("MakeGRPCService: %v", err)
	}

	after, err := os.ReadFile(providerPath)
	if err != nil {
		t.Fatalf("re-read provider: %v", err)
	}
	if string(after) != string(original) {
		t.Errorf("provider was mutated despite missing markers.\n--- before ---\n%s\n--- after ---\n%s", original, after)
	}

	// Service + proto must still be scaffolded so the user only has to do
	// the provider wire manually.
	if _, err := os.Stat(filepath.Join("api", "proto", "bar", "v1", "bar.proto")); err != nil {
		t.Errorf("expected proto to be created even when provider has no markers: %v", err)
	}
	if _, err := os.Stat(filepath.Join("internal", "grpc", "services", "bar.go")); err != nil {
		t.Errorf("expected service impl to be created even when provider has no markers: %v", err)
	}
}

func TestWireGRPCProvider_SkipsWhenServiceAlreadyRegistered(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	// Provider has markers AND already registers FooService. The wire helper
	// must detect the duplicate by RegisterFooServiceServer signature and
	// leave the file untouched.
	provider := strings.Replace(legacyProvider,
		"foo := services.NewFooService()",
		"// vel:grpc:imports\n\t// vel:grpc:services\n\tfoo := services.NewFooService()",
		1)
	providerPath := writeProvider(t, provider)
	original, _ := os.ReadFile(providerPath)

	if err := wireGRPCProvider(grpcScaffold{
		ServiceName: "FooService", Leaf: "foo", Version: "v1", Alias: "foopb",
		GenPkgName: "foov1", ModulePath: "acme/app",
		ServicesImport: "acme/app/internal/grpc/services", VarName: "foo",
	}); err != nil {
		t.Fatalf("wireGRPCProvider: %v", err)
	}

	after, _ := os.ReadFile(providerPath)
	if string(after) != string(original) {
		t.Errorf("provider was mutated for an already-registered service.\n--- before ---\n%s\n--- after ---\n%s", original, after)
	}
	if c := strings.Count(string(after), "RegisterFooServiceServer("); c != 1 {
		t.Errorf("expected RegisterFooServiceServer to remain at 1 occurrence, got %d", c)
	}
}

func TestWireGRPCProvider_InjectsAtMarkersInOrder(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	// Provider with markers but no service registrations yet. wireGRPCProvider
	// should inject both the import and registration in the expected slots.
	provider := strings.Replace(legacyProvider,
		`foov1 "acme/app/api/gen/go/foo/v1"`,
		`// vel:grpc:imports`,
		1)
	provider = strings.Replace(provider,
		"foo := services.NewFooService()\n\tp.server.RegisterService(func(srv interface{}) {\n\t\tfoov1.RegisterFooServiceServer(srv.(*googleGrpc.Server), foo)\n\t})",
		"// vel:grpc:services",
		1)
	providerPath := writeProvider(t, provider)

	if err := wireGRPCProvider(grpcScaffold{
		ServiceName: "FooService", Leaf: "foo", Version: "v1", Alias: "foopb",
		GenPkgName: "foov1", ModulePath: "acme/app",
		ServicesImport: "acme/app/internal/grpc/services", VarName: "foo",
	}); err != nil {
		t.Fatalf("wireGRPCProvider: %v", err)
	}

	after, _ := os.ReadFile(providerPath)
	s := string(after)

	importIdx := strings.Index(s, grpcImportsMarker)
	importLineIdx := strings.Index(s, `foopb "acme/app/api/gen/go/foo/v1"`)
	servicesIdx := strings.Index(s, grpcServicesMarker)
	registerIdx := strings.Index(s, "RegisterFooServiceServer(")

	if importIdx < 0 || importLineIdx < 0 || servicesIdx < 0 || registerIdx < 0 {
		t.Fatalf("missing expected substrings in provider:\n%s", s)
	}
	if importIdx >= importLineIdx {
		t.Errorf("import was not inserted after // vel:grpc:imports marker")
	}
	if servicesIdx >= registerIdx {
		t.Errorf("registration was not inserted after // vel:grpc:services marker")
	}
}
