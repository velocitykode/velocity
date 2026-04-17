package rules

import (
	"mime/multipart"
	"testing"
)

// fakeFile is a lightweight FileHeader implementation so tests don't need a
// real multipart form.
type fakeFile struct {
	name string
	size int64
}

func (f fakeFile) Filename() string { return f.name }
func (f fakeFile) Size() int64      { return f.size }

func TestFileRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"nil ok", nil, false},
		{"FileHeader interface", fakeFile{name: "x.pdf", size: 10}, false},
		{"*multipart.FileHeader", &multipart.FileHeader{Filename: "x.pdf", Size: 10}, false},
		{"nil *multipart.FileHeader", (*multipart.FileHeader)(nil), true},
		{"string fails", "not-a-file", true},
		{"int fails", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := FileRule("upload", tt.value, nil, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestMimesRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		params  []string
		wantErr bool
	}{
		{"nil ok", nil, []string{"pdf"}, false},
		{"missing params", fakeFile{name: "x.pdf"}, nil, true},
		{"matches", fakeFile{name: "report.pdf"}, []string{"pdf"}, false},
		{"case insensitive", fakeFile{name: "REPORT.PDF"}, []string{"pdf"}, false},
		{"leading dot in param", fakeFile{name: "report.pdf"}, []string{".pdf"}, false},
		{"mismatch", fakeFile{name: "report.txt"}, []string{"pdf"}, true},
		{"non-file value", "report.pdf", []string{"pdf"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MimesRule("upload", tt.value, tt.params, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestImageRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"nil ok", nil, false},
		{"png passes", fakeFile{name: "photo.png"}, false},
		{"jpg passes", fakeFile{name: "photo.jpg"}, false},
		{"webp passes", fakeFile{name: "photo.webp"}, false},
		{"pdf fails", fakeFile{name: "doc.pdf"}, true},
		{"non-file fails", "photo.png", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ImageRule("photo", tt.value, nil, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}
