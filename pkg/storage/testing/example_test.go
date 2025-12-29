package testing_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	storageTesting "github.com/velocitykode/velocity/pkg/storage/testing"
	httpTesting "github.com/velocitykode/velocity/pkg/testing/http"
)

// Example: Testing a simple file upload
func TestSimpleFileUpload(t *testing.T) {
	// Create fake storage
	storage := storageTesting.StorageFake()

	// Create a fake image file
	file := storageTesting.Fake().Image("avatar.jpg", 200, 200)

	// Create upload request
	builder := httpTesting.NewUploadBuilder()
	builder.AddFile("avatar", file)
	req, err := builder.Build("POST", "/upload/avatar")
	if err != nil {
		t.Fatal(err)
	}

	// Create test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse multipart form
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "Failed to parse form", http.StatusBadRequest)
			return
		}

		// Get uploaded file
		file, header, err := r.FormFile("avatar")
		if err != nil {
			http.Error(w, "Failed to get file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		// Read content
		content := make([]byte, header.Size)
		file.Read(content)

		// Store in fake storage
		path := "avatars/" + header.Filename
		if err := storage.Put(path, content); err != nil {
			http.Error(w, "Failed to store file", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	})

	// Execute request
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Assert response
	if rr.Code != http.StatusOK {
		t.Errorf("Handler returned wrong status code: got %v want %v", rr.Code, http.StatusOK)
	}

	// Assert file was stored
	storage.Assert(t).AssertExists("avatars/avatar.jpg")
	storage.Assert(t).AssertSize("avatars/avatar.jpg", file.Size())
	storage.Assert(t).AssertMimeType("avatars/avatar.jpg", "image/jpeg")
}

// Example: Testing multiple file uploads
func TestMultipleFileUploads(t *testing.T) {
	storage := storageTesting.StorageFake()

	// Create multiple fake files
	image := storageTesting.Fake().Image("photo.png", 300, 300)
	pdf := storageTesting.Fake().PDF("document.pdf", 100)
	csv := storageTesting.Fake().CSV("data.csv", 50)

	// Build request with multiple files
	builder := httpTesting.NewUploadBuilder()
	builder.AddFile("files", image)
	builder.AddFile("files", pdf)
	builder.AddFile("files", csv)
	builder.AddField("category", "important")

	req, _ := builder.Build("POST", "/upload/multiple")

	// Handler that processes multiple files
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(32 << 20)

		category := r.FormValue("category")
		files := r.MultipartForm.File["files"]

		for _, fileHeader := range files {
			file, _ := fileHeader.Open()
			defer file.Close()

			content := make([]byte, fileHeader.Size)
			file.Read(content)

			path := category + "/" + fileHeader.Filename
			storage.Put(path, content)
		}

		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Assertions
	assert := storage.Assert(t)
	assert.AssertCount(3)
	assert.AssertExists("important/photo.png")
	assert.AssertExists("important/document.pdf")
	assert.AssertExists("important/data.csv")
	assert.AssertDirectory("important", []string{
		"important/photo.png",
		"important/document.pdf",
		"important/data.csv",
	})
}

// Example: Testing file validation
func TestFileValidation(t *testing.T) {
	t.Skip("TODO: fix multipart form parsing timeout")
	storage := storageTesting.StorageFake()

	// Create a file that's too large
	largeFile := storageTesting.Fake().Create("large.txt", 11*1024*1024) // 11MB

	builder := httpTesting.NewUploadBuilder()
	builder.AddFile("file", largeFile)
	req, _ := builder.Build("POST", "/upload")

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Limit to 10MB
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "File too large", http.StatusRequestEntityTooLarge)
			return
		}

		file, header, _ := r.FormFile("file")
		defer file.Close()

		// Check file size
		if header.Size > 10*1024*1024 {
			http.Error(w, "File exceeds 10MB limit", http.StatusBadRequest)
			return
		}

		// Check file type
		if header.Header.Get("Content-Type") != "text/plain" {
			http.Error(w, "Invalid file type", http.StatusBadRequest)
			return
		}

		content := make([]byte, header.Size)
		file.Read(content)
		storage.Put("uploads/"+header.Filename, content)

		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Should fail validation
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("Expected status %d, got %d", http.StatusRequestEntityTooLarge, rr.Code)
	}

	// File should not be stored
	storage.Assert(t).AssertNothingStored()
}

// Example: Testing image upload with resizing simulation
func TestImageUploadWithProcessing(t *testing.T) {
	storage := storageTesting.StorageFake()

	// Create a fake image
	original := storageTesting.Fake().Image("profile.jpg", 1000, 1000)

	builder := httpTesting.NewUploadBuilder()
	builder.AddFile("image", original)
	req, _ := builder.Build("POST", "/upload/image")

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(10 << 20)

		file, header, _ := r.FormFile("image")
		defer file.Close()

		content := make([]byte, header.Size)
		file.Read(content)

		// Store original
		storage.Put("images/original/"+header.Filename, content)

		// Simulate creating thumbnails
		thumbnail := storageTesting.Fake().Image("profile_thumb.jpg", 150, 150)
		storage.Put("images/thumbnail/"+header.Filename, thumbnail.Content())

		// Simulate creating medium size
		medium := storageTesting.Fake().Image("profile_medium.jpg", 500, 500)
		storage.Put("images/medium/"+header.Filename, medium.Content())

		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Assertions
	assert := storage.Assert(t)
	assert.AssertCount(3) // Original + thumbnail + medium
	assert.AssertExists("images/original/profile.jpg")
	assert.AssertExists("images/thumbnail/profile.jpg")
	assert.AssertExists("images/medium/profile.jpg")
}

// Example: Testing file operations after upload
func TestFileOperationsAfterUpload(t *testing.T) {
	storage := storageTesting.StorageFake()

	// Upload a file
	file := storageTesting.Fake().JSON("config.json", nil)
	storage.Put("uploads/config.json", file.Content())

	// Test copy operation
	err := storage.Copy("uploads/config.json", "backups/config.json")
	if err != nil {
		t.Fatal(err)
	}

	assert := storage.Assert(t)
	assert.AssertCopied("uploads/config.json", "backups/config.json")

	// Test move operation
	storage.Put("temp/draft.json", file.Content())
	err = storage.Move("temp/draft.json", "published/final.json")
	if err != nil {
		t.Fatal(err)
	}

	assert.AssertMoved("temp/draft.json", "published/final.json")

	// Test delete operation
	storage.Delete("uploads/config.json")
	assert.AssertDeleted("uploads/config.json")

	// Final state check
	assert.AssertCount(2) // backups/config.json and published/final.json
}

// Example: Testing storage failure scenarios
func TestStorageFailures(t *testing.T) {
	storage := storageTesting.StorageFake()

	// Configure storage to fail
	storage.ShouldFail("Storage is full")

	file := storageTesting.Fake().Create("test.txt", 1)

	builder := httpTesting.NewUploadBuilder()
	builder.AddFile("file", file)
	req, _ := builder.Build("POST", "/upload")

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(10 << 20)

		file, header, _ := r.FormFile("file")
		defer file.Close()

		content := make([]byte, header.Size)
		file.Read(content)

		if err := storage.Put("uploads/"+header.Filename, content); err != nil {
			http.Error(w, err.Error(), http.StatusInsufficientStorage)
			return
		}

		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Should return storage error
	if rr.Code != http.StatusInsufficientStorage {
		t.Errorf("Expected status %d, got %d", http.StatusInsufficientStorage, rr.Code)
	}

	// Nothing should be stored
	storage.Assert(t).AssertNothingStored()
}

// Example: Testing with custom assertions
func TestCustomAssertions(t *testing.T) {
	storage := storageTesting.StorageFake()

	// Upload some test files
	storage.Put("logs/app.log", []byte("Error: Something went wrong\nInfo: Process completed"))
	storage.Put("configs/app.yaml", []byte("debug: true\nport: 8080"))

	assert := storage.Assert(t)

	// Test operation tracking (before content assertions which call Get)
	assert.AssertOperationCalled("put", "logs/app.log")
	assert.AssertOperationCount("put", 2)
	assert.AssertLastOperation("put")

	// Test content assertions
	assert.AssertContains("logs/app.log", "Error")
	assert.AssertNotContains("logs/app.log", "Warning")

	// Test URL generation
	url := storage.URL("configs/app.yaml")
	assert.AssertURL("configs/app.yaml", url)

	// Test temporary URL
	assert.AssertTemporaryURL("logs/app.log")

	// Debug helpers
	if t.Failed() {
		assert.DumpStoredFiles()
		assert.DumpOperations()
	}
}

// Example: Testing chunked uploads simulation
func TestChunkedUpload(t *testing.T) {
	storage := storageTesting.StorageFake()

	// Simulate uploading a large file in chunks
	totalSize := 5 * 1024 * 1024 // 5MB
	chunkSize := 1024 * 1024     // 1MB chunks

	var uploadedContent []byte

	for i := 0; i < totalSize; i += chunkSize {
		// Create chunk
		size := chunkSize
		if i+chunkSize > totalSize {
			size = totalSize - i
		}

		chunk := make([]byte, size)
		for j := range chunk {
			chunk[j] = byte((i + j) % 256)
		}

		uploadedContent = append(uploadedContent, chunk...)

		// Simulate chunk upload
		chunkPath := "chunks/video_" + string(rune('0'+i/chunkSize)) + ".part"
		storage.Put(chunkPath, chunk)
	}

	// Verify all chunks uploaded
	assert := storage.Assert(t)
	for i := 0; i < 5; i++ {
		chunkPath := "chunks/video_" + string(rune('0'+i)) + ".part"
		assert.AssertExists(chunkPath)
	}

	// Simulate merging chunks
	storage.Put("videos/complete.mp4", uploadedContent)
	assert.AssertSize("videos/complete.mp4", int64(totalSize))

	// Clean up chunks
	for i := 0; i < 5; i++ {
		chunkPath := "chunks/video_" + string(rune('0'+i)) + ".part"
		storage.Delete(chunkPath)
	}

	assert.AssertCount(1) // Only complete video remains
}
