package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenGRPCService_SuffixOnlyNameRejected covers the gRPC scaffolder's own
// version of the suffix-only-name hole the other generators close via
// requireNormalizedName. "Service" survives scaffold.ValidateName and then
// normalises to an empty base.
//
// The derived values guard themselves: the package leaf, proto file name, and
// impl file name are all seeded from the empty base and each is validated. But
// --package, --proto-name, and --impl-name replace exactly those three
// derivations, so setting all three bypasses every guard and the empty base
// reaches the module writer as an empty VarName, emitting the uncompilable
// ` := services.NewService()`.
func TestGenGRPCService_SuffixOnlyNameRejected(t *testing.T) {
	overrides := GenGRPCServiceOptions{
		Package:   "admin",
		ProtoName: "thing",
		ImplName:  "thing",
	}

	tests := []struct {
		name string
		arg  string
		opts GenGRPCServiceOptions
	}{
		{"pascal bare", "Service", GenGRPCServiceOptions{}},
		{"lower bare", "service", GenGRPCServiceOptions{}},
		{"pascal with overrides", "Service", overrides},
		{"lower with overrides", "service", overrides},
		{"pascal with overrides no module", "Service", GenGRPCServiceOptions{
			Package: "admin", ProtoName: "thing", ImplName: "thing", NoModule: true,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			writeFakeGoMod(t, "acme/app")

			if err := GenGRPCService(tt.arg, tt.opts); err == nil {
				t.Fatalf("GenGRPCService(%q, %+v) = nil, want error", tt.arg, tt.opts)
			}

			// The rejection must land before anything is written: no proto,
			// no impl, no module, and not even the buf configs.
			for _, unwanted := range []string{
				filepath.Join("api", "proto"),
				filepath.Join("internal", "grpc"),
				filepath.Join("internal", "modules"),
			} {
				if _, err := os.Stat(unwanted); !os.IsNotExist(err) {
					t.Errorf("GenGRPCService(%q) created %s, want nothing written (stat err=%v)", tt.arg, unwanted, err)
				}
			}
		})
	}
}

// TestGenGRPCService_NormalNameUnaffected pins that the empty-base rejection
// does not touch ordinary names, including one that merely ends in "Service".
func TestGenGRPCService_NormalNameUnaffected(t *testing.T) {
	tests := []struct {
		name      string
		arg       string
		wantProto string
		wantImpl  string
		wantVar   string
		wantType  string
	}{
		{"bare", "TemplateControl", "templatecontrol", "template_control", "templateControl", "TemplateControlService"},
		{"redundant suffix", "TemplateControlService", "templatecontrol", "template_control", "templateControl", "TemplateControlService"},
		{"snake input", "template_control", "templatecontrol", "template_control", "templateControl", "TemplateControlService"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeFakeGoMod(t, "acme/app")

			if err := GenGRPCService(tt.arg, GenGRPCServiceOptions{}); err != nil {
				t.Fatalf("GenGRPCService(%q): %v", tt.arg, err)
			}

			mustContain(t,
				filepath.Join("api", "proto", "templatecontrol", "v1", tt.wantProto+".proto"),
				"service "+tt.wantType+" {",
			)
			mustContain(t,
				filepath.Join("internal", "grpc", "services", tt.wantImpl+".go"),
				"type "+tt.wantType+" struct {",
			)
			mustContain(t,
				filepath.Join("internal", "modules", "grpc_module.go"),
				tt.wantVar+" := services.New"+tt.wantType+"()",
			)
		})
	}
}

// TestWriteFormattedGo_RefusesUnparseableSource pins the second half of the
// same bug: writeFormattedGo used to swallow the format.Source error and write
// the unparseable bytes anyway, so wireGRPCModule reported success while
// leaving a module that does not compile.
func TestWriteFormattedGo_RefusesUnparseableSource(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantErr bool
	}{
		{"valid go", "package modules\n\nfunc f() {}\n", false},
		{"empty var name", "package modules\n\nfunc f() {\n\t := services.NewService()\n}\n", true},
		{"truncated", "package modules\n\nfunc f() {\n", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mod.go")

			err := writeFormattedGo(path, []byte(tt.src))
			if (err != nil) != tt.wantErr {
				t.Fatalf("writeFormattedGo() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Errorf("writeFormattedGo wrote %s on a parse failure (stat err=%v)", path, statErr)
				}
				if !strings.Contains(err.Error(), path) {
					t.Errorf("error %q does not name the target path %q", err, path)
				}
				return
			}
			if _, statErr := os.Stat(path); statErr != nil {
				t.Errorf("writeFormattedGo did not write %s: %v", path, statErr)
			}
		})
	}
}
