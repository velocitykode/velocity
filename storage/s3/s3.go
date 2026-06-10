package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/velocitykode/velocity/storage"
)

// s3API defines the S3 operations used by the driver.
// *s3.Client satisfies this interface.
type s3API interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObjects(ctx context.Context, params *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	CopyObject(ctx context.Context, params *s3.CopyObjectInput, optFns ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// maxS3PresignExpiration caps S3 presigned URL lifetimes at 7 days, the maximum
// supported by AWS Signature V4 (RFC 3339 / AWS signing spec).
const maxS3PresignExpiration = 7 * 24 * time.Hour

func init() {
	storage.Drivers().Register("s3", func(ctx context.Context, cfg storage.DiskConfig) (storage.Driver, error) {
		return NewS3DriverWithContext(ctx, cfg)
	})
}

// New constructs an S3 driver from cfg for standalone use without going
// through the storage driver registry. It returns the same driver the
// registry path produces ("s3" factory), so both routes are equivalent.
func New(cfg storage.DiskConfig) (storage.Driver, error) {
	return NewS3DriverWithContext(context.Background(), cfg)
}

// ErrExpirationTooLong is returned by TemporaryURL / TemporaryURLCtx when
// the requested expiration exceeds AWS SigV4's 7-day cap. Callers that
// previously relied on silent clamping should cap their own values; the
// framework no longer sign URLs that are guaranteed to expire sooner than
// the caller requested.
var ErrExpirationTooLong = errors.New("velocity/storage: temporary url expiration exceeds aws sigv4 7-day cap")

// ErrExpirationNonPositive is returned when the requested expiration is
// zero or negative — signing an already-expired URL is almost certainly a
// bug, not intent.
var ErrExpirationNonPositive = errors.New("velocity/storage: temporary url expiration must be positive")

// S3Driver implements the Driver interface for AWS S3 storage.
//
// Methods defined by the Driver interface use context.Background() internally.
// Use the *Ctx variants (PutCtx, GetCtx, etc.) to plumb a caller-provided
// context through AWS SDK calls for cancellation and deadline propagation.
type S3Driver struct {
	client        s3API
	presignClient *s3.PresignClient
	uploader      *manager.Uploader
	downloader    *manager.Downloader
	bucket        string
	region        string
	url           string
	visibility    storage.Visibility
}

// NewS3Driver creates a new S3 storage driver.
//
// Configuration is validated up front: Region and Bucket are required,
// and any non-empty URL must parse as an http(s) endpoint.
// Visibility defaults to Private; Public must be opted in explicitly
// by setting DiskConfig.Visibility == "public".
func NewS3Driver(diskConfig storage.DiskConfig) (*S3Driver, error) {
	return NewS3DriverWithContext(context.Background(), diskConfig)
}

// NewS3DriverWithContext is the context-aware variant of NewS3Driver.
// The provided context governs AWS config loading and the HeadBucket call.
func NewS3DriverWithContext(ctx context.Context, diskConfig storage.DiskConfig) (*S3Driver, error) {
	if strings.TrimSpace(diskConfig.Region) == "" {
		return nil, fmt.Errorf("velocity/storage: s3 region is required")
	}
	if strings.TrimSpace(diskConfig.Bucket) == "" {
		return nil, fmt.Errorf("velocity/storage: s3 bucket is required")
	}

	if diskConfig.URL != "" {
		u, err := url.Parse(diskConfig.URL)
		if err != nil {
			return nil, fmt.Errorf("velocity/storage: s3 url is invalid: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("velocity/storage: s3 url must use http or https scheme, got %q", u.Scheme)
		}
	}

	// Build config options
	var opts []func(*config.LoadOptions) error
	opts = append(opts, config.WithRegion(diskConfig.Region))

	// Set credentials if provided
	if diskConfig.Key != "" && diskConfig.Secret != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(diskConfig.Key, diskConfig.Secret, ""),
		))
	}

	// Load AWS config
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("velocity/storage: failed to load aws config: %w", err)
	}

	// Create S3 client
	client := s3.NewFromConfig(cfg)

	// Check if bucket exists
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(diskConfig.Bucket),
	})
	if err != nil {
		return nil, fmt.Errorf("velocity/storage: failed to access bucket %s: %w", diskConfig.Bucket, err)
	}

	// Default to Private. Public visibility must be opt-in.
	visibility := storage.Private
	if diskConfig.Visibility == string(storage.Public) {
		visibility = storage.Public
	}

	return &S3Driver{
		client:        client,
		presignClient: s3.NewPresignClient(client),
		uploader:      manager.NewUploader(client),
		downloader:    manager.NewDownloader(client),
		bucket:        diskConfig.Bucket,
		region:        diskConfig.Region,
		url:           strings.TrimSuffix(diskConfig.URL, "/"),
		visibility:    visibility,
	}, nil
}

// Put stores content at the given path.
//
// Deprecated: use PutCtx with a request-scoped context.Context.
func (d *S3Driver) Put(path string, contents []byte) error {
	return d.PutCtx(context.Background(), path, contents)
}

// PutCtx stores content at the given path using the caller-provided context.
func (d *S3Driver) PutCtx(ctx context.Context, path string, contents []byte) error {
	return d.PutStreamCtx(ctx, path, bytes.NewReader(contents))
}

// maxS3StreamSize is the default maximum stream size for S3 uploads (100MB)
const maxS3StreamSize = 100 * 1024 * 1024

// mimeSniffSize is the number of bytes net/http sniffs to detect a
// Content-Type. We pre-buffer this many bytes off the source stream so
// detectMimeType keeps working without forcing us to load the entire
// upload into memory first.
const mimeSniffSize = 512

const safePublicContentType = "application/octet-stream"

var publicAttachmentContentTypePrefixes = []string{
	"text/html",
	"image/svg+xml",
	"application/xhtml+xml",
}

// ErrStreamTooLarge is returned by PutStream / PutStreamCtx when the
// uploaded body would exceed maxS3StreamSize. Surfacing this as a typed
// sentinel lets callers distinguish "client uploaded too much" from
// generic SDK transport errors.
var ErrStreamTooLarge = errors.New("velocity/storage: stream exceeds maximum size")

// PutStream stores a stream at the given path.
//
// Deprecated: use PutStreamCtx with a request-scoped context.Context.
func (d *S3Driver) PutStream(path string, stream io.Reader) error {
	return d.PutStreamCtx(context.Background(), path, stream)
}

// PutStreamCtx stores a stream at the given path using the caller-provided
// context.
//
// The body is streamed to S3 in chunks via manager.Uploader so peak memory
// stays close to the SDK's default part size (5 MiB) rather than the full
// upload (up to 100 MiB). MIME detection sniffs the first 512 bytes off the
// front of the stream, which are then prepended back via io.MultiReader so
// the SDK still sees the complete body. A cap-enforcing wrapper aborts the
// upload with ErrStreamTooLarge if the source exceeds maxS3StreamSize: this
// surfaces back through manager.Uploader as a wrapped read error and the
// partial multipart upload (if any) is cleaned up by the SDK's standard
// abort-on-error behaviour.
func (d *S3Driver) PutStreamCtx(ctx context.Context, path string, stream io.Reader) error {
	var err error
	path, err = d.cleanPath(path)
	if err != nil {
		return err
	}

	// Sniff the leading bytes for MIME detection without buffering the
	// whole upload. io.ReadFull lets us tolerate short streams (an upload
	// smaller than mimeSniffSize is still valid; we just sniff whatever
	// we got).
	sniff := make([]byte, mimeSniffSize)
	n, err := io.ReadFull(stream, sniff)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("velocity/storage: failed to read stream: %w", err)
	}
	sniff = sniff[:n]
	contentType := detectMimeType(sniff)
	var contentDisposition *string

	// Re-attach the sniffed bytes to the front of the remaining stream
	// and wrap with a cap-enforcing reader sized to maxS3StreamSize+1.
	// The +1 byte lets us distinguish "exactly at cap" (still fine) from
	// "over cap" (abort): the cap reader returns ErrStreamTooLarge on
	// the read that crosses the boundary, which surfaces back through
	// the AWS uploader as an upload error.
	body := io.MultiReader(bytes.NewReader(sniff), stream)
	capped := &capReader{r: body, max: maxS3StreamSize}

	// Set ACL based on visibility. Private uploads also get
	// Content-Disposition: attachment so that any presigned GET or
	// proxy serve path defaults to download semantics rather than
	// inline rendering, defanging stored-XSS via Content-Type
	// spoofing for non-image payloads.
	//
	// Public uploads preserve the detected Content-Type for ordinary
	// inline-safe objects such as images and plain text. Renderable
	// browser document types are stored as application/octet-stream and
	// forced to attachment so the public S3/CloudFront path does not
	// execute attacker-controlled HTML, SVG, or XHTML in the bucket
	// origin.
	//
	// X-Content-Type-Options: nosniff CANNOT be persisted on the
	// object: the S3 PutObject API only honours a fixed set of
	// canonical response-header overrides (Cache-Control,
	// Content-Disposition, Content-Encoding, Content-Language,
	// Content-Type, Expires). Storing the directive as user metadata
	// would surface it on retrieval as the unrelated header
	// X-Amz-Meta-X-Content-Type-Options, which browsers do not honour
	// for MIME-sniffing protection. Real X-Content-Type-Options
	// enforcement therefore MUST live at the serve layer (the
	// application's HTTP handler, a reverse proxy, or a CloudFront
	// response-headers policy in front of the bucket).
	if d.visibility == storage.Public {
		if publicContentTypeRequiresAttachment(contentType) {
			contentType = safePublicContentType
			contentDisposition = aws.String("attachment")
		}
	} else {
		contentDisposition = aws.String("attachment")
	}

	input := &s3.PutObjectInput{
		Bucket:             aws.String(d.bucket),
		Key:                aws.String(path),
		Body:               capped,
		ContentType:        aws.String(contentType),
		ContentDisposition: contentDisposition,
	}

	if d.visibility == storage.Public {
		input.ACL = types.ObjectCannedACLPublicRead
	} else {
		input.ACL = types.ObjectCannedACLPrivate
	}

	_, err = d.uploader.Upload(ctx, input)
	if err != nil {
		// Surface ErrStreamTooLarge cleanly even when the AWS SDK has
		// wrapped it in its own error type. The cap reader emits the
		// sentinel from Read; the uploader's chunker propagates it
		// through one or more layers of wrapping.
		if errors.Is(err, ErrStreamTooLarge) {
			return ErrStreamTooLarge
		}
		return fmt.Errorf("velocity/storage: failed to upload to s3: %w", err)
	}

	return nil
}

// capReader returns ErrStreamTooLarge from Read once the source has
// produced more than max bytes. The wrapper is used to enforce an
// upload size cap WITHOUT buffering the entire upload in memory: the
// AWS uploader streams chunks through Read, the wrapper counts as it
// goes, and the read that crosses the boundary returns the sentinel
// error. The uploader propagates the error back to its caller and (for
// multipart uploads) issues an AbortMultipartUpload internally.
//
// Implementation reads from the source unrestricted but trims any
// returned slice that would push the running total past max+1. The
// running total going strictly above max trips the sentinel on the
// SAME Read call so the uploader sees the failure immediately.
type capReader struct {
	r      io.Reader
	max    int64
	read   int64
	tipped bool
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.tipped {
		return 0, ErrStreamTooLarge
	}
	// Allow reading exactly one byte past the cap so we can detect
	// overflow without trapping a stream that is exactly max bytes.
	allowed := c.max + 1 - c.read
	if allowed <= 0 {
		c.tipped = true
		return 0, ErrStreamTooLarge
	}
	if int64(len(p)) > allowed {
		p = p[:allowed]
	}
	n, err := c.r.Read(p)
	c.read += int64(n)
	if c.read > c.max {
		c.tipped = true
		// Hand back the bytes that fit under the cap so the caller's
		// running buffer is consistent, then surface the cap error.
		over := c.read - c.max
		if int64(n) >= over {
			n -= int(over)
		} else {
			n = 0
		}
		return n, ErrStreamTooLarge
	}
	return n, err
}

// Get retrieves content from the given path.
//
// Deprecated: use GetCtx with a request-scoped context.Context.
func (d *S3Driver) Get(path string) ([]byte, error) {
	return d.GetCtx(context.Background(), path)
}

// GetCtx retrieves content using the caller-provided context.
func (d *S3Driver) GetCtx(ctx context.Context, path string) ([]byte, error) {
	stream, err := d.GetStreamCtx(ctx, path)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, stream); err != nil {
		return nil, fmt.Errorf("velocity/storage: failed to read stream: %w", err)
	}

	return buf.Bytes(), nil
}

// GetStream retrieves a stream from the given path.
//
// Deprecated: use GetStreamCtx with a request-scoped context.Context.
func (d *S3Driver) GetStream(path string) (io.ReadCloser, error) {
	return d.GetStreamCtx(context.Background(), path)
}

// GetStreamCtx retrieves a stream using the caller-provided context.
func (d *S3Driver) GetStreamCtx(ctx context.Context, path string) (io.ReadCloser, error) {
	var err error
	path, err = d.cleanPath(path)
	if err != nil {
		return nil, err
	}

	result, err := d.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if isNotFoundError(err) {
			return nil, storage.ErrFileNotFound
		}
		return nil, fmt.Errorf("velocity/storage: failed to get object from s3: %w", err)
	}

	return result.Body, nil
}

// Exists checks if a file exists at the given path.
//
// Deprecated: use ExistsCtx with a request-scoped context.Context.
func (d *S3Driver) Exists(path string) bool {
	return d.ExistsCtx(context.Background(), path)
}

// ExistsCtx checks existence using the caller-provided context.
func (d *S3Driver) ExistsCtx(ctx context.Context, path string) bool {
	var err error
	path, err = d.cleanPath(path)
	if err != nil {
		return false
	}

	_, err = d.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	})

	return err == nil
}

// Delete removes files at the given paths.
//
// Deprecated: use DeleteCtx with a request-scoped context.Context.
func (d *S3Driver) Delete(paths ...string) error {
	return d.DeleteCtx(context.Background(), paths...)
}

// DeleteCtx removes files using the caller-provided context.
func (d *S3Driver) DeleteCtx(ctx context.Context, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}

	// Build delete objects
	objects := make([]types.ObjectIdentifier, len(paths))
	for i, path := range paths {
		cleanPath, err := d.cleanPath(path)
		if err != nil {
			return err
		}
		objects[i] = types.ObjectIdentifier{
			Key: aws.String(cleanPath),
		}
	}

	// Delete objects
	_, err := d.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(d.bucket),
		Delete: &types.Delete{
			Objects: objects,
			Quiet:   aws.Bool(true),
		},
	})

	if err != nil {
		return fmt.Errorf("velocity/storage: failed to delete objects from s3: %w", err)
	}

	return nil
}

// Copy copies a file from one path to another.
//
// Deprecated: use CopyCtx with a request-scoped context.Context.
func (d *S3Driver) Copy(from, to string) error {
	return d.CopyCtx(context.Background(), from, to)
}

// CopyCtx copies a file using the caller-provided context.
func (d *S3Driver) CopyCtx(ctx context.Context, from, to string) error {
	var err error
	from, err = d.cleanPath(from)
	if err != nil {
		return err
	}
	to, err = d.cleanPath(to)
	if err != nil {
		return err
	}

	// Create copy source. AWS requires CopySource to be URL-encoded;
	// encode the key per path segment (bucket names cannot contain
	// characters that need encoding).
	source := fmt.Sprintf("%s/%s", d.bucket, escapeURLPathSegments(from))

	// Copy object
	_, err = d.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(d.bucket),
		CopySource: aws.String(source),
		Key:        aws.String(to),
	})

	if err != nil {
		if isNotFoundError(err) {
			return storage.ErrFileNotFound
		}
		return fmt.Errorf("velocity/storage: failed to copy object in s3: %w", err)
	}

	return nil
}

// Move moves a file from one path to another.
//
// Deprecated: use MoveCtx with a request-scoped context.Context.
func (d *S3Driver) Move(from, to string) error {
	return d.MoveCtx(context.Background(), from, to)
}

// MoveCtx moves a file using the caller-provided context.
func (d *S3Driver) MoveCtx(ctx context.Context, from, to string) error {
	if err := d.CopyCtx(ctx, from, to); err != nil {
		return err
	}
	return d.DeleteCtx(ctx, from)
}

// Size returns the size of a file at the given path.
//
// Deprecated: use SizeCtx with a request-scoped context.Context.
func (d *S3Driver) Size(path string) (int64, error) {
	return d.SizeCtx(context.Background(), path)
}

// SizeCtx returns the size using the caller-provided context.
func (d *S3Driver) SizeCtx(ctx context.Context, path string) (int64, error) {
	var err error
	path, err = d.cleanPath(path)
	if err != nil {
		return 0, err
	}

	result, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	})

	if err != nil {
		if isNotFoundError(err) {
			return 0, storage.ErrFileNotFound
		}
		return 0, fmt.Errorf("velocity/storage: failed to get object metadata from s3: %w", err)
	}

	return *result.ContentLength, nil
}

// LastModified returns the last modified time of a file.
//
// Deprecated: use LastModifiedCtx with a request-scoped context.Context.
func (d *S3Driver) LastModified(path string) (time.Time, error) {
	return d.LastModifiedCtx(context.Background(), path)
}

// LastModifiedCtx returns the last modified time using the caller-provided context.
func (d *S3Driver) LastModifiedCtx(ctx context.Context, path string) (time.Time, error) {
	var err error
	path, err = d.cleanPath(path)
	if err != nil {
		return time.Time{}, err
	}

	result, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	})

	if err != nil {
		if isNotFoundError(err) {
			return time.Time{}, storage.ErrFileNotFound
		}
		return time.Time{}, fmt.Errorf("velocity/storage: failed to get object metadata from s3: %w", err)
	}

	return *result.LastModified, nil
}

// MimeType returns the MIME type of a file.
//
// Deprecated: use MimeTypeCtx with a request-scoped context.Context.
func (d *S3Driver) MimeType(path string) (string, error) {
	return d.MimeTypeCtx(context.Background(), path)
}

// MimeTypeCtx returns the MIME type using the caller-provided context.
func (d *S3Driver) MimeTypeCtx(ctx context.Context, path string) (string, error) {
	var err error
	path, err = d.cleanPath(path)
	if err != nil {
		return "", err
	}

	result, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	})

	if err != nil {
		if isNotFoundError(err) {
			return "", storage.ErrFileNotFound
		}
		return "", fmt.Errorf("velocity/storage: failed to get object metadata from s3: %w", err)
	}

	if result.ContentType != nil {
		return *result.ContentType, nil
	}

	return "application/octet-stream", nil
}

// Files lists files in a directory.
//
// Deprecated: use FilesCtx with a request-scoped context.Context.
func (d *S3Driver) Files(directory string) ([]string, error) {
	return d.FilesCtx(context.Background(), directory)
}

// FilesCtx lists files in a directory using the caller-provided context.
func (d *S3Driver) FilesCtx(ctx context.Context, directory string) ([]string, error) {
	var err error
	directory, err = d.cleanPath(directory)
	if err != nil {
		return nil, err
	}
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	var files []string

	// List objects with prefix
	paginator := s3.NewListObjectsV2Paginator(d.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(d.bucket),
		Prefix:    aws.String(directory),
		Delimiter: aws.String("/"), // Don't recurse
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("velocity/storage: failed to list objects from s3: %w", err)
		}

		for _, obj := range page.Contents {
			key := *obj.Key
			// Skip the directory itself
			if key != directory {
				files = append(files, key)
			}
		}
	}

	return files, nil
}

// AllFiles lists all files recursively in a directory.
//
// Deprecated: use AllFilesCtx with a request-scoped context.Context.
func (d *S3Driver) AllFiles(directory string) ([]string, error) {
	return d.AllFilesCtx(context.Background(), directory)
}

// AllFilesCtx lists all files recursively using the caller-provided context.
func (d *S3Driver) AllFilesCtx(ctx context.Context, directory string) ([]string, error) {
	var err error
	directory, err = d.cleanPath(directory)
	if err != nil {
		return nil, err
	}
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	var files []string

	// List all objects with prefix (no delimiter for recursion)
	paginator := s3.NewListObjectsV2Paginator(d.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(d.bucket),
		Prefix: aws.String(directory),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("velocity/storage: failed to list objects from s3: %w", err)
		}

		for _, obj := range page.Contents {
			key := *obj.Key
			// Skip directories (keys ending with /)
			if !strings.HasSuffix(key, "/") {
				files = append(files, key)
			}
		}
	}

	return files, nil
}

// Directories lists directories.
//
// Deprecated: use DirectoriesCtx with a request-scoped context.Context.
func (d *S3Driver) Directories(directory string) ([]string, error) {
	return d.DirectoriesCtx(context.Background(), directory)
}

// DirectoriesCtx lists directories using the caller-provided context.
func (d *S3Driver) DirectoriesCtx(ctx context.Context, directory string) ([]string, error) {
	var err error
	directory, err = d.cleanPath(directory)
	if err != nil {
		return nil, err
	}
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	var dirs []string

	// List objects with delimiter to get "folders"
	result, err := d.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:    aws.String(d.bucket),
		Prefix:    aws.String(directory),
		Delimiter: aws.String("/"),
	})
	if err != nil {
		return nil, fmt.Errorf("velocity/storage: failed to list objects from s3: %w", err)
	}

	// Common prefixes represent "directories"
	for _, prefix := range result.CommonPrefixes {
		dir := *prefix.Prefix
		// Remove trailing slash for consistency
		dirs = append(dirs, strings.TrimSuffix(dir, "/"))
	}

	return dirs, nil
}

// AllDirectories lists all directories recursively.
//
// Deprecated: use AllDirectoriesCtx with a request-scoped context.Context.
func (d *S3Driver) AllDirectories(directory string) ([]string, error) {
	return d.AllDirectoriesCtx(context.Background(), directory)
}

// AllDirectoriesCtx lists all directories recursively using the caller-provided context.
func (d *S3Driver) AllDirectoriesCtx(ctx context.Context, directory string) ([]string, error) {
	// S3 doesn't have real directories, we need to infer from object keys
	allFiles, err := d.AllFilesCtx(ctx, directory)
	if err != nil {
		return nil, err
	}

	// Extract unique directories from file paths
	dirMap := make(map[string]bool)
	for _, file := range allFiles {
		dir := file
		for {
			dir = strings.TrimSuffix(dir, "/")
			lastSlash := strings.LastIndex(dir, "/")
			if lastSlash <= 0 {
				break
			}
			dir = dir[:lastSlash]
			if directory != "" && !strings.HasPrefix(dir, directory) {
				break
			}
			dirMap[dir] = true
		}
	}

	// Convert map to slice
	dirs := make([]string, 0, len(dirMap))
	for dir := range dirMap {
		dirs = append(dirs, dir)
	}

	return dirs, nil
}

// MakeDirectory creates a directory (no-op for S3).
//
// Deprecated: use MakeDirectoryCtx with a request-scoped context.Context.
func (d *S3Driver) MakeDirectory(path string) error {
	return d.MakeDirectoryCtx(context.Background(), path)
}

// MakeDirectoryCtx is the ctx-aware variant of MakeDirectory. S3 does not
// have real directories, so this is a no-op that only honours pre-flight
// context cancellation for interface uniformity.
func (d *S3Driver) MakeDirectoryCtx(ctx context.Context, path string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// DeleteDirectory deletes a directory and all its contents.
//
// Deprecated: use DeleteDirectoryCtx with a request-scoped context.Context.
func (d *S3Driver) DeleteDirectory(directory string) error {
	return d.DeleteDirectoryCtx(context.Background(), directory)
}

// DeleteDirectoryCtx deletes a directory using the caller-provided context.
func (d *S3Driver) DeleteDirectoryCtx(ctx context.Context, directory string) error {
	// List all files in directory
	files, err := d.AllFilesCtx(ctx, directory)
	if err != nil {
		return err
	}

	// Delete all files
	if len(files) > 0 {
		return d.DeleteCtx(ctx, files...)
	}

	return nil
}

// URL returns the public URL for a file.
//
// Each path segment is URL-escaped independently so reserved
// characters (`?`, `#`, space, `%`, ...) inside an object key cannot
// inject query strings or fragments into the emitted URL. The literal
// `/` between segments is preserved. Applies to both the custom-URL
// branch and the synthesised `s3.<region>.amazonaws.com` branch.
func (d *S3Driver) URL(path string) string {
	var err error
	path, err = d.cleanPath(path)
	if err != nil {
		return ""
	}

	escaped := escapeURLPathSegments(path)
	if d.url != "" {
		// Use custom URL if configured
		return d.url + "/" + escaped
	}

	// Generate S3 URL
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", d.bucket, d.region, escaped)
}

// TemporaryURL returns a temporary presigned URL for a file.
// Returns ErrExpirationTooLong if expiration > 7 days (AWS SigV4 cap) or
// ErrExpirationNonPositive if expiration <= 0. Operators who previously
// passed 30-day durations and relied on silent 7-day clamping now get a
// loud failure: cap the value at your call site before invoking.
//
// Deprecated: use TemporaryURLCtx with a request-scoped context.Context.
func (d *S3Driver) TemporaryURL(path string, expiration time.Duration) (string, error) {
	return d.TemporaryURLCtx(context.Background(), path, expiration)
}

// TemporaryURLCtx returns a temporary presigned URL using the caller-provided context.
// See TemporaryURL for error semantics.
func (d *S3Driver) TemporaryURLCtx(ctx context.Context, path string, expiration time.Duration) (string, error) {
	if expiration <= 0 {
		return "", ErrExpirationNonPositive
	}
	if expiration > maxS3PresignExpiration {
		return "", ErrExpirationTooLong
	}

	var err error
	path, err = d.cleanPath(path)
	if err != nil {
		return "", err
	}

	// Generate presigned URL
	result, err := d.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	}, s3.WithPresignExpires(expiration))

	if err != nil {
		return "", fmt.Errorf("velocity/storage: failed to generate presigned url: %w", err)
	}

	return result.URL, nil
}

// cleanPath cleans and normalizes a path for S3.
// Rejects paths containing ".." components to prevent path traversal.
func (d *S3Driver) cleanPath(path string) (string, error) {
	// Remove leading/trailing slashes
	path = strings.Trim(path, "/")
	// Ensure forward slashes
	path = strings.ReplaceAll(path, "\\", "/")

	// Reject path traversal
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return "", fmt.Errorf("velocity/storage: path traversal detected")
		}
	}

	return path, nil
}

// isNotFoundError checks if an error is a "not found" error
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "NotFound") ||
		strings.Contains(err.Error(), "NoSuchKey")
}

// detectMimeType detects the MIME type from content using the standard
// library sniffer (net/http.DetectContentType), with a narrow SVG
// correction for markup the sniffer classifies as plain text. Sniffing
// only the first 512 bytes mirrors http.DetectContentType's own contract
// and bounds the work for very large objects.
func detectMimeType(content []byte) string {
	if len(content) == 0 {
		return "application/octet-stream"
	}
	n := len(content)
	if n > 512 {
		n = 512
	}
	sample := content[:n]
	contentType := http.DetectContentType(sample)
	if strings.HasPrefix(strings.ToLower(contentType), "text/plain") && looksLikeSVG(sample) {
		return "image/svg+xml"
	}
	return contentType
}

func looksLikeSVG(content []byte) bool {
	s := strings.TrimSpace(strings.ToLower(string(content)))
	return strings.HasPrefix(s, "<svg") ||
		(strings.HasPrefix(s, "<?xml") && strings.Contains(s, "<svg"))
}

func publicContentTypeRequiresAttachment(contentType string) bool {
	contentType = strings.ToLower(contentType)
	for _, prefix := range publicAttachmentContentTypePrefixes {
		if strings.HasPrefix(contentType, prefix) {
			return true
		}
	}
	return false
}

// escapeURLPathSegments percent-encodes each `/`-delimited segment of
// path so reserved characters in keys cannot inject query / fragment
// state. `url.PathEscape` does NOT escape `/`, so a blanket call would
// destroy the separators; splitting first preserves the path shape
// while encoding every segment individually.
func escapeURLPathSegments(path string) string {
	if path == "" {
		return ""
	}
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return strings.Join(segs, "/")
}
