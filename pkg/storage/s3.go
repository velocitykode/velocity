package storage

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

// S3Driver implements the Driver interface for AWS S3 storage
type S3Driver struct {
	client     s3iface.S3API
	uploader   *s3manager.Uploader
	downloader *s3manager.Downloader
	bucket     string
	region     string
	url        string
	visibility Visibility
}

// NewS3Driver creates a new S3 storage driver
func NewS3Driver(config DiskConfig) (*S3Driver, error) {
	// Create AWS session
	awsConfig := &aws.Config{
		Region: aws.String(config.Region),
	}

	// Set credentials if provided
	if config.Key != "" && config.Secret != "" {
		awsConfig.Credentials = credentials.NewStaticCredentials(
			config.Key,
			config.Secret,
			"", // token
		)
	}

	sess, err := session.NewSession(awsConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session: %w", err)
	}

	// Create S3 service client
	svc := s3.New(sess)

	// Check if bucket exists
	_, err = svc.HeadBucket(&s3.HeadBucketInput{
		Bucket: aws.String(config.Bucket),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to access bucket %s: %w", config.Bucket, err)
	}

	visibility := Public
	if config.Visibility == "private" {
		visibility = Private
	}

	return &S3Driver{
		client:     svc,
		uploader:   s3manager.NewUploader(sess),
		downloader: s3manager.NewDownloader(sess),
		bucket:     config.Bucket,
		region:     config.Region,
		url:        strings.TrimSuffix(config.URL, "/"),
		visibility: visibility,
	}, nil
}

// Put stores content at the given path
func (d *S3Driver) Put(path string, contents []byte) error {
	reader := bytes.NewReader(contents)
	return d.PutStream(path, reader)
}

// PutStream stores a stream at the given path
func (d *S3Driver) PutStream(path string, stream io.Reader) error {
	path = d.cleanPath(path)

	input := &s3manager.UploadInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
		Body:   stream,
	}

	// Set ACL based on visibility
	if d.visibility == Public {
		input.ACL = aws.String("public-read")
	} else {
		input.ACL = aws.String("private")
	}

	// Detect content type if possible
	if reader, ok := stream.(io.ReadSeeker); ok {
		buffer := make([]byte, 512)
		n, _ := reader.Read(buffer)
		contentType := detectMimeType(buffer[:n])
		input.ContentType = aws.String(contentType)
		reader.Seek(0, 0) // Reset to beginning
	}

	_, err := d.uploader.Upload(input)
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
	path = d.cleanPath(path)

	result, err := d.client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == s3.ErrCodeNoSuchKey {
			return nil, ErrFileNotFound
		}
		return nil, fmt.Errorf("failed to get object from S3: %w", err)
	}

	return result.Body, nil
}

// Exists checks if a file exists at the given path
func (d *S3Driver) Exists(path string) bool {
	path = d.cleanPath(path)

	_, err := d.client.HeadObject(&s3.HeadObjectInput{
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

	// Build delete objects
	objects := make([]*s3.ObjectIdentifier, len(paths))
	for i, path := range paths {
		cleanPath := d.cleanPath(path)
		objects[i] = &s3.ObjectIdentifier{
			Key: aws.String(cleanPath),
		}
	}

	// Delete objects
	_, err := d.client.DeleteObjects(&s3.DeleteObjectsInput{
		Bucket: aws.String(d.bucket),
		Delete: &s3.Delete{
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
	from = d.cleanPath(from)
	to = d.cleanPath(to)

	// Create copy source
	source := fmt.Sprintf("%s/%s", d.bucket, from)

	// Copy object
	_, err := d.client.CopyObject(&s3.CopyObjectInput{
		Bucket:     aws.String(d.bucket),
		CopySource: aws.String(source),
		Key:        aws.String(to),
	})

	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == s3.ErrCodeNoSuchKey {
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
	path = d.cleanPath(path)

	result, err := d.client.HeadObject(&s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	})

	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == s3.ErrCodeNoSuchKey {
			return 0, ErrFileNotFound
		}
		return 0, fmt.Errorf("failed to get object metadata from S3: %w", err)
	}

	return *result.ContentLength, nil
}

// LastModified returns the last modified time of a file
func (d *S3Driver) LastModified(path string) (time.Time, error) {
	path = d.cleanPath(path)

	result, err := d.client.HeadObject(&s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	})

	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == s3.ErrCodeNoSuchKey {
			return time.Time{}, ErrFileNotFound
		}
		return time.Time{}, fmt.Errorf("failed to get object metadata from S3: %w", err)
	}

	return *result.LastModified, nil
}

// MimeType returns the MIME type of a file
func (d *S3Driver) MimeType(path string) (string, error) {
	path = d.cleanPath(path)

	result, err := d.client.HeadObject(&s3.HeadObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	})

	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == s3.ErrCodeNoSuchKey {
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
	directory = d.cleanPath(directory)
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	var files []string

	// List objects with prefix
	input := &s3.ListObjectsV2Input{
		Bucket:    aws.String(d.bucket),
		Prefix:    aws.String(directory),
		Delimiter: aws.String("/"), // Don't recurse
	}

	err := d.client.ListObjectsV2Pages(input, func(page *s3.ListObjectsV2Output, lastPage bool) bool {
		for _, obj := range page.Contents {
			key := *obj.Key
			// Skip the directory itself
			if key != directory {
				files = append(files, key)
			}
		}
		return true
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list objects from S3: %w", err)
	}

	return files, nil
}

// AllFiles lists all files recursively in a directory
func (d *S3Driver) AllFiles(directory string) ([]string, error) {
	directory = d.cleanPath(directory)
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	var files []string

	// List all objects with prefix (no delimiter for recursion)
	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(d.bucket),
		Prefix: aws.String(directory),
	}

	err := d.client.ListObjectsV2Pages(input, func(page *s3.ListObjectsV2Output, lastPage bool) bool {
		for _, obj := range page.Contents {
			key := *obj.Key
			// Skip directories (keys ending with /)
			if !strings.HasSuffix(key, "/") {
				files = append(files, key)
			}
		}
		return true
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list objects from S3: %w", err)
	}

	return files, nil
}

// Directories lists directories
func (d *S3Driver) Directories(directory string) ([]string, error) {
	directory = d.cleanPath(directory)
	if directory != "" && !strings.HasSuffix(directory, "/") {
		directory += "/"
	}

	var dirs []string

	// List objects with delimiter to get "folders"
	input := &s3.ListObjectsV2Input{
		Bucket:    aws.String(d.bucket),
		Prefix:    aws.String(directory),
		Delimiter: aws.String("/"),
	}

	result, err := d.client.ListObjectsV2(input)
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
	path = d.cleanPath(path)

	if d.url != "" {
		// Use custom URL if configured
		return fmt.Sprintf("%s/%s", d.url, path)
	}

	// Generate S3 URL
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", d.bucket, d.region, path)
}

// TemporaryURL returns a temporary URL for a file
func (d *S3Driver) TemporaryURL(path string, expiration time.Duration) (string, error) {
	path = d.cleanPath(path)

	// Generate presigned URL
	req, _ := d.client.GetObjectRequest(&s3.GetObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(path),
	})

	urlStr, err := req.Presign(expiration)
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return urlStr, nil
}

// cleanPath cleans and normalizes a path for S3
func (d *S3Driver) cleanPath(path string) string {
	// Remove leading/trailing slashes
	path = strings.Trim(path, "/")
	// Ensure forward slashes
	path = strings.ReplaceAll(path, "\\", "/")
	return path
}
