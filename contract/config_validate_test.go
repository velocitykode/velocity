package contract

import "testing"

// StorageConfig.Validate rejects disks no driver can build and a default
// that names no disk.
func TestStorageConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     StorageConfig
		wantErr bool
	}{
		{"zero config", StorageConfig{}, false},
		{"default with no disks", StorageConfig{Default: "local"}, false},
		{"valid", StorageConfig{Default: "s3", Disks: map[string]StorageDiskConfig{
			"local": {Driver: "local", Root: "/srv"},
			"s3":    {Driver: "s3", Bucket: "b"},
		}}, false},
		{"empty driver", StorageConfig{Disks: map[string]StorageDiskConfig{"x": {}}}, true},
		{"s3 without bucket", StorageConfig{Disks: map[string]StorageDiskConfig{"s3": {Driver: "s3"}}}, true},
		{"default missing", StorageConfig{Default: "s3", Disks: map[string]StorageDiskConfig{"local": {Driver: "local"}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
