package velocity

import "testing"

// TestParseDirFlag covers the --dir extractor shared by the make:* commands:
// the inline (--dir=VALUE) and spaced (--dir VALUE) forms, absence, and the
// dangling case (a trailing --dir with no value) which must error rather than
// silently fall back to the default directory.
func TestParseDirFlag(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "absent", args: []string{"--uuid"}, want: ""},
		{name: "spaced form", args: []string{"--dir", "internal/x"}, want: "internal/x"},
		{name: "inline form", args: []string{"--dir=internal/x"}, want: "internal/x"},
		{name: "among other flags", args: []string{"--uuid", "--dir", "app/models", "-m"}, want: "app/models"},
		{name: "dangling errors", args: []string{"--dir"}, wantErr: true},
		{name: "dangling after flag errors", args: []string{"--uuid", "--dir"}, wantErr: true},
		{name: "inline empty value errors", args: []string{"--dir="}, wantErr: true},
		{name: "spaced empty value errors", args: []string{"--dir", ""}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDirFlag(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseDirFlag(%v) = %q, want error", tc.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDirFlag(%v) unexpected error: %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("parseDirFlag(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
