package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustContain reads path and fails unless every needle is present.
func mustContain(t *testing.T, path string, needles ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, n := range needles {
		if !strings.Contains(string(data), n) {
			t.Errorf("%s missing %q\n--- got ---\n%s", path, n, data)
		}
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to not exist, stat err=%v", path, err)
	}
}

// TestMakeGRPCService_NorthStar is the end-to-end acceptance for the multi-flag
// invocation: a vendor-prefixed package, an explicit impl dir, the default
// <leaf>pb alias, and no provider wiring.
func TestMakeGRPCService_NorthStar(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService("TemplateControl", MakeGRPCServiceOptions{
		Package:      "admin",
		ProtoPackage: "velship.admin.v1",
		Dir:          filepath.Join("internal", "shared", "grpc", "services"),
		NoProvider:   true,
	}); err != nil {
		t.Fatalf("MakeGRPCService: %v", err)
	}

	proto := filepath.Join("api", "proto", "admin", "v1", "templatecontrol.proto")
	mustContain(t, proto,
		"package velship.admin.v1;",
		`option go_package = "acme/app/api/gen/go/admin/v1;adminv1";`,
		"service TemplateControlService {",
	)

	impl := filepath.Join("internal", "shared", "grpc", "services", "template_control.go")
	mustContain(t, impl,
		"package services",
		`adminpb "acme/app/api/gen/go/admin/v1"`,
		"type TemplateControlService struct {",
		"adminpb.UnimplementedTemplateControlServiceServer",
		"func NewTemplateControlService() *TemplateControlService",
	)

	// --no-provider must leave no provider behind, and nothing should land in
	// the default impl dir.
	mustNotExist(t, filepath.Join("internal", "providers", "grpc_provider.go"))
	mustNotExist(t, filepath.Join("internal", "grpc", "services", "template_control.go"))
}

// TestMakeGRPCService_TwoServicesSharePackage proves several services can live
// in one shared package + dir without clobbering one another.
func TestMakeGRPCService_TwoServicesSharePackage(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	dir := filepath.Join("internal", "shared", "grpc", "services")
	common := MakeGRPCServiceOptions{
		Package:      "admin",
		ProtoPackage: "velship.admin.v1",
		Dir:          dir,
		NoProvider:   true,
	}

	if err := MakeGRPCService("TemplateControl", common); err != nil {
		t.Fatalf("first service: %v", err)
	}
	if err := MakeGRPCService("StackControl", common); err != nil {
		t.Fatalf("second service: %v", err)
	}

	// Both protos coexist in the same package dir.
	mustContain(t, filepath.Join("api", "proto", "admin", "v1", "templatecontrol.proto"),
		"service TemplateControlService {", "package velship.admin.v1;")
	mustContain(t, filepath.Join("api", "proto", "admin", "v1", "stackcontrol.proto"),
		"service StackControlService {", "package velship.admin.v1;")

	// Both impls coexist; the first is untouched by the second.
	mustContain(t, filepath.Join(dir, "template_control.go"), "type TemplateControlService struct {")
	mustContain(t, filepath.Join(dir, "stack_control.go"), "type StackControlService struct {")
}

// TestMakeGRPCService_SamePackageProviderDedupesImport verifies that wiring two
// services that share a package emits the proto import only once (a duplicate
// import would fail to compile) while still registering both servers.
func TestMakeGRPCService_SamePackageProviderDedupesImport(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	common := MakeGRPCServiceOptions{Package: "admin", ProtoPackage: "velship.admin.v1"}
	if err := MakeGRPCService("TemplateControl", common); err != nil {
		t.Fatalf("first service: %v", err)
	}
	if err := MakeGRPCService("StackControl", common); err != nil {
		t.Fatalf("second service: %v", err)
	}

	data, err := os.ReadFile(filepath.Join("internal", "providers", "grpc_provider.go"))
	if err != nil {
		t.Fatalf("read provider: %v", err)
	}
	s := string(data)
	if c := strings.Count(s, `"acme/app/api/gen/go/admin/v1"`); c != 1 {
		t.Errorf("expected the shared proto package imported exactly once, got %d\n%s", c, s)
	}
	for _, n := range []string{
		"RegisterTemplateControlServiceServer(",
		"RegisterStackControlServiceServer(",
	} {
		if !strings.Contains(s, n) {
			t.Errorf("provider missing %q", n)
		}
	}
}

// TestMakeGRPCService_SamePackageReusesAliasOnConflict verifies that wiring a
// second service into a package that is already imported reuses the existing
// alias (a fresh --alias for an already-imported path would reference an
// undefined name).
func TestMakeGRPCService_SamePackageReusesAliasOnConflict(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	first := MakeGRPCServiceOptions{Package: "admin", ProtoPackage: "velship.admin.v1"}
	if err := MakeGRPCService("TemplateControl", first); err != nil {
		t.Fatalf("first service: %v", err)
	}
	second := first
	second.Alias = "adminapi" // conflicts with the alias already bound to the package
	if err := MakeGRPCService("StackControl", second); err != nil {
		t.Fatalf("second service: %v", err)
	}

	path := filepath.Join("internal", "providers", "grpc_provider.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read provider: %v", err)
	}
	s := string(data)
	if c := strings.Count(s, "adminapi"); c != 0 {
		t.Errorf("conflicting alias adminapi should not appear, found %d times\n%s", c, s)
	}
	// Both registrations must use the originally-bound alias.
	mustContain(t, path,
		"adminpb.RegisterTemplateControlServiceServer(",
		"adminpb.RegisterStackControlServiceServer(",
	)
}

// TestMakeGRPCService_MismatchedDirWithProviderErrors guards the unsupported
// combination of provider auto-wiring and a second service whose impl lives in
// a different directory: the registration would bind to the wrong package, so
// the command must refuse with an actionable error instead.
func TestMakeGRPCService_MismatchedDirWithProviderErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService("Foo", MakeGRPCServiceOptions{}); err != nil {
		t.Fatalf("first service: %v", err)
	}
	otherDir := filepath.Join("internal", "other", "services")
	err := MakeGRPCService("Bar", MakeGRPCServiceOptions{Dir: otherDir})
	if err == nil {
		t.Fatal("expected an error wiring a service from a different dir into an existing provider")
	}
	if !strings.Contains(err.Error(), "--no-provider") {
		t.Errorf("error should suggest --no-provider, got: %v", err)
	}

	// The preflight must fail before any of Bar's files are written, so the
	// suggested "re-run with --no-provider" does not trip "already exists".
	mustNotExist(t, filepath.Join("api", "proto", "bar", "v1", "bar.proto"))
	mustNotExist(t, filepath.Join(otherDir, "bar.go"))

	// The recovery path must actually work: re-running with --no-provider
	// succeeds and writes both files.
	if err := MakeGRPCService("Bar", MakeGRPCServiceOptions{Dir: otherDir, NoProvider: true}); err != nil {
		t.Fatalf("re-run with --no-provider should succeed, got: %v", err)
	}
	mustContain(t, filepath.Join("api", "proto", "bar", "v1", "bar.proto"), "service BarService {")
	mustContain(t, filepath.Join(otherDir, "bar.go"), "type BarService struct {")
}

// TestMakeGRPCService_ProtoPackageDerivesLeaf checks that --proto-package alone
// (no --package) drives the directory leaf, version, and alias.
func TestMakeGRPCService_ProtoPackageDerivesLeaf(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService("Invoices", MakeGRPCServiceOptions{
		ProtoPackage: "acme.billing.v1",
		NoProvider:   true,
	}); err != nil {
		t.Fatalf("MakeGRPCService: %v", err)
	}

	proto := filepath.Join("api", "proto", "billing", "v1", "invoices.proto")
	mustContain(t, proto,
		"package acme.billing.v1;",
		`option go_package = "acme/app/api/gen/go/billing/v1;billingv1";`,
	)
	mustContain(t, filepath.Join("internal", "grpc", "services", "invoices.go"),
		`billingpb "acme/app/api/gen/go/billing/v1"`)
}

// TestMakeGRPCService_VersionFromProtoPackage threads a non-v1 version through
// the directory layout, go_package, and alias.
func TestMakeGRPCService_VersionFromProtoPackage(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService("Ledger", MakeGRPCServiceOptions{
		ProtoPackage: "acme.billing.v2",
		NoProvider:   true,
	}); err != nil {
		t.Fatalf("MakeGRPCService: %v", err)
	}

	proto := filepath.Join("api", "proto", "billing", "v2", "ledger.proto")
	mustContain(t, proto,
		"package acme.billing.v2;",
		`option go_package = "acme/app/api/gen/go/billing/v2;billingv2";`,
	)
	mustContain(t, filepath.Join("internal", "grpc", "services", "ledger.go"),
		`billingpb "acme/app/api/gen/go/billing/v2"`)
}

// TestMakeGRPCService_NameAndAliasOverrides covers --proto-name, --impl-name,
// and an explicit --alias.
func TestMakeGRPCService_NameAndAliasOverrides(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService("TemplateControl", MakeGRPCServiceOptions{
		Package:    "admin",
		Alias:      "adminapi",
		ProtoName:  "tmpl_ctrl",
		ImplName:   "tmpl",
		NoProvider: true,
	}); err != nil {
		t.Fatalf("MakeGRPCService: %v", err)
	}

	mustContain(t, filepath.Join("api", "proto", "admin", "v1", "tmpl_ctrl.proto"),
		"service TemplateControlService {")
	mustContain(t, filepath.Join("internal", "grpc", "services", "tmpl.go"),
		`adminapi "acme/app/api/gen/go/admin/v1"`,
		"adminapi.UnimplementedTemplateControlServiceServer",
	)
}

// TestMakeGRPCService_NoProviderSkipsWiring asserts the default-layout path also
// honors --no-provider.
func TestMakeGRPCService_NoProviderSkipsWiring(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService("Foo", MakeGRPCServiceOptions{NoProvider: true}); err != nil {
		t.Fatalf("MakeGRPCService: %v", err)
	}
	mustContain(t, filepath.Join("internal", "grpc", "services", "foo.go"), "type FooService struct {")
	mustNotExist(t, filepath.Join("internal", "providers", "grpc_provider.go"))
}

func TestMakeGRPCService_RejectsBadFlags(t *testing.T) {
	cases := []struct {
		name string
		opts MakeGRPCServiceOptions
	}{
		{"uppercase proto-package", MakeGRPCServiceOptions{ProtoPackage: "Velship.Admin.v1"}},
		{"proto-package with newline", MakeGRPCServiceOptions{ProtoPackage: "admin\nv1"}},
		{"bad alias", MakeGRPCServiceOptions{Alias: "1bad"}},
		{"package with slash", MakeGRPCServiceOptions{Package: "foo/bar"}},
		{"dir traversal", MakeGRPCServiceOptions{Dir: filepath.Join("..", "escape")}},
		{"proto-name with slash", MakeGRPCServiceOptions{ProtoName: "a/b"}},
		{"impl-name traversal", MakeGRPCServiceOptions{ImplName: ".."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeFakeGoMod(t, "acme/app")
			if err := MakeGRPCService("Foo", tc.opts); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// TestParseMakeGRPCServiceArgs is exercised in the velocity package; here we
// pin the console-level resolution invariants the parser depends on: the zero
// options reproduce the legacy single-arg layout.
func TestMakeGRPCService_ZeroOptionsLegacyLayout(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := MakeGRPCService("Foo", MakeGRPCServiceOptions{}); err != nil {
		t.Fatalf("MakeGRPCService: %v", err)
	}
	mustContain(t, filepath.Join("api", "proto", "foo", "v1", "foo.proto"),
		"package foo.v1;",
		`option go_package = "acme/app/api/gen/go/foo/v1;foov1";`,
	)
	mustContain(t, filepath.Join("internal", "grpc", "services", "foo.go"),
		`foopb "acme/app/api/gen/go/foo/v1"`)
}
