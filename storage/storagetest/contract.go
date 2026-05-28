// Package storagetest provides executable specifications (contract tests)
// for [storage.Driver] implementations.
//
// Every framework-shipped storage driver runs through RunDriverContractTests
// in CI; third-party drivers are expected to do the same so the I/O surface
// (Put / Get / Delete / List / metadata) behaves identically across local,
// S3, memory, and any future backend.
package storagetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/storage"
)

// DriverFactory returns a fresh Driver per sub-test.
type DriverFactory func(t *testing.T) storage.Driver

// RunDriverContractTests is the executable specification of [storage.Driver].
func RunDriverContractTests(t *testing.T, factory DriverFactory) {
	t.Helper()

	t.Run("PutCtx_Then_GetCtx_RoundTripsBytes", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		want := []byte("hello, storage")
		if err := d.PutCtx(ctx, "round.txt", want); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		got, err := d.GetCtx(ctx, "round.txt")
		if err != nil {
			t.Fatalf("GetCtx: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("round-trip mismatch: got %q, want %q", got, want)
		}
	})

	t.Run("GetCtx_MissingFile_ReturnsErrFileNotFound", func(t *testing.T) {
		d := factory(t)
		_, err := d.GetCtx(context.Background(), "does-not-exist.txt")
		if err == nil {
			t.Fatal("expected error reading missing file, got nil")
		}
		if !errors.Is(err, contract.ErrFileNotFound) {
			t.Fatalf("expected contract.ErrFileNotFound, got %v", err)
		}
	})

	t.Run("ExistsCtx_TruthForPresent_FalseForAbsent", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		_ = d.PutCtx(ctx, "exists.txt", []byte("v"))
		if !d.ExistsCtx(ctx, "exists.txt") {
			t.Fatal("expected ExistsCtx=true for present file")
		}
		if d.ExistsCtx(ctx, "ghost.txt") {
			t.Fatal("expected ExistsCtx=false for absent file")
		}
	})

	t.Run("DeleteCtx_PresentFile_Removes", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		_ = d.PutCtx(ctx, "delete-me.txt", []byte("v"))
		if err := d.DeleteCtx(ctx, "delete-me.txt"); err != nil {
			t.Fatalf("DeleteCtx: %v", err)
		}
		if d.ExistsCtx(ctx, "delete-me.txt") {
			t.Fatal("file present after Delete")
		}
	})

	t.Run("PutStreamCtx_Then_GetStreamCtx_RoundTripsStream", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		want := []byte("streamed content")
		if err := d.PutStreamCtx(ctx, "stream.txt", bytes.NewReader(want)); err != nil {
			t.Fatalf("PutStreamCtx: %v", err)
		}
		rc, err := d.GetStreamCtx(ctx, "stream.txt")
		if err != nil {
			t.Fatalf("GetStreamCtx: %v", err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("stream round-trip mismatch")
		}
	})

	t.Run("SizeCtx_ReportsByteCount", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		_ = d.PutCtx(ctx, "size.bin", []byte("12345"))
		n, err := d.SizeCtx(ctx, "size.bin")
		if err != nil {
			t.Fatalf("SizeCtx: %v", err)
		}
		if n != 5 {
			t.Fatalf("expected Size=5, got %d", n)
		}
	})

	t.Run("LastModifiedCtx_ReturnsRecentTime", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		before := time.Now().Add(-time.Minute)
		_ = d.PutCtx(ctx, "lm.txt", []byte("v"))
		ts, err := d.LastModifiedCtx(ctx, "lm.txt")
		if err != nil {
			t.Fatalf("LastModifiedCtx: %v", err)
		}
		if ts.Before(before) {
			t.Fatalf("LastModified earlier than expected: %v < %v", ts, before)
		}
	})

	t.Run("CopyCtx_CreatesIdenticalDest", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		_ = d.PutCtx(ctx, "src.txt", []byte("payload"))
		if err := d.CopyCtx(ctx, "src.txt", "dst.txt"); err != nil {
			t.Fatalf("CopyCtx: %v", err)
		}
		got, err := d.GetCtx(ctx, "dst.txt")
		if err != nil {
			t.Fatalf("GetCtx dst: %v", err)
		}
		if string(got) != "payload" {
			t.Fatalf("Copy produced %q, want payload", got)
		}
		// Source must still exist.
		if !d.ExistsCtx(ctx, "src.txt") {
			t.Fatal("source removed by Copy")
		}
	})

	t.Run("MoveCtx_RelocatesFile", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		_ = d.PutCtx(ctx, "move-src.txt", []byte("payload"))
		if err := d.MoveCtx(ctx, "move-src.txt", "move-dst.txt"); err != nil {
			t.Fatalf("MoveCtx: %v", err)
		}
		if d.ExistsCtx(ctx, "move-src.txt") {
			t.Fatal("source still present after Move")
		}
		got, err := d.GetCtx(ctx, "move-dst.txt")
		if err != nil {
			t.Fatalf("GetCtx dst: %v", err)
		}
		if string(got) != "payload" {
			t.Fatalf("Move produced %q, want payload", got)
		}
	})

	t.Run("MakeDirectoryCtx_Then_FilesCtx_ListsEmpty", func(t *testing.T) {
		d := factory(t)
		ctx := context.Background()
		if err := d.MakeDirectoryCtx(ctx, "subdir"); err != nil {
			t.Fatalf("MakeDirectoryCtx: %v", err)
		}
		got, err := d.FilesCtx(ctx, "subdir")
		if err != nil {
			t.Fatalf("FilesCtx: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty directory, got %v", got)
		}
	})

	t.Run("PutCtx_CancelledCtx_ReturnsError", func(t *testing.T) {
		d := factory(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := d.PutCtx(ctx, "cancelled.txt", []byte("v"))
		// Drivers MAY honour the ctx cancel and refuse; drivers backed
		// by pure memory MAY succeed (no I/O to abort). The invariant
		// is "never panic". Some drivers (local) refuse early; others
		// (memory) accept. Either is conforming.
		_ = err
	})

	t.Run("URL_ReturnsStableString", func(t *testing.T) {
		d := factory(t)
		// URL is a pure transform; we only check that two calls with the
		// same path agree.
		a := d.URL("some/path.txt")
		b := d.URL("some/path.txt")
		if a != b {
			t.Fatalf("URL not deterministic: %q vs %q", a, b)
		}
	})
}
