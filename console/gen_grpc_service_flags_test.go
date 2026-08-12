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

// TestGenGRPCService_NorthStar is the end-to-end acceptance for the multi-flag
// invocation: a vendor-prefixed package, an explicit impl dir, the default
// <leaf>pb alias, and no module wiring.
func TestGenGRPCService_NorthStar(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := GenGRPCService("TemplateControl", GenGRPCServiceOptions{
		Package:      "admin",
		ProtoPackage: "velship.admin.v1",
		Dir:          filepath.Join("internal", "shared", "grpc", "services"),
		NoModule:     true,
	}); err != nil {
		t.Fatalf("GenGRPCService: %v", err)
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

	// --no-module must leave no module behind, and nothing should land in
	// the default impl dir.
	mustNotExist(t, filepath.Join("internal", "modules", "grpc_module.go"))
	mustNotExist(t, filepath.Join("internal", "grpc", "services", "template_control.go"))
}

// TestGenGRPCService_TwoServicesSharePackage proves several services can live
// in one shared package + dir without clobbering one another.
func TestGenGRPCService_TwoServicesSharePackage(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	dir := filepath.Join("internal", "shared", "grpc", "services")
	common := GenGRPCServiceOptions{
		Package:      "admin",
		ProtoPackage: "velship.admin.v1",
		Dir:          dir,
		NoModule:     true,
	}

	if err := GenGRPCService("TemplateControl", common); err != nil {
		t.Fatalf("first service: %v", err)
	}
	if err := GenGRPCService("StackControl", common); err != nil {
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

// TestGenGRPCService_SamePackageModuleDedupesImport verifies that wiring two
// services that share a package emits the proto import only once (a duplicate
// import would fail to compile) while still registering both servers.
func TestGenGRPCService_SamePackageModuleDedupesImport(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	common := GenGRPCServiceOptions{Package: "admin", ProtoPackage: "velship.admin.v1"}
	if err := GenGRPCService("TemplateControl", common); err != nil {
		t.Fatalf("first service: %v", err)
	}
	if err := GenGRPCService("StackControl", common); err != nil {
		t.Fatalf("second service: %v", err)
	}

	data, err := os.ReadFile(filepath.Join("internal", "modules", "grpc_module.go"))
	if err != nil {
		t.Fatalf("read module: %v", err)
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
			t.Errorf("module missing %q", n)
		}
	}
}

// TestGenGRPCService_SamePackageReusesAliasOnConflict verifies that wiring a
// second service into a package that is already imported reuses the existing
// alias (a fresh --alias for an already-imported path would reference an
// undefined name).
func TestGenGRPCService_SamePackageReusesAliasOnConflict(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	first := GenGRPCServiceOptions{Package: "admin", ProtoPackage: "velship.admin.v1"}
	if err := GenGRPCService("TemplateControl", first); err != nil {
		t.Fatalf("first service: %v", err)
	}
	second := first
	second.Alias = "adminapi" // conflicts with the alias already bound to the package
	if err := GenGRPCService("StackControl", second); err != nil {
		t.Fatalf("second service: %v", err)
	}

	path := filepath.Join("internal", "modules", "grpc_module.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read module: %v", err)
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

// TestGenGRPCService_MismatchedDirWithModuleErrors guards the unsupported
// combination of module auto-wiring and a second service whose impl lives in
// a different directory: the registration would bind to the wrong package, so
// the command must refuse with an actionable error instead.
func TestGenGRPCService_MismatchedDirWithModuleErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := GenGRPCService("Foo", GenGRPCServiceOptions{}); err != nil {
		t.Fatalf("first service: %v", err)
	}
	otherDir := filepath.Join("internal", "other", "services")
	err := GenGRPCService("Bar", GenGRPCServiceOptions{Dir: otherDir})
	if err == nil {
		t.Fatal("expected an error wiring a service from a different dir into an existing module")
	}
	if !strings.Contains(err.Error(), "--no-module") {
		t.Errorf("error should suggest --no-module, got: %v", err)
	}

	// The preflight must fail before any of Bar's files are written, so the
	// suggested "re-run with --no-module" does not trip "already exists".
	mustNotExist(t, filepath.Join("api", "proto", "bar", "v1", "bar.proto"))
	mustNotExist(t, filepath.Join(otherDir, "bar.go"))

	// The recovery path must actually work: re-running with --no-module
	// succeeds and writes both files.
	if err := GenGRPCService("Bar", GenGRPCServiceOptions{Dir: otherDir, NoModule: true}); err != nil {
		t.Fatalf("re-run with --no-module should succeed, got: %v", err)
	}
	mustContain(t, filepath.Join("api", "proto", "bar", "v1", "bar.proto"), "service BarService {")
	mustContain(t, filepath.Join(otherDir, "bar.go"), "type BarService struct {")
}

// TestGenGRPCService_ProtoPackageDerivesLeaf checks that --proto-package alone
// (no --package) drives the directory leaf, version, and alias.
func TestGenGRPCService_ProtoPackageDerivesLeaf(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := GenGRPCService("Invoices", GenGRPCServiceOptions{
		ProtoPackage: "acme.billing.v1",
		NoModule:     true,
	}); err != nil {
		t.Fatalf("GenGRPCService: %v", err)
	}

	proto := filepath.Join("api", "proto", "billing", "v1", "invoices.proto")
	mustContain(t, proto,
		"package acme.billing.v1;",
		`option go_package = "acme/app/api/gen/go/billing/v1;billingv1";`,
	)
	mustContain(t, filepath.Join("internal", "grpc", "services", "invoices.go"),
		`billingpb "acme/app/api/gen/go/billing/v1"`)
}

// TestGenGRPCService_VersionFromProtoPackage threads a non-v1 version through
// the directory layout, go_package, and alias.
func TestGenGRPCService_VersionFromProtoPackage(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := GenGRPCService("Ledger", GenGRPCServiceOptions{
		ProtoPackage: "acme.billing.v2",
		NoModule:     true,
	}); err != nil {
		t.Fatalf("GenGRPCService: %v", err)
	}

	proto := filepath.Join("api", "proto", "billing", "v2", "ledger.proto")
	mustContain(t, proto,
		"package acme.billing.v2;",
		`option go_package = "acme/app/api/gen/go/billing/v2;billingv2";`,
	)
	mustContain(t, filepath.Join("internal", "grpc", "services", "ledger.go"),
		`billingpb "acme/app/api/gen/go/billing/v2"`)
}

// TestGenGRPCService_NameAndAliasOverrides covers --proto-name, --impl-name,
// and an explicit --alias.
func TestGenGRPCService_NameAndAliasOverrides(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := GenGRPCService("TemplateControl", GenGRPCServiceOptions{
		Package:   "admin",
		Alias:     "adminapi",
		ProtoName: "tmpl_ctrl",
		ImplName:  "tmpl",
		NoModule:  true,
	}); err != nil {
		t.Fatalf("GenGRPCService: %v", err)
	}

	mustContain(t, filepath.Join("api", "proto", "admin", "v1", "tmpl_ctrl.proto"),
		"service TemplateControlService {")
	mustContain(t, filepath.Join("internal", "grpc", "services", "tmpl.go"),
		`adminapi "acme/app/api/gen/go/admin/v1"`,
		"adminapi.UnimplementedTemplateControlServiceServer",
	)
}

// TestGenGRPCService_NoModuleSkipsWiring asserts the default-layout path also
// honors --no-module.
func TestGenGRPCService_NoModuleSkipsWiring(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := GenGRPCService("Foo", GenGRPCServiceOptions{NoModule: true}); err != nil {
		t.Fatalf("GenGRPCService: %v", err)
	}
	mustContain(t, filepath.Join("internal", "grpc", "services", "foo.go"), "type FooService struct {")
	mustNotExist(t, filepath.Join("internal", "modules", "grpc_module.go"))
}

func TestGenGRPCService_RejectsBadFlags(t *testing.T) {
	cases := []struct {
		name string
		opts GenGRPCServiceOptions
	}{
		{"uppercase proto-package", GenGRPCServiceOptions{ProtoPackage: "Velship.Admin.v1"}},
		{"proto-package with newline", GenGRPCServiceOptions{ProtoPackage: "admin\nv1"}},
		{"bad alias", GenGRPCServiceOptions{Alias: "1bad"}},
		{"package with slash", GenGRPCServiceOptions{Package: "foo/bar"}},
		{"dir traversal", GenGRPCServiceOptions{Dir: filepath.Join("..", "escape")}},
		{"proto-name with slash", GenGRPCServiceOptions{ProtoName: "a/b"}},
		{"impl-name traversal", GenGRPCServiceOptions{ImplName: ".."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeFakeGoMod(t, "acme/app")
			if err := GenGRPCService("Foo", tc.opts); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// TestParseGenGRPCServiceArgs is exercised in the velocity package; here we
// pin the console-level resolution invariants the parser depends on: the zero
// options reproduce the legacy single-arg layout.
func TestGenGRPCService_ZeroOptionsLegacyLayout(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFakeGoMod(t, "acme/app")

	if err := GenGRPCService("Foo", GenGRPCServiceOptions{}); err != nil {
		t.Fatalf("GenGRPCService: %v", err)
	}
	mustContain(t, filepath.Join("api", "proto", "foo", "v1", "foo.proto"),
		"package foo.v1;",
		`option go_package = "acme/app/api/gen/go/foo/v1;foov1";`,
	)
	mustContain(t, filepath.Join("internal", "grpc", "services", "foo.go"),
		`foopb "acme/app/api/gen/go/foo/v1"`)
}
