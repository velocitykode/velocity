package velocity

import (
	"testing"

	"github.com/velocitykode/velocity/console"
)

func TestParseGenGRPCServiceArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantName string
		want     console.GenGRPCServiceOptions
		wantErr  bool
	}{
		{
			name:     "bare name",
			args:     []string{"Foo"},
			wantName: "Foo",
		},
		{
			name:     "north star, name first",
			args:     []string{"TemplateControl", "--package", "admin", "--proto-package", "velship.admin.v1", "--dir", "internal/shared/grpc/services", "--no-module"},
			wantName: "TemplateControl",
			want: console.GenGRPCServiceOptions{
				Package:      "admin",
				ProtoPackage: "velship.admin.v1",
				Dir:          "internal/shared/grpc/services",
				NoModule:     true,
			},
		},
		{
			name:     "flags before name",
			args:     []string{"--package", "admin", "TemplateControl"},
			wantName: "TemplateControl",
			want:     console.GenGRPCServiceOptions{Package: "admin"},
		},
		{
			name:     "equals form",
			args:     []string{"Foo", "--alias=foopb", "--impl-name=foo_svc"},
			wantName: "Foo",
			want:     console.GenGRPCServiceOptions{Alias: "foopb", ImplName: "foo_svc"},
		},
		{
			name:    "unknown flag",
			args:    []string{"Foo", "--bogus", "x"},
			wantErr: true,
		},
		{
			name:    "missing flag value",
			args:    []string{"Foo", "--package"},
			wantErr: true,
		},
		{
			name:    "two positionals",
			args:    []string{"Foo", "Bar"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, opts, err := parseGenGRPCServiceArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got name=%q opts=%+v", name, opts)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if opts != tc.want {
				t.Errorf("opts = %+v, want %+v", opts, tc.want)
			}
		})
	}
}
