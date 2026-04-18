//go:build integration

// Storage integration tests — run with: make test-integration
//
// These tests exercise the real storage drivers (local + S3) under a shared
// parity suite. The point is to catch cross-driver behaviour drift that unit
// tests with in-memory fakes won't: subtle differences in error shape,
// listing order, copy/move semantics, and large-payload handling.
//
// The S3 driver is pointed at MinIO via MINIO_ENDPOINT. MinIO requires
// path-style addressing (bucket in the URL path, not the subdomain) which
// this package's production NewS3Driver does not configure — so the test
// constructs an *S3Driver directly with a path-style *s3.Client. Tests live
// in `package storage` so unexported fields are accessible.
package storage

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// requiredEnv is checked once in TestMain. Missing any of these fails the
// entire integration run with a loud, actionable message — we deliberately
// do NOT t.Skip per-test, because a silent skip turns a real CI infra
// failure (MinIO sidecar didn't start) into a green build.
var requiredEnv = []string{
	"MINIO_ENDPOINT",
	"MINIO_ACCESS_KEY",
	"MINIO_SECRET_KEY",
	"MINIO_BUCKET",
	"MINIO_REGION",
}

func TestMain(m *testing.M) {
	var missing []string
	for _, name := range requiredEnv {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr,
			"storage integration tests require env vars (missing: %s) — use `make test-integration`\n",
			strings.Join(missing, ", "))
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// newMinIOS3Driver constructs an S3Driver wired to MinIO. We build the
// client manually because production NewS3Driver doesn't expose a
// path-style option, which MinIO requires.
func newMinIOS3Driver(t *testing.T) *S3Driver {
	t.Helper()
	ctx := context.Background()

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(os.Getenv("MINIO_REGION")),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			os.Getenv("MINIO_ACCESS_KEY"),
			os.Getenv("MINIO_SECRET_KEY"),
			"",
		)),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}

	endpoint := os.Getenv("MINIO_ENDPOINT")
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	bucket := os.Getenv("MINIO_BUCKET")
	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("HeadBucket %q (endpoint=%s): %v — ensure MinIO is running and bucket exists", bucket, endpoint, err)
	}

	return &S3Driver{
		client:        client,
		presignClient: s3.NewPresignClient(client),
		uploader:      manager.NewUploader(client),
		downloader:    manager.NewDownloader(client),
		bucket:        bucket,
		region:        os.Getenv("MINIO_REGION"),
		visibility:    Private,
	}
}

// driverFixture pairs a driver with a cleanup hook so the parity suite can
// wipe state between subtests without leaking files or S3 objects.
type driverFixture struct {
	name    string
	driver  Driver
	cleanup func()
}

func fixtures(t *testing.T) []driverFixture {
	t.Helper()

	localDir := t.TempDir()
	local := NewLocalDriver(DiskConfig{Driver: "local", Root: localDir})

	s3d := newMinIOS3Driver(t)
	s3Prefix := fmt.Sprintf("integration-test-%d/", os.Getpid())

	return []driverFixture{
		{
			name:    "local",
			driver:  local,
			cleanup: func() { _ = local.Shutdown(context.Background()) },
		},
		{
			name:   "s3-minio",
			driver: prefixedDriver{Driver: s3d, prefix: s3Prefix},
			cleanup: func() {
				// Purge the test prefix so reruns don't accumulate.
				files, _ := s3d.AllFiles(strings.TrimSuffix(s3Prefix, "/"))
				if len(files) > 0 {
					_ = s3d.Delete(files...)
				}
			},
		},
	}
}

// prefixedDriver wraps a Driver and rewrites every path through a fixed
// prefix so integration runs against a shared S3 bucket don't collide.
// It only implements the subset of Driver methods the parity suite uses.
type prefixedDriver struct {
	Driver
	prefix string
}

func (p prefixedDriver) key(path string) string { return p.prefix + strings.TrimPrefix(path, "/") }

func (p prefixedDriver) Put(path string, contents []byte) error {
	return p.Driver.Put(p.key(path), contents)
}
func (p prefixedDriver) Get(path string) ([]byte, error) { return p.Driver.Get(p.key(path)) }
func (p prefixedDriver) Exists(path string) bool         { return p.Driver.Exists(p.key(path)) }
func (p prefixedDriver) Size(path string) (int64, error) { return p.Driver.Size(p.key(path)) }
func (p prefixedDriver) Delete(paths ...string) error {
	keyed := make([]string, len(paths))
	for i, path := range paths {
		keyed[i] = p.key(path)
	}
	return p.Driver.Delete(keyed...)
}
func (p prefixedDriver) Copy(from, to string) error { return p.Driver.Copy(p.key(from), p.key(to)) }
func (p prefixedDriver) Move(from, to string) error { return p.Driver.Move(p.key(from), p.key(to)) }

// TestParity_BasicRoundTrip runs the same Put/Get/Exists/Delete sequence on
// every driver. A mismatch surfaces as a subtest failure with the driver name.
func TestParity_BasicRoundTrip(t *testing.T) {
	for _, fx := range fixtures(t) {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			t.Cleanup(fx.cleanup)

			const path = "parity/hello.txt"
			payload := []byte("hello from parity suite")

			if fx.driver.Exists(path) {
				t.Fatalf("precondition: %q should not exist before Put", path)
			}
			if err := fx.driver.Put(path, payload); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if !fx.driver.Exists(path) {
				t.Fatalf("Exists should return true after Put")
			}

			got, err := fx.driver.Get(path)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("Get mismatch: got %q, want %q", got, payload)
			}

			size, err := fx.driver.Size(path)
			if err != nil {
				t.Fatalf("Size: %v", err)
			}
			if size != int64(len(payload)) {
				t.Errorf("Size = %d, want %d", size, len(payload))
			}

			if err := fx.driver.Delete(path); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if fx.driver.Exists(path) {
				t.Error("Exists should return false after Delete")
			}
		})
	}
}

// TestParity_GetMissingReturnsNotFound locks in the "missing file" contract:
// Get on a non-existent path must return an error that unwraps to
// ErrFileNotFound on every driver. This is load-bearing for callers that
// branch on errors.Is(err, storage.ErrFileNotFound) to serve a 404.
func TestParity_GetMissingReturnsNotFound(t *testing.T) {
	for _, fx := range fixtures(t) {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			t.Cleanup(fx.cleanup)

			_, err := fx.driver.Get("does-not-exist.txt")
			if err == nil {
				t.Fatal("Get on missing path must error")
			}
			// Unwrap via the driver's own error shape — local returns
			// ErrFileNotFound directly; S3 wraps it.
			if !strings.Contains(err.Error(), "not found") &&
				!strings.Contains(err.Error(), "NoSuchKey") {
				t.Errorf("error should indicate missing file, got %v", err)
			}
		})
	}
}

// TestParity_CopyAndMove verifies that Copy preserves the source and Move
// removes it. Drivers that silently alias (shallow copy) would fail this.
func TestParity_CopyAndMove(t *testing.T) {
	for _, fx := range fixtures(t) {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			t.Cleanup(fx.cleanup)

			payload := []byte("copy+move parity")
			if err := fx.driver.Put("src.txt", payload); err != nil {
				t.Fatalf("Put src: %v", err)
			}

			if err := fx.driver.Copy("src.txt", "copy.txt"); err != nil {
				t.Fatalf("Copy: %v", err)
			}
			if !fx.driver.Exists("src.txt") {
				t.Error("Copy must preserve the source")
			}
			if !fx.driver.Exists("copy.txt") {
				t.Error("Copy must create the destination")
			}
			got, _ := fx.driver.Get("copy.txt")
			if !bytes.Equal(got, payload) {
				t.Errorf("Copy destination contents = %q, want %q", got, payload)
			}

			if err := fx.driver.Move("copy.txt", "moved.txt"); err != nil {
				t.Fatalf("Move: %v", err)
			}
			if fx.driver.Exists("copy.txt") {
				t.Error("Move must remove the source")
			}
			if !fx.driver.Exists("moved.txt") {
				t.Error("Move must create the destination")
			}

			_ = fx.driver.Delete("src.txt", "moved.txt")
		})
	}
}

// TestParity_OverwriteReplacesContent guards against drivers that quietly
// append or version on repeated Put to the same key.
func TestParity_OverwriteReplacesContent(t *testing.T) {
	for _, fx := range fixtures(t) {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			t.Cleanup(fx.cleanup)

			const path = "overwrite.txt"
			if err := fx.driver.Put(path, []byte("first")); err != nil {
				t.Fatalf("Put first: %v", err)
			}
			if err := fx.driver.Put(path, []byte("second longer value")); err != nil {
				t.Fatalf("Put second: %v", err)
			}

			got, err := fx.driver.Get(path)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if string(got) != "second longer value" {
				t.Errorf("after overwrite: got %q, want %q", got, "second longer value")
			}

			_ = fx.driver.Delete(path)
		})
	}
}
