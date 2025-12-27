package testing

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
)

// FakeFile represents a fake file for testing
type FakeFile struct {
	name     string
	content  []byte
	mimeType string
}

// FakeFileBuilder helps build fake files for testing
type FakeFileBuilder struct{}

// Fake returns a new FakeFileBuilder for creating test files
func Fake() *FakeFileBuilder {
	return &FakeFileBuilder{}
}

// Name returns the file name
func (f *FakeFile) Name() string {
	return f.name
}

// Content returns the file content as bytes
func (f *FakeFile) Content() []byte {
	return f.content
}

// Reader returns an io.Reader for the file content
func (f *FakeFile) Reader() io.Reader {
	return bytes.NewReader(f.content)
}

// Size returns the file size in bytes
func (f *FakeFile) Size() int64 {
	return int64(len(f.content))
}

// MimeType returns the MIME type of the file
func (f *FakeFile) MimeType() string {
	return f.mimeType
}

// String returns the file content as a string
func (f *FakeFile) String() string {
	return string(f.content)
}

// Image creates a fake image file with specified dimensions
func (b *FakeFileBuilder) Image(name string, width, height int) *FakeFile {
	// Create a simple image
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Fill with a gradient for visual testing
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r := uint8((x * 255) / width)
			g := uint8((y * 255) / height)
			b := uint8(128)
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}

	// Encode based on file extension
	var buf bytes.Buffer
	var mimeType string
	ext := strings.ToLower(getExtension(name))

	switch ext {
	case ".png":
		png.Encode(&buf, img)
		mimeType = "image/png"
	case ".jpg", ".jpeg":
		jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
		mimeType = "image/jpeg"
	default:
		// Default to PNG
		png.Encode(&buf, img)
		mimeType = "image/png"
	}

	return &FakeFile{
		name:     name,
		content:  buf.Bytes(),
		mimeType: mimeType,
	}
}

// Create creates a fake file with specified size in KB
func (b *FakeFileBuilder) Create(name string, sizeInKB int) *FakeFile {
	return b.CreateWithMimeType(name, sizeInKB, detectMimeTypeFromName(name))
}

// CreateWithMimeType creates a fake file with specified size and MIME type
func (b *FakeFileBuilder) CreateWithMimeType(name string, sizeInKB int, mimeType string) *FakeFile {
	sizeInBytes := sizeInKB * 1024
	content := make([]byte, sizeInBytes)

	// Fill with appropriate content based on MIME type
	switch {
	case strings.HasPrefix(mimeType, "text/"):
		// Fill with readable text
		text := "Lorem ipsum dolor sit amet, consectetur adipiscing elit. "
		for i := 0; i < sizeInBytes; i++ {
			content[i] = text[i%len(text)]
		}
	case mimeType == "application/pdf":
		// Add PDF header
		copy(content, []byte("%PDF-1.4\n"))
		// Fill rest with random binary data
		rand.Read(content[9:])
	case mimeType == "application/zip":
		// Add ZIP header (PK)
		copy(content, []byte("PK\x03\x04"))
		// Fill rest with random binary data
		rand.Read(content[4:])
	case strings.HasPrefix(mimeType, "application/"):
		// Binary content
		rand.Read(content)
	default:
		// Default to random bytes
		rand.Read(content)
	}

	return &FakeFile{
		name:     name,
		content:  content,
		mimeType: mimeType,
	}
}

// PDF creates a fake PDF file
func (b *FakeFileBuilder) PDF(name string, sizeInKB int) *FakeFile {
	content := make([]byte, sizeInKB*1024)

	// Add PDF header and structure
	pdfContent := `%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> >> /MediaBox [0 0 612 792] /Contents 4 0 R >>
endobj
4 0 obj
<< /Length 44 >>
stream
BT
/F1 12 Tf
100 700 Td
(Test PDF Document) Tj
ET
endstream
endobj
xref
0 5
0000000000 65535 f
0000000009 00000 n
0000000058 00000 n
0000000115 00000 n
0000000274 00000 n
trailer
<< /Size 5 /Root 1 0 R >>
startxref
365
%%EOF`

	copy(content, []byte(pdfContent))

	// Fill remaining space with comments
	remaining := len(content) - len(pdfContent)
	if remaining > 0 {
		comment := []byte("\n% Additional content for file size\n% ")
		for i := len(pdfContent); i < len(content)-len(comment); i += len(comment) {
			copy(content[i:], comment)
		}
	}

	return &FakeFile{
		name:     name,
		content:  content,
		mimeType: "application/pdf",
	}
}

// CSV creates a fake CSV file
func (b *FakeFileBuilder) CSV(name string, rows int) *FakeFile {
	var buf bytes.Buffer

	// Write header
	buf.WriteString("id,name,email,created_at\n")

	// Write rows
	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&buf, "%d,User %d,user%d@example.com,2024-01-%02d\n",
			i, i, i, (i%28)+1)
	}

	return &FakeFile{
		name:     name,
		content:  buf.Bytes(),
		mimeType: "text/csv",
	}
}

// JSON creates a fake JSON file
func (b *FakeFileBuilder) JSON(name string, data interface{}) *FakeFile {
	var content []byte

	if data == nil {
		// Default JSON structure
		content = []byte(`{
  "id": 1,
  "name": "Test User",
  "email": "test@example.com",
  "active": true,
  "metadata": {
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  },
  "tags": ["test", "example", "fake"]
}`)
	} else {
		// Use provided data
		switch v := data.(type) {
		case string:
			content = []byte(v)
		case []byte:
			content = v
		default:
			content = []byte(fmt.Sprintf("%v", data))
		}
	}

	return &FakeFile{
		name:     name,
		content:  content,
		mimeType: "application/json",
	}
}

// XML creates a fake XML file
func (b *FakeFileBuilder) XML(name string) *FakeFile {
	content := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<root>
  <user>
    <id>1</id>
    <name>Test User</name>
    <email>test@example.com</email>
    <active>true</active>
  </user>
  <metadata>
    <created_at>2024-01-01T00:00:00Z</created_at>
    <updated_at>2024-01-01T00:00:00Z</updated_at>
  </metadata>
</root>`)

	return &FakeFile{
		name:     name,
		content:  content,
		mimeType: "application/xml",
	}
}

// Video creates a fake video file (minimal valid MP4 structure)
func (b *FakeFileBuilder) Video(name string, sizeInKB int) *FakeFile {
	content := make([]byte, sizeInKB*1024)

	// Add basic MP4 file structure (ftyp box)
	ftypBox := []byte{
		0x00, 0x00, 0x00, 0x20, // Box size (32 bytes)
		'f', 't', 'y', 'p',     // Box type
		'i', 's', 'o', 'm',     // Major brand
		0x00, 0x00, 0x02, 0x00, // Minor version
		'i', 's', 'o', 'm',     // Compatible brand
		'i', 's', 'o', '2',     // Compatible brand
		'a', 'v', 'c', '1',     // Compatible brand
		'm', 'p', '4', '1',     // Compatible brand
	}

	copy(content, ftypBox)

	// Add mdat box header (media data)
	mdatHeader := []byte{
		0x00, 0x00, 0x00, 0x08, // Box size (just header)
		'm', 'd', 'a', 't',     // Box type
	}
	copy(content[32:], mdatHeader)

	// Fill rest with video-like data
	rand.Read(content[40:])

	return &FakeFile{
		name:     name,
		content:  content,
		mimeType: "video/mp4",
	}
}

// Archive creates a fake archive file (ZIP)
func (b *FakeFileBuilder) Archive(name string, sizeInKB int) *FakeFile {
	content := make([]byte, sizeInKB*1024)

	// Add ZIP file header
	zipHeader := []byte{
		'P', 'K', 0x03, 0x04, // Local file header signature
		0x14, 0x00,           // Version needed
		0x00, 0x00,           // Flags
		0x00, 0x00,           // Compression method (stored)
		0x00, 0x00,           // Last mod time
		0x00, 0x00,           // Last mod date
	}

	copy(content, zipHeader)

	// Add file name
	fileName := []byte("test.txt")
	binary.LittleEndian.PutUint16(content[26:], uint16(len(fileName)))
	copy(content[30:], fileName)

	// Add central directory at the end
	cdOffset := len(content) - 100
	if cdOffset > 30+len(fileName) {
		content[cdOffset] = 'P'
		content[cdOffset+1] = 'K'
		content[cdOffset+2] = 0x01
		content[cdOffset+3] = 0x02
	}

	// End of central directory
	eocdr := len(content) - 22
	if eocdr > cdOffset+4 {
		copy(content[eocdr:], []byte{'P', 'K', 0x05, 0x06})
	}

	mimeType := "application/zip"
	if strings.HasSuffix(name, ".tar") {
		mimeType = "application/x-tar"
	} else if strings.HasSuffix(name, ".gz") {
		mimeType = "application/gzip"
	} else if strings.HasSuffix(name, ".rar") {
		mimeType = "application/x-rar-compressed"
	}

	return &FakeFile{
		name:     name,
		content:  content,
		mimeType: mimeType,
	}
}

// getExtension returns the file extension including the dot
func getExtension(name string) string {
	idx := strings.LastIndex(name, ".")
	if idx == -1 {
		return ""
	}
	return name[idx:]
}

// detectMimeTypeFromName guesses MIME type from file name
func detectMimeTypeFromName(name string) string {
	ext := strings.ToLower(getExtension(name))

	mimeTypes := map[string]string{
		".txt":  "text/plain",
		".html": "text/html",
		".css":  "text/css",
		".js":   "application/javascript",
		".json": "application/json",
		".xml":  "application/xml",
		".pdf":  "application/pdf",
		".doc":  "application/msword",
		".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		".xls":  "application/vnd.ms-excel",
		".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		".ppt":  "application/vnd.ms-powerpoint",
		".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		".zip":  "application/zip",
		".tar":  "application/x-tar",
		".gz":   "application/gzip",
		".rar":  "application/x-rar-compressed",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".bmp":  "image/bmp",
		".svg":  "image/svg+xml",
		".mp3":  "audio/mpeg",
		".wav":  "audio/wav",
		".mp4":  "video/mp4",
		".avi":  "video/x-msvideo",
		".mov":  "video/quicktime",
	}

	if mimeType, ok := mimeTypes[ext]; ok {
		return mimeType
	}

	return "application/octet-stream"
}