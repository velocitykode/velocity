package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/velocitykode/velocity/storage"
)

// uploadMockClient implements manager.UploadAPIClient for tests that need
// the real manager.Uploader to drive a PutStream upload. Only PutObject
// is meaningfully exercised for sub-part-size uploads (< 5 MiB default
// PartSize); the multipart methods are present so the interface is
// satisfied if a test pushes a larger body.
type uploadMockClient struct {
	mu          sync.Mutex
	puts        map[string][]byte
	contentType map[string]string
	failPutOnce bool
}

func newUploadMockClient() *uploadMockClient {
	return &uploadMockClient{
		puts:        make(map[string][]byte),
		contentType: make(map[string]string),
	}
}

func (m *uploadMockClient) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPutOnce {
		m.failPutOnce = false
		return nil, errors.New("mock: forced PutObject failure")
	}
	buf := new(bytes.Buffer)
	if input.Body != nil {
		// Copy through the supplied Body so capReader's running counter
		// advances even though the mock isn't a real network endpoint.
		if _, err := io.Copy(buf, input.Body); err != nil {
			return nil, err
		}
	}
	key := aws.ToString(input.Key)
	m.puts[key] = buf.Bytes()
	m.contentType[key] = aws.ToString(input.ContentType)
	return &s3.PutObjectOutput{}, nil
}

func (m *uploadMockClient) UploadPart(_ context.Context, _ *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	return nil, errors.New("mock: multipart UploadPart not implemented for these tests")
}

func (m *uploadMockClient) CreateMultipartUpload(_ context.Context, _ *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	return nil, errors.New("mock: multipart CreateMultipartUpload not implemented for these tests")
}

func (m *uploadMockClient) CompleteMultipartUpload(_ context.Context, _ *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	return nil, errors.New("mock: multipart CompleteMultipartUpload not implemented for these tests")
}

func (m *uploadMockClient) AbortMultipartUpload(_ context.Context, _ *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	return &s3.AbortMultipartUploadOutput{}, nil
}

// The remaining s3API surface is required to satisfy the driver's typed
// field but is never invoked by PutStreamCtx; the stubs return errors so
// any accidental call is obvious in test output.
func (m *uploadMockClient) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return nil, errors.New("mock: GetObject not implemented")
}

func (m *uploadMockClient) HeadObject(_ context.Context, _ *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return nil, errors.New("mock: HeadObject not implemented")
}

func (m *uploadMockClient) DeleteObjects(_ context.Context, _ *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	return nil, errors.New("mock: DeleteObjects not implemented")
}

func (m *uploadMockClient) CopyObject(_ context.Context, _ *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	return nil, errors.New("mock: CopyObject not implemented")
}

func (m *uploadMockClient) ListObjectsV2(_ context.Context, _ *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return nil, errors.New("mock: ListObjectsV2 not implemented")
}

// newS3DriverWithUploadMock wires both client AND uploader against the
// shared mock so PutStreamCtx exercises the real manager.Uploader code
// path (sniff -> cap -> SDK chunker -> mock PutObject).
func newS3DriverWithUploadMock(visibility storage.Visibility) (*S3Driver, *uploadMockClient) {
	mock := newUploadMockClient()
	driver := &S3Driver{
		client:     mock,
		uploader:   manager.NewUploader(mock),
		bucket:     "test-bucket",
		region:     "us-east-1",
		url:        "https://test-bucket.s3.amazonaws.com",
		visibility: visibility,
	}
	return driver, mock
}

func TestS3PutStream_SuccessBelowCap(t *testing.T) {
	driver, mock := newS3DriverWithUploadMock(storage.Private)
	body := []byte("hello world from a small stream")
	if err := driver.PutStream("docs/hello.txt", bytes.NewReader(body)); err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	got, ok := mock.puts["docs/hello.txt"]
	if !ok {
		t.Fatalf("expected key to be present in mock")
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body mismatch: got %q, want %q", got, body)
	}
	// MIME sniffing should classify ASCII text as text/plain.
	if ct := mock.contentType["docs/hello.txt"]; !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type should start with text/plain, got %q", ct)
	}
}

// TestS3PutStream_RefusesOverCap pins the headline fix: a stream that
// would exceed maxS3StreamSize is rejected via ErrStreamTooLarge WITHOUT
// the whole body being buffered into RAM first. The cap reader trips on
// the read that crosses the boundary; manager.Uploader surfaces the
// error and we unwrap it back to the sentinel.
func TestS3PutStream_RefusesOverCap(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 100 MiB upload cap-overflow test in -short mode")
	}
	mock := newUploadMockClient()
	driver := &S3Driver{
		client: mock,
		uploader: manager.NewUploader(mock, func(u *manager.Uploader) {
			// Single-part path so the cap reader trips inside one
			// PutObject call rather than waiting for the multipart
			// chunker (which the mock does not implement).
			u.PartSize = maxS3StreamSize + (16 << 20)
		}),
		bucket:     "test-bucket",
		region:     "us-east-1",
		visibility: storage.Private,
	}
	// Reader that yields max+1 bytes of NUL data. We must NOT buffer
	// the whole 100+ MiB up front (defeats the point of the test);
	// the reader streams bytes on demand from a counter.
	stream := &repeatingReader{remaining: maxS3StreamSize + 1}
	err := driver.PutStream("overflow.bin", stream)
	if err == nil {
		t.Fatal("expected PutStream to reject oversized stream")
	}
	if !errors.Is(err, ErrStreamTooLarge) {
		t.Errorf("expected ErrStreamTooLarge, got %v", err)
	}
}

// TestS3PutStream_AcceptsExactlyAtCap pins the lower boundary of the
// guard: a stream of exactly maxS3StreamSize bytes is uploaded without
// error. The capReader's +1 lookahead must not refuse a payload that
// fits.
//
// NOTE: 100 MiB exceeds the 5 MiB default Uploader.PartSize, so this
// test would normally trigger multipart upload (which the mock does not
// implement). We patch the uploader's PartSize up to >100 MiB so the
// upload stays in the single-PutObject path.
func TestS3PutStream_AcceptsExactlyAtCap(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 100 MiB upload boundary test in -short mode")
	}
	mock := newUploadMockClient()
	driver := &S3Driver{
		client: mock,
		uploader: manager.NewUploader(mock, func(u *manager.Uploader) {
			// Force single-part path: PartSize > body size.
			u.PartSize = maxS3StreamSize + (16 << 20)
		}),
		bucket:     "test-bucket",
		region:     "us-east-1",
		visibility: storage.Private,
	}
	stream := &repeatingReader{remaining: maxS3StreamSize}
	if err := driver.PutStream("atcap.bin", stream); err != nil {
		t.Fatalf("PutStream at exactly cap should succeed, got: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	got, ok := mock.puts["atcap.bin"]
	if !ok {
		t.Fatal("expected key in mock")
	}
	if int64(len(got)) != maxS3StreamSize {
		t.Errorf("expected exactly %d bytes uploaded, got %d", maxS3StreamSize, len(got))
	}
}

// TestS3PutStream_MIMESniffOnShortStream confirms detectMimeType still
// works when the source stream is shorter than mimeSniffSize (the +1
// lookahead read is well below 512 bytes here).
func TestS3PutStream_MIMESniffOnShortStream(t *testing.T) {
	driver, mock := newS3DriverWithUploadMock(storage.Private)
	// 3-byte gzip magic + nothing else.
	body := []byte{0x1f, 0x8b, 0x08}
	if err := driver.PutStream("tiny.bin", bytes.NewReader(body)); err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if ct := mock.contentType["tiny.bin"]; ct != "application/x-gzip" && ct != "application/gzip" {
		t.Errorf("expected gzip content-type, got %q", ct)
	}
}

// TestCapReader_TableDriven pins the cap reader semantics on its own,
// independent of the AWS uploader. The wrapper must surface
// ErrStreamTooLarge on the read that crosses max bytes, return the
// preserved prefix bytes on that same Read call, and be idempotent
// afterwards.
func TestCapReader_TableDriven(t *testing.T) {
	cases := []struct {
		name       string
		max        int64
		bodyLen    int64
		wantErr    error
		wantBytes  int64 // total bytes successfully delivered before err
		wantTipped bool
	}{
		{
			name:      "below cap",
			max:       100,
			bodyLen:   50,
			wantErr:   nil,
			wantBytes: 50,
		},
		{
			name:      "exactly at cap",
			max:       100,
			bodyLen:   100,
			wantErr:   nil,
			wantBytes: 100,
		},
		{
			name:       "one byte over cap",
			max:        100,
			bodyLen:    101,
			wantErr:    ErrStreamTooLarge,
			wantBytes:  100,
			wantTipped: true,
		},
		{
			name:       "well over cap",
			max:        100,
			bodyLen:    10_000,
			wantErr:    ErrStreamTooLarge,
			wantBytes:  100,
			wantTipped: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := &repeatingReader{remaining: tc.bodyLen}
			c := &capReader{r: body, max: tc.max}
			out := new(bytes.Buffer)
			n, err := io.Copy(out, c)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
			}
			if n != tc.wantBytes {
				t.Errorf("bytes delivered: got %d, want %d", n, tc.wantBytes)
			}
			if c.tipped != tc.wantTipped {
				t.Errorf("tipped flag: got %v, want %v", c.tipped, tc.wantTipped)
			}
			if tc.wantTipped {
				// Subsequent reads return the sentinel idempotently.
				p := make([]byte, 8)
				_, err := c.Read(p)
				if !errors.Is(err, ErrStreamTooLarge) {
					t.Errorf("post-tip read: expected ErrStreamTooLarge, got %v", err)
				}
			}
		})
	}
}

// repeatingReader returns a fixed number of zero bytes from Read calls
// without ever allocating remaining as a slice. Lets us simulate a
// 100+ MiB stream with O(1) memory.
type repeatingReader struct {
	remaining int64
}

func (r *repeatingReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = 0
	}
	r.remaining -= n
	return int(n), nil
}
