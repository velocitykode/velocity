package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	storageTesting "github.com/velocitykode/velocity/storage/testing"
)

func TestNewUploadBuilder(t *testing.T) {
	builder := NewUploadBuilder()
	if builder == nil {
		t.Fatal("NewUploadBuilder() returned nil")
	}
	if builder.files == nil {
		t.Error("files slice should be initialized")
	}
	if builder.fields == nil {
		t.Error("fields map should be initialized")
	}
	if builder.headers == nil {
		t.Error("headers map should be initialized")
	}
}

func TestUploadBuilder_AddRawFile(t *testing.T) {
	tests := []struct {
		name      string
		fieldName string
		fileName  string
		content   []byte
		mimeType  string
	}{
		{
			name:      "adds text file",
			fieldName: "document",
			fileName:  "test.txt",
			content:   []byte("hello world"),
			mimeType:  "text/plain",
		},
		{
			name:      "adds binary file",
			fieldName: "image",
			fileName:  "photo.jpg",
			content:   []byte{0xFF, 0xD8, 0xFF, 0xE0},
			mimeType:  "image/jpeg",
		},
		{
			name:      "adds file with empty mime type",
			fieldName: "file",
			fileName:  "data.bin",
			content:   []byte("binary data"),
			mimeType:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewUploadBuilder()
			result := builder.AddRawFile(tt.fieldName, tt.fileName, tt.content, tt.mimeType)

			if result != builder {
				t.Error("AddRawFile should return builder for chaining")
			}
			if len(builder.files) != 1 {
				t.Errorf("expected 1 file, got %d", len(builder.files))
			}
			if builder.files[0].FieldName != tt.fieldName {
				t.Errorf("FieldName = %q, want %q", builder.files[0].FieldName, tt.fieldName)
			}
			if builder.files[0].FileName != tt.fileName {
				t.Errorf("FileName = %q, want %q", builder.files[0].FileName, tt.fileName)
			}
			if !bytes.Equal(builder.files[0].Content, tt.content) {
				t.Errorf("Content mismatch")
			}
			if builder.files[0].MimeType != tt.mimeType {
				t.Errorf("MimeType = %q, want %q", builder.files[0].MimeType, tt.mimeType)
			}
		})
	}
}

func TestUploadBuilder_AddField(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]string
	}{
		{
			name:   "adds single field",
			fields: map[string]string{"name": "John"},
		},
		{
			name:   "adds multiple fields",
			fields: map[string]string{"name": "John", "email": "john@example.com"},
		},
		{
			name:   "adds empty value",
			fields: map[string]string{"empty": ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewUploadBuilder()
			for k, v := range tt.fields {
				result := builder.AddField(k, v)
				if result != builder {
					t.Error("AddField should return builder for chaining")
				}
			}

			for k, v := range tt.fields {
				if builder.fields[k] != v {
					t.Errorf("field[%s] = %q, want %q", k, builder.fields[k], v)
				}
			}
		})
	}
}

func TestUploadBuilder_AddHeader(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
	}{
		{
			name:    "adds authorization header",
			headers: map[string]string{"Authorization": "Bearer token123"},
		},
		{
			name:    "adds multiple headers",
			headers: map[string]string{"X-Custom": "value", "X-Request-ID": "123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewUploadBuilder()
			for k, v := range tt.headers {
				result := builder.AddHeader(k, v)
				if result != builder {
					t.Error("AddHeader should return builder for chaining")
				}
			}

			for k, v := range tt.headers {
				if builder.headers[k] != v {
					t.Errorf("header[%s] = %q, want %q", k, builder.headers[k], v)
				}
			}
		})
	}
}

func TestUploadBuilder_Build(t *testing.T) {
	tests := []struct {
		name         string
		files        []UploadedFile
		fields       map[string]string
		headers      map[string]string
		method       string
		url          string
		wantErr      bool
		checkBody    bool
		bodyContains string
	}{
		{
			name: "builds request with file",
			files: []UploadedFile{
				{FieldName: "file", FileName: "test.txt", Content: []byte("hello"), MimeType: "text/plain"},
			},
			method:  "POST",
			url:     "/upload",
			wantErr: false,
		},
		{
			name: "builds request with file and fields",
			files: []UploadedFile{
				{FieldName: "avatar", FileName: "photo.jpg", Content: []byte("image"), MimeType: "image/jpeg"},
			},
			fields:  map[string]string{"name": "John"},
			method:  "POST",
			url:     "/profile",
			wantErr: false,
		},
		{
			name: "builds request with custom headers",
			files: []UploadedFile{
				{FieldName: "doc", FileName: "file.pdf", Content: []byte("pdf"), MimeType: "application/pdf"},
			},
			headers: map[string]string{"Authorization": "Bearer xyz"},
			method:  "PUT",
			url:     "/documents/1",
			wantErr: false,
		},
		{
			name:    "builds request with only fields",
			fields:  map[string]string{"name": "Test"},
			method:  "POST",
			url:     "/submit",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewUploadBuilder()
			for _, f := range tt.files {
				builder.AddRawFile(f.FieldName, f.FileName, f.Content, f.MimeType)
			}
			for k, v := range tt.fields {
				builder.AddField(k, v)
			}
			for k, v := range tt.headers {
				builder.AddHeader(k, v)
			}

			req, err := builder.Build(tt.method, tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("Build() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			if req.Method != tt.method {
				t.Errorf("Method = %q, want %q", req.Method, tt.method)
			}
			if req.URL.Path != tt.url {
				t.Errorf("URL = %q, want %q", req.URL.Path, tt.url)
			}
			contentType := req.Header.Get("Content-Type")
			if contentType == "" || !bytes.Contains([]byte(contentType), []byte("multipart/form-data")) {
				t.Errorf("Content-Type should be multipart/form-data, got %q", contentType)
			}

			for k, v := range tt.headers {
				if req.Header.Get(k) != v {
					t.Errorf("Header[%s] = %q, want %q", k, req.Header.Get(k), v)
				}
			}
		})
	}
}

func TestUploadBuilder_BuildTest(t *testing.T) {
	builder := NewUploadBuilder()
	builder.AddRawFile("file", "test.txt", []byte("content"), "text/plain")

	req := builder.BuildTest(t, "POST", "/upload")
	if req == nil {
		t.Fatal("BuildTest returned nil")
	}
	if req.Method != "POST" {
		t.Errorf("Method = %q, want POST", req.Method)
	}
	// The multipart Content-Type (with boundary) must survive the copy so
	// handlers calling ParseMultipartForm can parse the body.
	if ct := req.Header.Get("Content-Type"); !bytes.Contains([]byte(ct), []byte("multipart/form-data")) {
		t.Errorf("Content-Type = %q, want multipart/form-data with boundary", ct)
	}
}

func TestUploadBuilder_Chaining(t *testing.T) {
	req, err := NewUploadBuilder().
		AddRawFile("file1", "a.txt", []byte("a"), "text/plain").
		AddRawFile("file2", "b.txt", []byte("b"), "text/plain").
		AddField("name", "test").
		AddField("type", "document").
		AddHeader("Authorization", "Bearer token").
		Build("POST", "/upload")

	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if req == nil {
		t.Fatal("Build() returned nil request")
	}
}

func TestEscapeQuotes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no quotes", "hello", "hello"},
		{"single quote", `say "hello"`, `say \"hello\"`},
		{"multiple quotes", `"a" and "b"`, `\"a\" and \"b\"`},
		{"empty string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeQuotes(tt.input)
			if got != tt.want {
				t.Errorf("escapeQuotes(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTestUpload(t *testing.T) {
	fakeFile := storageTesting.Fake().Create("test.txt", 1) // 1 KB file

	req := TestUpload("document", fakeFile)
	if req == nil {
		t.Fatal("TestUpload returned nil")
	}
	if req.Method != "POST" {
		t.Errorf("Method = %q, want POST", req.Method)
	}
	if req.URL.Path != "/upload" {
		t.Errorf("URL = %q, want /upload", req.URL.Path)
	}
}

func TestTestMultipleUploads(t *testing.T) {
	files := map[string]*storageTesting.FakeFile{
		"file1": storageTesting.Fake().Create("a.txt", 1),
		"file2": storageTesting.Fake().Create("b.txt", 1),
	}

	req := TestMultipleUploads(files)
	if req == nil {
		t.Fatal("TestMultipleUploads returned nil")
	}
	if req.Method != "POST" {
		t.Errorf("Method = %q, want POST", req.Method)
	}
}

func TestParseMultipartForm(t *testing.T) {
	tests := []struct {
		name      string
		maxMemory int64
		wantErr   bool
	}{
		{
			name:      "default max memory",
			maxMemory: 0,
			wantErr:   false,
		},
		{
			name:      "custom max memory",
			maxMemory: 1 << 20,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewUploadBuilder()
			builder.AddRawFile("file", "test.txt", []byte("content"), "text/plain")
			req, _ := builder.Build("POST", "/upload")

			err := ParseMultipartForm(req, tt.maxMemory)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseMultipartForm() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetUploadedFile(t *testing.T) {
	tests := []struct {
		name      string
		fieldName string
		content   []byte
		wantErr   bool
	}{
		{
			name:      "gets existing file",
			fieldName: "document",
			content:   []byte("hello world"),
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewUploadBuilder()
			builder.AddRawFile(tt.fieldName, "test.txt", tt.content, "text/plain")
			req, _ := builder.Build("POST", "/upload")

			file, header, err := GetUploadedFile(req, tt.fieldName)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetUploadedFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			if file == nil {
				t.Fatal("file is nil")
			}
			defer file.Close()

			if header == nil {
				t.Fatal("header is nil")
			}
			if header.Filename != "test.txt" {
				t.Errorf("Filename = %q, want test.txt", header.Filename)
			}

			content, _ := io.ReadAll(file)
			if !bytes.Equal(content, tt.content) {
				t.Errorf("content = %q, want %q", content, tt.content)
			}
		})
	}
}

func TestGetUploadedFile_MissingFile(t *testing.T) {
	builder := NewUploadBuilder()
	builder.AddRawFile("file", "test.txt", []byte("content"), "text/plain")
	req, _ := builder.Build("POST", "/upload")

	_, _, err := GetUploadedFile(req, "nonexistent")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestGetUploadedFiles(t *testing.T) {
	builder := NewUploadBuilder()
	builder.AddRawFile("files", "a.txt", []byte("a"), "text/plain")
	builder.AddRawFile("files", "b.txt", []byte("b"), "text/plain")
	req, _ := builder.Build("POST", "/upload")

	files, err := GetUploadedFiles(req, "files")
	if err != nil {
		t.Fatalf("GetUploadedFiles() error = %v", err)
	}
	if len(files) != 2 {
		t.Errorf("got %d files, want 2", len(files))
	}
}

func TestGetUploadedFiles_MissingField(t *testing.T) {
	builder := NewUploadBuilder()
	builder.AddRawFile("file", "test.txt", []byte("content"), "text/plain")
	req, _ := builder.Build("POST", "/upload")

	_, err := GetUploadedFiles(req, "nonexistent")
	if err != http.ErrMissingFile {
		t.Errorf("expected ErrMissingFile, got %v", err)
	}
}

func TestSaveUploadedFile(t *testing.T) {
	builder := NewUploadBuilder()
	builder.AddRawFile("doc", "test.txt", []byte("hello"), "text/plain")
	req, _ := builder.Build("POST", "/upload")

	file, header, err := GetUploadedFile(req, "doc")
	if err != nil {
		t.Fatalf("GetUploadedFile() error = %v", err)
	}

	storage := storageTesting.StorageFake()
	path, err := SaveUploadedFile(file, header, "/uploads", storage)
	if err != nil {
		t.Fatalf("SaveUploadedFile() error = %v", err)
	}
	if path != "/uploads/test.txt" {
		t.Errorf("path = %q, want /uploads/test.txt", path)
	}
	if !storage.Exists(path) {
		t.Errorf("expected file stored at %q", path)
	}
	got, err := storage.Get(path)
	if err != nil {
		t.Fatalf("storage.Get(%q) error = %v", path, err)
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Errorf("stored content = %q, want %q", got, "hello")
	}
}

func TestSaveUploadedFile_InvalidFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantPath string
	}{
		{"dot filename", ".", "/uploads/file"},
		{"slash filename", "/", "/uploads/file"},
		{"normal filename", "document.pdf", "/uploads/document.pdf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewUploadBuilder()
			builder.AddRawFile("doc", tt.filename, []byte("content"), "text/plain")
			req, _ := builder.Build("POST", "/upload")

			file, header, _ := GetUploadedFile(req, "doc")
			// Override header filename for test
			header.Filename = tt.filename

			storage := storageTesting.StorageFake()
			path, err := SaveUploadedFile(file, header, "/uploads", storage)
			if err != nil {
				t.Fatalf("SaveUploadedFile() error = %v", err)
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
			if !storage.Exists(path) {
				t.Errorf("expected file stored at %q", path)
			}
		})
	}
}

func TestSaveUploadedFile_TraversalRejected(t *testing.T) {
	tests := []struct {
		name     string
		filename string
	}{
		{"parent traversal", "../secret.txt"},
		{"nested traversal", "../../secret.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewUploadBuilder()
			builder.AddRawFile("doc", "safe.txt", []byte("content"), "text/plain")
			req, _ := builder.Build("POST", "/upload")

			file, header, _ := GetUploadedFile(req, "doc")
			header.Filename = tt.filename

			storage := storageTesting.StorageFake()
			path, err := SaveUploadedFile(file, header, "/uploads", storage)
			if err == nil {
				t.Fatalf("SaveUploadedFile() error = nil, want traversal rejection (path = %q)", path)
			}
			if storage.Exists("/uploads/secret.txt") || storage.Exists("/secret.txt") {
				t.Errorf("traversal filename %q was stored", tt.filename)
			}
		})
	}
}

func TestTestUploadHandler(t *testing.T) {
	storage := storageTesting.StorageFake()
	handler := TestUploadHandler(storage)

	builder := NewUploadBuilder()
	builder.AddRawFile("file", "test.txt", []byte("hello world"), "text/plain")
	req, _ := builder.Build("POST", "/upload")

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	if !storage.Exists("uploads/test.txt") {
		t.Error("file should be stored in fake storage")
	}
}

func TestTestUploadHandler_InvalidRequest(t *testing.T) {
	storage := storageTesting.StorageFake()
	handler := TestUploadHandler(storage)

	req := httptest.NewRequest("POST", "/upload", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestUploadRoundTrip exercises the full path: build a multipart request via
// BuildTest, run it through TestUploadHandler, decode the JSON response body,
// and confirm the file landed in fake storage with the right content.
func TestUploadRoundTrip(t *testing.T) {
	storage := storageTesting.StorageFake()
	handler := TestUploadHandler(storage)

	content := []byte("round trip content")
	req := NewUploadBuilder().
		AddRawFile("file", "doc.txt", content, "text/plain").
		BuildTest(t, "POST", "/upload")
	if req == nil {
		t.Fatal("BuildTest returned nil")
	}

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp UploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response body: %v; body=%s", err, rec.Body.String())
	}
	if !resp.Success {
		t.Errorf("response success = false, want true")
	}
	if len(resp.Files) != 1 || resp.Files[0].Name != "doc.txt" {
		t.Fatalf("expected one file doc.txt in response, got %+v", resp.Files)
	}

	AssertFileUploaded(t, storage, "uploads/doc.txt")
	AssertFileContent(t, storage, "uploads/doc.txt", content)
}

type mockT struct {
	helperCalled bool
	errors       []string
}

func (m *mockT) Helper() {
	m.helperCalled = true
}

func (m *mockT) Errorf(format string, args ...interface{}) {
	m.errors = append(m.errors, format)
}

func TestAssertFileUploaded(t *testing.T) {
	storage := storageTesting.StorageFake()
	storage.Put("uploads/test.txt", []byte("content"))

	t.Run("file exists", func(t *testing.T) {
		mt := &mockT{}
		AssertFileUploaded(mt, storage, "uploads/test.txt")
		if len(mt.errors) > 0 {
			t.Error("should not report error when file exists")
		}
	})

	t.Run("file missing", func(t *testing.T) {
		mt := &mockT{}
		AssertFileUploaded(mt, storage, "uploads/missing.txt")
		if len(mt.errors) == 0 {
			t.Error("should report error when file missing")
		}
	})
}

func TestAssertFileContent(t *testing.T) {
	storage := storageTesting.StorageFake()
	storage.Put("test.txt", []byte("expected content"))

	t.Run("content matches", func(t *testing.T) {
		mt := &mockT{}
		AssertFileContent(mt, storage, "test.txt", []byte("expected content"))
		if len(mt.errors) > 0 {
			t.Error("should not report error when content matches")
		}
	})

	t.Run("content mismatch", func(t *testing.T) {
		mt := &mockT{}
		AssertFileContent(mt, storage, "test.txt", []byte("wrong content"))
		if len(mt.errors) == 0 {
			t.Error("should report error when content mismatches")
		}
	})

	t.Run("file not found", func(t *testing.T) {
		mt := &mockT{}
		AssertFileContent(mt, storage, "missing.txt", []byte("content"))
		if len(mt.errors) == 0 {
			t.Error("should report error when file not found")
		}
	})
}

func TestAddFile_WithFakeFile(t *testing.T) {
	fakeFile := storageTesting.Fake().Image("image.png", 100, 100) // 100x100 PNG image

	builder := NewUploadBuilder()
	result := builder.AddFile("avatar", fakeFile)

	if result != builder {
		t.Error("AddFile should return builder for chaining")
	}
	if len(builder.files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(builder.files))
	}

	file := builder.files[0]
	if file.FieldName != "avatar" {
		t.Errorf("FieldName = %q, want avatar", file.FieldName)
	}
	if file.FileName != "image.png" {
		t.Errorf("FileName = %q, want image.png", file.FileName)
	}
	if file.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", file.MimeType)
	}
}
