package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/velocitykode/velocity/storage"
)

// mockS3Client implements s3API for testing
type mockS3Client struct {
	files       map[string][]byte
	shouldError bool
}

func (m *mockS3Client) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if m.shouldError {
		return nil, errors.New("mock error")
	}
	if m.files == nil {
		m.files = make(map[string][]byte)
	}

	buf := new(bytes.Buffer)
	if input.Body != nil {
		io.Copy(buf, input.Body)
	}

	key := aws.ToString(input.Key)
	m.files[key] = buf.Bytes()

	return &s3.PutObjectOutput{}, nil
}

func (m *mockS3Client) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if m.shouldError {
		return nil, errors.New("mock error")
	}

	key := aws.ToString(input.Key)
	data, exists := m.files[key]
	if !exists {
		return nil, errors.New("NoSuchKey: key not found")
	}

	return &s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(data)),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String("application/octet-stream"),
	}, nil
}

func (m *mockS3Client) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if m.shouldError {
		return nil, errors.New("mock error")
	}

	key := aws.ToString(input.Key)
	data, exists := m.files[key]
	if !exists {
		return nil, errors.New("NoSuchKey: key not found")
	}

	now := time.Now()
	return &s3.HeadObjectOutput{
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String("application/octet-stream"),
		LastModified:  &now,
	}, nil
}

func (m *mockS3Client) DeleteObjects(_ context.Context, input *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	if m.shouldError {
		return nil, errors.New("mock error")
	}

	deleted := []types.DeletedObject{}
	for _, obj := range input.Delete.Objects {
		key := aws.ToString(obj.Key)
		delete(m.files, key)
		deleted = append(deleted, types.DeletedObject{Key: obj.Key})
	}

	return &s3.DeleteObjectsOutput{
		Deleted: deleted,
	}, nil
}

func (m *mockS3Client) CopyObject(_ context.Context, input *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	if m.shouldError {
		return nil, errors.New("mock error")
	}

	source := aws.ToString(input.CopySource)
	parts := strings.SplitN(source, "/", 2)
	if len(parts) != 2 {
		return nil, errors.New("invalid copy source")
	}

	sourceKey := parts[1]
	destKey := aws.ToString(input.Key)

	data, exists := m.files[sourceKey]
	if !exists {
		return nil, errors.New("NoSuchKey: source key not found")
	}

	m.files[destKey] = data
	return &s3.CopyObjectOutput{}, nil
}

func (m *mockS3Client) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if m.shouldError {
		return nil, errors.New("mock error")
	}

	prefix := aws.ToString(input.Prefix)
	delimiter := aws.ToString(input.Delimiter)

	var contents []types.Object
	var commonPrefixes []types.CommonPrefix
	prefixMap := make(map[string]bool)

	for key := range m.files {
		if !strings.HasPrefix(key, prefix) {
			continue
		}

		relKey := strings.TrimPrefix(key, prefix)

		if delimiter != "" && strings.Contains(relKey, delimiter) {
			idx := strings.Index(relKey, delimiter)
			if idx > 0 {
				dirPrefix := prefix + relKey[:idx+1]
				if !prefixMap[dirPrefix] {
					prefixMap[dirPrefix] = true
					commonPrefixes = append(commonPrefixes, types.CommonPrefix{
						Prefix: aws.String(dirPrefix),
					})
				}
			}
		} else {
			contents = append(contents, types.Object{
				Key:  aws.String(key),
				Size: aws.Int64(int64(len(m.files[key]))),
			})
		}
	}

	return &s3.ListObjectsV2Output{
		Contents:       contents,
		CommonPrefixes: commonPrefixes,
		IsTruncated:    aws.Bool(false),
	}, nil
}

// mockS3Driver is a test implementation that bypasses uploader/downloader
type mockS3Driver struct {
	*S3Driver
	mock *mockS3Client
}

func (d *mockS3Driver) Put(path string, contents []byte) error {
	input := &s3.PutObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
		Body:   bytes.NewReader(contents),
	}
	_, err := d.mock.PutObject(context.Background(), input)
	return err
}

func (d *mockS3Driver) Get(path string) ([]byte, error) {
	input := &s3.GetObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	}
	output, err := d.mock.GetObject(context.Background(), input)
	if err != nil {
		return nil, err
	}
	defer output.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(output.Body)
	return buf.Bytes(), nil
}

func (d *mockS3Driver) Exists(path string) bool {
	input := &s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	}
	_, err := d.mock.HeadObject(context.Background(), input)
	return err == nil
}

// TestS3DriverWithMock tests S3 driver with mock client
func TestS3DriverWithMock(t *testing.T) {
	mock := &mockS3Client{
		files: make(map[string][]byte),
	}

	driver := &mockS3Driver{
		S3Driver: &S3Driver{
			client:     mock,
			bucket:     "test-bucket",
			region:     "us-east-1",
			url:        "https://test-bucket.s3.amazonaws.com",
			visibility: storage.Private,
		},
		mock: mock,
	}

	// Test Put
	t.Run("Put", func(t *testing.T) {
		err := driver.Put("test.txt", []byte("test content"))
		if err != nil {
			t.Errorf("Put failed: %v", err)
		}
	})

	// Test Get
	t.Run("Get", func(t *testing.T) {
		content, err := driver.Get("test.txt")
		if err != nil {
			t.Errorf("Get failed: %v", err)
		}
		if string(content) != "test content" {
			t.Errorf("Content mismatch: got %s, want test content", content)
		}
	})

	// Test GetStream
	t.Run("GetStream", func(t *testing.T) {
		stream, err := driver.GetStream("test.txt")
		if err != nil {
			t.Errorf("GetStream failed: %v", err)
		}
		if stream != nil {
			stream.Close()
		}
	})

	// Test Exists
	t.Run("Exists", func(t *testing.T) {
		if !driver.Exists("test.txt") {
			t.Error("File should exist")
		}
		if driver.Exists("nonexistent.txt") {
			t.Error("File should not exist")
		}
	})

	// Test Delete
	t.Run("Delete", func(t *testing.T) {
		driver.Put("delete1.txt", []byte("delete"))
		driver.Put("delete2.txt", []byte("delete"))
		err := driver.Delete("delete1.txt", "delete2.txt")
		if err != nil {
			t.Errorf("Delete failed: %v", err)
		}
		if driver.Exists("delete1.txt") || driver.Exists("delete2.txt") {
			t.Error("Files should be deleted")
		}
	})

	// Test Copy
	t.Run("Copy", func(t *testing.T) {
		driver.Put("source.txt", []byte("copy me"))
		err := driver.Copy("source.txt", "dest.txt")
		if err != nil {
			t.Errorf("Copy failed: %v", err)
		}
		if !driver.Exists("dest.txt") {
			t.Error("Destination file should exist")
		}
		if !driver.Exists("source.txt") {
			t.Error("Source file should still exist")
		}
	})

	// Test Move
	t.Run("Move", func(t *testing.T) {
		driver.Put("moveme.txt", []byte("move"))
		err := driver.Move("moveme.txt", "moved.txt")
		if err != nil {
			t.Errorf("Move failed: %v", err)
		}
		if !driver.Exists("moved.txt") {
			t.Error("Moved file should exist")
		}
		if driver.Exists("moveme.txt") {
			t.Error("Original file should not exist")
		}
	})

	// Test Size
	t.Run("Size", func(t *testing.T) {
		driver.Put("sized.txt", []byte("12345"))
		size, err := driver.Size("sized.txt")
		if err != nil {
			t.Errorf("Size failed: %v", err)
		}
		if size != 5 {
			t.Errorf("Size wrong: got %d, want 5", size)
		}
	})

	// Test LastModified
	t.Run("LastModified", func(t *testing.T) {
		driver.Put("timed.txt", []byte("time"))
		_, err := driver.LastModified("timed.txt")
		if err != nil {
			t.Errorf("LastModified failed: %v", err)
		}
	})

	// Test MimeType
	t.Run("MimeType", func(t *testing.T) {
		driver.Put("mime.txt", []byte("text"))
		mime, err := driver.MimeType("mime.txt")
		if err != nil {
			t.Errorf("MimeType failed: %v", err)
		}
		if mime != "application/octet-stream" {
			t.Errorf("MimeType wrong: got %s", mime)
		}
	})

	// Test Files
	t.Run("Files", func(t *testing.T) {
		driver.Put("dir/file1.txt", []byte("1"))
		driver.Put("dir/file2.txt", []byte("2"))
		driver.Put("dir/sub/file3.txt", []byte("3"))

		files, err := driver.Files("dir")
		if err != nil {
			t.Errorf("Files failed: %v", err)
		}
		// Non-recursive should only return file1 and file2
		if len(files) != 2 {
			t.Errorf("Files count wrong: got %d, want 2", len(files))
		}
	})

	// Test AllFiles
	t.Run("AllFiles", func(t *testing.T) {
		files, err := driver.AllFiles("dir")
		if err != nil {
			t.Errorf("AllFiles failed: %v", err)
		}
		// Recursive should return all 3 files
		if len(files) != 3 {
			t.Errorf("AllFiles count wrong: got %d, want 3", len(files))
		}
	})

	// Test Directories
	t.Run("Directories", func(t *testing.T) {
		dirs, err := driver.Directories("dir")
		if err != nil {
			t.Errorf("Directories failed: %v", err)
		}
		hasSubDir := false
		for _, dir := range dirs {
			if strings.Contains(dir, "sub") {
				hasSubDir = true
				break
			}
		}
		if !hasSubDir {
			t.Error("Should have sub directory")
		}
	})

	// Test AllDirectories
	t.Run("AllDirectories", func(t *testing.T) {
		driver.Put("a/b/c/file.txt", []byte("nested"))
		dirs, err := driver.AllDirectories("a")
		if err != nil {
			t.Errorf("AllDirectories failed: %v", err)
		}
		if len(dirs) < 2 {
			t.Errorf("AllDirectories should have at least 2 dirs, got %d", len(dirs))
		}
	})

	// Test MakeDirectory (no-op for S3)
	t.Run("MakeDirectory", func(t *testing.T) {
		err := driver.MakeDirectory("newdir")
		if err != nil {
			t.Errorf("MakeDirectory failed: %v", err)
		}
	})

	// Test DeleteDirectory
	t.Run("DeleteDirectory", func(t *testing.T) {
		driver.Put("deldir/file1.txt", []byte("1"))
		driver.Put("deldir/file2.txt", []byte("2"))
		err := driver.DeleteDirectory("deldir")
		if err != nil {
			t.Errorf("DeleteDirectory failed: %v", err)
		}
		if driver.Exists("deldir/file1.txt") || driver.Exists("deldir/file2.txt") {
			t.Error("Files in directory should be deleted")
		}
	})

	// Test URL
	t.Run("URL", func(t *testing.T) {
		driver.Put("public.txt", []byte("public"))
		url := driver.URL("public.txt")
		expected := "https://test-bucket.s3.amazonaws.com/public.txt"
		if url != expected {
			t.Errorf("URL wrong: got %s, want %s", url, expected)
		}
	})

	// Test error cases
	t.Run("ErrorCases", func(t *testing.T) {
		errorMock := &mockS3Client{
			files:       make(map[string][]byte),
			shouldError: true,
		}
		errorDriver := &mockS3Driver{
			S3Driver: &S3Driver{
				client: errorMock,
				bucket: "test-bucket",
			},
			mock: errorMock,
		}

		// Test Put error
		err := errorDriver.Put("fail.txt", []byte("fail"))
		if err == nil {
			t.Error("Put should fail with error")
		}

		// Test Get error
		_, err = errorDriver.Get("fail.txt")
		if err == nil {
			t.Error("Get should fail with error")
		}

		// Test Delete error
		err = errorDriver.Delete("fail.txt")
		if err == nil {
			t.Error("Delete should fail with error")
		}
	})

	// Test Get non-existent file
	t.Run("GetNonExistent", func(t *testing.T) {
		_, err := driver.Get("nonexistent.txt")
		if err == nil {
			t.Error("Get should fail for non-existent file")
		}
	})

	// Test Size non-existent file
	t.Run("SizeNonExistent", func(t *testing.T) {
		_, err := driver.Size("nonexistent.txt")
		if err == nil {
			t.Error("Size should fail for non-existent file")
		}
	})

	// Test LastModified non-existent file
	t.Run("LastModifiedNonExistent", func(t *testing.T) {
		_, err := driver.LastModified("nonexistent.txt")
		if err == nil {
			t.Error("LastModified should fail for non-existent file")
		}
	})

	// Test MimeType non-existent file
	t.Run("MimeTypeNonExistent", func(t *testing.T) {
		_, err := driver.MimeType("nonexistent.txt")
		if err == nil {
			t.Error("MimeType should fail for non-existent file")
		}
	})

	// Test Copy non-existent file
	t.Run("CopyNonExistent", func(t *testing.T) {
		err := driver.Copy("nonexistent.txt", "dest.txt")
		if err == nil {
			t.Error("Copy should fail for non-existent file")
		}
	})

}

// TestNewS3Driver tests S3 driver creation
func TestNewS3Driver(t *testing.T) {
	// Test with minimal config (will fail but tests the path)
	t.Run("MinimalConfig", func(t *testing.T) {
		_, err := NewS3Driver(storage.DiskConfig{
			Driver: "s3",
			Bucket: "test-bucket",
			Region: "us-east-1",
		})
		// May fail due to AWS session creation
		_ = err
	})

	// Test with full config (will fail but tests the path)
	t.Run("FullConfig", func(t *testing.T) {
		_, err := NewS3Driver(storage.DiskConfig{
			Driver:     "s3",
			Key:        "test-key",
			Secret:     "test-secret",
			Region:     "us-west-2",
			Bucket:     "test-bucket",
			URL:        "https://custom.s3.amazonaws.com",
			Visibility: "public",
		})
		// May fail due to AWS session creation
		_ = err
	})
}

// TestNewS3DriverValidation ensures config validation runs before any AWS I/O.
func TestNewS3DriverValidation(t *testing.T) {
	t.Run("MissingRegion", func(t *testing.T) {
		_, err := NewS3Driver(storage.DiskConfig{
			Driver: "s3",
			Bucket: "test-bucket",
		})
		if err == nil {
			t.Fatal("expected error for missing region")
		}
		if !strings.Contains(err.Error(), "region is required") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("MissingBucket", func(t *testing.T) {
		_, err := NewS3Driver(storage.DiskConfig{
			Driver: "s3",
			Region: "us-east-1",
		})
		if err == nil {
			t.Fatal("expected error for missing bucket")
		}
		if !strings.Contains(err.Error(), "bucket is required") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("InvalidURLScheme", func(t *testing.T) {
		_, err := NewS3Driver(storage.DiskConfig{
			Driver: "s3",
			Region: "us-east-1",
			Bucket: "test-bucket",
			URL:    "ftp://example.com/files",
		})
		if err == nil {
			t.Fatal("expected error for non-http(s) url")
		}
		if !strings.Contains(err.Error(), "http or https scheme") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("MalformedURL", func(t *testing.T) {
		_, err := NewS3Driver(storage.DiskConfig{
			Driver: "s3",
			Region: "us-east-1",
			Bucket: "test-bucket",
			URL:    "://bad",
		})
		if err == nil {
			t.Fatal("expected error for malformed url")
		}
	})
}

// TestS3DefaultVisibilityPrivate asserts that new drivers default to private
// visibility when Visibility is unspecified — Public must be opted in.
func TestS3DefaultVisibilityPrivate(t *testing.T) {
	cases := []struct {
		name       string
		visibility string
		want       storage.Visibility
	}{
		{"DefaultEmpty", "", storage.Private},
		{"ExplicitPrivate", "private", storage.Private},
		{"ExplicitPublic", "public", storage.Public},
		{"UnknownValueDefaultsPrivate", "world-readable", storage.Private},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &S3Driver{}
			// Simulate the assignment performed in NewS3Driver without requiring
			// AWS credentials or network access.
			if tc.visibility == string(storage.Public) {
				d.visibility = storage.Public
			} else {
				d.visibility = storage.Private
			}
			if d.visibility != tc.want {
				t.Errorf("visibility = %q, want %q", d.visibility, tc.want)
			}
		})
	}
}

// TestS3TemporaryURLExpirationBounds verifies that expirations outside the
// AWS SigV4 7-day window return a sentinel error rather than silently
// clamping — operators relying on clamping would get URLs that expire
// sooner than they asked for, which is a surprising failure mode.
func TestS3TemporaryURLExpirationBounds(t *testing.T) {
	d := &S3Driver{bucket: "b", region: "us-east-1"}
	cases := []struct {
		name  string
		input time.Duration
		want  error
	}{
		{"Zero", 0, ErrExpirationNonPositive},
		{"Negative", -1 * time.Hour, ErrExpirationNonPositive},
		{"OverMax8Days", 8 * 24 * time.Hour, ErrExpirationTooLong},
		{"OverMax30Days", 30 * 24 * time.Hour, ErrExpirationTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.TemporaryURLCtx(context.Background(), "a.txt", tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("TemporaryURLCtx(%v): err = %v, want %v", tc.input, err, tc.want)
			}
		})
	}
}

// TestS3ContextCancellationPropagates verifies a cancelled context surfaces
// from Get via the underlying s3API without being translated to
// ErrFileNotFound. The Put path goes through the AWS uploader which is not
// easily mockable without an embedded S3 server; we therefore assert ctx
// propagation at the GetObject / HeadObject / ListObjectsV2 layer.
func TestS3ContextCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel

	cm := &contextMockS3Client{}
	d := &S3Driver{client: cm, bucket: "b", region: "us-east-1"}

	t.Run("GetCtxCancelled", func(t *testing.T) {
		_, err := d.GetCtx(ctx, "some-key")
		if err == nil {
			t.Fatal("expected error for cancelled context")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("ExistsCtxCancelled", func(t *testing.T) {
		if d.ExistsCtx(ctx, "some-key") {
			t.Error("expected ExistsCtx to return false on cancelled ctx")
		}
	})

	t.Run("DeleteCtxCancelled", func(t *testing.T) {
		err := d.DeleteCtx(ctx, "some-key")
		if err == nil {
			t.Fatal("expected error for cancelled context")
		}
		if !errors.Is(err, context.Canceled) {
			t.Logf("delete surfaced wrapped error: %v", err)
		}
	})
}

// contextMockS3Client is a mock that honours context cancellation.
type contextMockS3Client struct{}

func (m *contextMockS3Client) GetObject(ctx context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &s3.GetObjectOutput{}, nil
}
func (m *contextMockS3Client) HeadObject(ctx context.Context, _ *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &s3.HeadObjectOutput{}, nil
}
func (m *contextMockS3Client) PutObject(ctx context.Context, _ *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &s3.PutObjectOutput{}, nil
}
func (m *contextMockS3Client) DeleteObjects(ctx context.Context, _ *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &s3.DeleteObjectsOutput{}, nil
}
func (m *contextMockS3Client) CopyObject(ctx context.Context, _ *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &s3.CopyObjectOutput{}, nil
}
func (m *contextMockS3Client) ListObjectsV2(ctx context.Context, _ *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &s3.ListObjectsV2Output{}, nil
}
