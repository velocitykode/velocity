package http

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"

	storageTesting "github.com/velocitykode/velocity/storage/testing"
)

// UploadedFile represents a file to be uploaded in tests
type UploadedFile struct {
	FieldName string
	FileName  string
	Content   []byte
	MimeType  string
}

// TestUploadBuilder helps build test upload requests
type TestUploadBuilder struct {
	files   []UploadedFile
	fields  map[string]string
	headers map[string]string
}

// NewUploadBuilder creates a new upload builder
func NewUploadBuilder() *TestUploadBuilder {
	return &TestUploadBuilder{
		files:   make([]UploadedFile, 0),
		fields:  make(map[string]string),
		headers: make(map[string]string),
	}
}

// AddFile adds a file to the upload
func (b *TestUploadBuilder) AddFile(fieldName string, file *storageTesting.FakeFile) *TestUploadBuilder {
	b.files = append(b.files, UploadedFile{
		FieldName: fieldName,
		FileName:  file.Name(),
		Content:   file.Content(),
		MimeType:  file.MimeType(),
	})
	return b
}

// AddRawFile adds a raw file to the upload
func (b *TestUploadBuilder) AddRawFile(fieldName, fileName string, content []byte, mimeType string) *TestUploadBuilder {
	b.files = append(b.files, UploadedFile{
		FieldName: fieldName,
		FileName:  fileName,
		Content:   content,
		MimeType:  mimeType,
	})
	return b
}

// AddField adds a form field to the upload
func (b *TestUploadBuilder) AddField(name, value string) *TestUploadBuilder {
	b.fields[name] = value
	return b
}

// AddHeader adds a header to the request
func (b *TestUploadBuilder) AddHeader(name, value string) *TestUploadBuilder {
	b.headers[name] = value
	return b
}

// Build creates the multipart request
func (b *TestUploadBuilder) Build(method, url string) (*http.Request, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add files
	for _, file := range b.files {
		part, err := createFormFile(writer, file.FieldName, file.FileName, file.MimeType)
		if err != nil {
			return nil, fmt.Errorf("failed to create form file: %w", err)
		}
		if _, err := part.Write(file.Content); err != nil {
			return nil, fmt.Errorf("failed to write file content: %w", err)
		}
	}

	// Add fields
	for name, value := range b.fields {
		if err := writer.WriteField(name, value); err != nil {
			return nil, fmt.Errorf("failed to write field %s: %w", name, err)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to close multipart writer: %w", err)
	}

	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Add custom headers
	for name, value := range b.headers {
		req.Header.Set(name, value)
	}

	return req, nil
}

// BuildTest creates a test request for httptest
func (b *TestUploadBuilder) BuildTest(method, url string) *http.Request {
	req, _ := b.Build(method, url)
	return httptest.NewRequest(method, url, req.Body)
}

// createFormFile creates a form file with custom MIME type
func createFormFile(w *multipart.Writer, fieldname, filename, mimeType string) (io.Writer, error) {
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
			escapeQuotes(fieldname), escapeQuotes(filename)))

	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	h.Set("Content-Type", mimeType)

	return w.CreatePart(h)
}

// escapeQuotes escapes quotes in multipart form data
func escapeQuotes(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

// TestUpload creates a simple file upload request for testing
func TestUpload(fieldName string, file *storageTesting.FakeFile) *http.Request {
	builder := NewUploadBuilder()
	builder.AddFile(fieldName, file)
	req, _ := builder.Build("POST", "/upload")
	return req
}

// TestMultipleUploads creates a request with multiple file uploads
func TestMultipleUploads(files map[string]*storageTesting.FakeFile) *http.Request {
	builder := NewUploadBuilder()
	for fieldName, file := range files {
		builder.AddFile(fieldName, file)
	}
	req, _ := builder.Build("POST", "/upload")
	return req
}

// ParseMultipartForm is a helper to parse multipart form in tests
func ParseMultipartForm(r *http.Request, maxMemory int64) error {
	if maxMemory == 0 {
		maxMemory = 10 << 20 // 10 MB default
	}
	return r.ParseMultipartForm(maxMemory)
}

// GetUploadedFile retrieves an uploaded file from the request
func GetUploadedFile(r *http.Request, fieldName string) (multipart.File, *multipart.FileHeader, error) {
	if r.MultipartForm == nil {
		if err := ParseMultipartForm(r, 0); err != nil {
			return nil, nil, err
		}
	}
	return r.FormFile(fieldName)
}

// GetUploadedFiles retrieves multiple uploaded files from the request
func GetUploadedFiles(r *http.Request, fieldName string) ([]*multipart.FileHeader, error) {
	if r.MultipartForm == nil {
		if err := ParseMultipartForm(r, 0); err != nil {
			return nil, err
		}
	}

	if r.MultipartForm == nil || r.MultipartForm.File == nil {
		return nil, http.ErrMissingFile
	}

	files, ok := r.MultipartForm.File[fieldName]
	if !ok {
		return nil, http.ErrMissingFile
	}

	return files, nil
}

// SaveUploadedFile saves an uploaded file to storage (helper for handlers)
func SaveUploadedFile(file multipart.File, header *multipart.FileHeader, basePath string) (string, error) {
	defer file.Close()

	// Read file content
	buf := bytes.NewBuffer(nil)
	if _, err := io.Copy(buf, file); err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	// Generate safe filename
	filename := filepath.Base(header.Filename)
	if filename == "." || filename == "/" {
		filename = "file"
	}

	// Create full path
	fullPath := filepath.Join(basePath, filename)

	// In a real implementation, you would save to storage here
	// For testing, we just return the path

	return fullPath, nil
}

// UploadResponse represents a typical upload response
type UploadResponse struct {
	Success bool                   `json:"success"`
	Message string                 `json:"message,omitempty"`
	Files   []UploadedFileResponse `json:"files,omitempty"`
	Errors  map[string]string      `json:"errors,omitempty"`
}

// UploadedFileResponse represents a single uploaded file in the response
type UploadedFileResponse struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	URL          string `json:"url"`
	Size         int64  `json:"size"`
	MimeType     string `json:"mime_type"`
	TemporaryURL string `json:"temporary_url,omitempty"`
}

// TestUploadHandler creates a test handler that processes file uploads
func TestUploadHandler(storage storageTesting.FakeStorage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "Failed to parse form", http.StatusBadRequest)
			return
		}

		response := UploadResponse{
			Success: true,
			Files:   make([]UploadedFileResponse, 0),
		}

		// Process all files
		for fieldName, files := range r.MultipartForm.File {
			for _, fileHeader := range files {
				file, err := fileHeader.Open()
				if err != nil {
					response.Success = false
					response.Message = "Failed to open file"
					continue
				}
				defer file.Close()

				// Read content
				content := make([]byte, fileHeader.Size)
				file.Read(content)

				// Store in fake storage
				path := fmt.Sprintf("uploads/%s", fileHeader.Filename)
				if err := storage.Put(path, content); err != nil {
					response.Success = false
					response.Message = "Failed to store file"
					continue
				}

				// Add to response
				response.Files = append(response.Files, UploadedFileResponse{
					Name:     fileHeader.Filename,
					Path:     path,
					URL:      storage.URL(path),
					Size:     fileHeader.Size,
					MimeType: fileHeader.Header.Get("Content-Type"),
				})

				_ = fieldName // Use fieldName if needed
			}
		}

		// Send JSON response
		w.Header().Set("Content-Type", "application/json")
		if response.Success {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}

		// In real code, you would use json.NewEncoder(w).Encode(response)
	}
}

// AssertFileUploaded checks if a file was successfully uploaded
func AssertFileUploaded(t TestingT, storage *storageTesting.FakeStorage, expectedPath string) {
	t.Helper()

	if !storage.Exists(expectedPath) {
		t.Errorf("Expected file to be uploaded at path: %s", expectedPath)
	}
}

// AssertFileContent checks if uploaded file has expected content
func AssertFileContent(t TestingT, storage *storageTesting.FakeStorage, path string, expectedContent []byte) {
	t.Helper()

	content, err := storage.Get(path)
	if err != nil {
		t.Errorf("Failed to get uploaded file: %v", err)
		return
	}

	if !bytes.Equal(content, expectedContent) {
		t.Errorf("File content mismatch at %s\nExpected: %q\nGot: %q",
			path, expectedContent, content)
	}
}

// TestingT is a subset of testing.T for helper functions
type TestingT interface {
	Helper()
	Errorf(format string, args ...interface{})
}
