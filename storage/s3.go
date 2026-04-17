package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
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

// S3Driver implements the Driver interface for AWS S3 storage
type S3Driver struct {
	client        s3API
	presignClient *s3.PresignClient
	uploader      *manager.Uploader
	downloader    *manager.Downloader
	bucket        string
	region        string
	url           string
	visibility    Visibility
}

// NewS3Driver creates a new S3 storage driver
func NewS3Driver(diskConfig DiskConfig) (*S3Driver, error) {
	ctx := context.Background()

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
		return nil, fmt.Errorf("failed to access bucket %s: %w", diskConfig.Bucket, err)
	}

	visibility := Public
	if diskConfig.Visibility == "private" {
		visibility = Private
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

// Put stores content at the given path
func (d *S3Driver) Put(path string, contents []byte) error {
	reader := bytes.NewReader(contents)
	return d.PutStream(path, reader)
}

// maxS3StreamSize is the default maximum stream size for S3 uploads (100MB)
const maxS3StreamSize = 100 * 1024 * 1024

// PutStream stores a stream at the given path
func (d *S3Driver) PutStream(path string, stream io.Reader) error {
	ctx := context.Background()
	var err error
	path, err = d.cleanPath(path)
	if err != nil {
		return err
	}

	// Limit stream size to prevent unbounded memory usage
	limited := io.LimitReader(stream, maxS3StreamSize+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("failed to read stream: %w", err)
	}
	if int64(len(content)) > maxS3StreamSize {
		return fmt.Errorf("stream exceeds maximum size of %d bytes", maxS3StreamSize)
	}

	input := &s3.PutObjectInput{
		Bucket:      aws.String(d.bucket),
		Key:         aws.String(path),
		Body:        bytes.NewReader(content),
		ContentType: aws.String(detectMimeType(content)),
	}

	// Set ACL based on visibility
	if d.visibility == Public {
		input.ACL = types.ObjectCannedACLPublicRead
	} else {
		input.ACL = types.ObjectCannedACLPrivate
	}

	_, err = d.uploader.Upload(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to upload to S3: %w", err)
	}

	return nil
}

// Get retrieves content from the given path
func (d *S3Driver) Get(path string) ([]byte, error) {
	stream, err := d.GetStream(path)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, stream); err != nil {
		return nil, fmt.Errorf("failed to read stream: %w", err)
	}

	return buf.Bytes(), nil
}

// GetStream retrieves a stream from the given path
func (d *S3Driver) GetStream(path string) (io.ReadCloser, error) {
	ctx := context.Background()
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
		if isNotFoundError(err) {
			return nil, ErrFileNotFound
		}
		return nil, fmt.Errorf("failed to get object from S3: %w", err)
	}

	return result.Body, nil
}

// Exists checks if a file exists at the given path
func (d *S3Driver) Exists(path string) bool {
	ctx := context.Background()
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

// Delete removes files at the given paths
func (d *S3Driver) Delete(paths ...string) error {
	if len(paths) == 0 {
		return nil
	}

	ctx := context.Background()

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
		return fmt.Errorf("failed to delete objects from S3: %w", err)
	}

	return nil
}

// Copy copies a file from one path to another
func (d *S3Driver) Copy(from, to string) error {
	ctx := context.Background()
	var err error
	from, err = d.cleanPath(from)
	if err != nil {
		return err
	}
	to, err = d.cleanPath(to)
	if err != nil {
		return err
	}

	// Create copy source
	source := fmt.Sprintf("%s/%s", d.bucket, from)

	// Copy object
	_, err = d.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(d.bucket),
		CopySource: aws.String(source),
		Key:        aws.String(to),
	})

	if err != nil {
		if isNotFoundError(err) {
			return ErrFileNotFound
		}
		return fmt.Errorf("failed to copy object in S3: %w", err)
	}

	return nil
}

// Move moves a file from one path to another
func (d *S3Driver) Move(from, to string) error {
	// Copy then delete
	if err := d.Copy(from, to); err != nil {
		return err
	}
	return d.Delete(from)
}

// Size returns the size of a file at the given path
func (d *S3Driver) Size(path string) (int64, error) {
	ctx := context.Background()
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
			return 0, ErrFileNotFound
		}
		return 0, fmt.Errorf("failed to get object metadata from S3: %w", err)
	}

	return *result.ContentLength, nil
}

// LastModified returns the last modified time of a file
func (d *S3Driver) LastModified(path string) (time.Time, error) {
	ctx := context.Background()
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
			return time.Time{}, ErrFileNotFound
		}
		return time.Time{}, fmt.Errorf("failed to get object metadata from S3: %w", err)
	}

	return *result.LastModified, nil
}

// MimeType returns the MIME type of a file
func (d *S3Driver) MimeType(path string) (string, error) {
	ctx := context.Background()
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
			return "", ErrFileNotFound
		}
		return "", fmt.Errorf("failed to get object metadata from S3: %w", err)
	}

	if result.ContentType != nil {
		return *result.ContentType, nil
	}

	return "application/octet-stream", nil
}

// Files lists files in a directory
func (d *S3Driver) Files(directory string) ([]string, error) {
	ctx := context.Background()
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
			return nil, fmt.Errorf("failed to list objects from S3: %w", err)
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

// AllFiles lists all files recursively in a directory
func (d *S3Driver) AllFiles(directory string) ([]string, error) {
	ctx := context.Background()
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
			return nil, fmt.Errorf("failed to list objects from S3: %w", err)
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

// Directories lists directories
func (d *S3Driver) Directories(directory string) ([]string, error) {
	ctx := context.Background()
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
		return nil, fmt.Errorf("failed to list objects from S3: %w", err)
	}

	// Common prefixes represent "directories"
	for _, prefix := range result.CommonPrefixes {
		dir := *prefix.Prefix
		// Remove trailing slash for consistency
		dirs = append(dirs, strings.TrimSuffix(dir, "/"))
	}

	return dirs, nil
}

// AllDirectories lists all directories recursively
func (d *S3Driver) AllDirectories(directory string) ([]string, error) {
	// S3 doesn't have real directories, we need to infer from object keys
	allFiles, err := d.AllFiles(directory)
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

// MakeDirectory creates a directory (no-op for S3)
func (d *S3Driver) MakeDirectory(path string) error {
	// S3 doesn't have real directories
	// Some tools create zero-byte objects with trailing slash
	// We'll do nothing here as directories are implicit
	return nil
}

// DeleteDirectory deletes a directory and all its contents
func (d *S3Driver) DeleteDirectory(directory string) error {
	// List all files in directory
	files, err := d.AllFiles(directory)
	if err != nil {
		return err
	}

	// Delete all files
	if len(files) > 0 {
		return d.Delete(files...)
	}

	return nil
}

// URL returns the public URL for a file
func (d *S3Driver) URL(path string) string {
	var err error
	path, err = d.cleanPath(path)
	if err != nil {
		return ""
	}

	if d.url != "" {
		// Use custom URL if configured
		return fmt.Sprintf("%s/%s", d.url, path)
	}

	// Generate S3 URL
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", d.bucket, d.region, path)
}

// TemporaryURL returns a temporary URL for a file
func (d *S3Driver) TemporaryURL(path string, expiration time.Duration) (string, error) {
	ctx := context.Background()
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
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
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
			return "", fmt.Errorf("path traversal detected")
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
