package console

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installFakeBuf writes an executable shell script named "buf" into a fresh
// directory and prepends that directory to PATH for the test. The script
// records its invocation by writing argv to <dir>/calls.log and, if exit is
// non-zero, exits with that status. The recorded log lets us prove the
// subprocess was actually invoked with the expected arguments rather than
// merely shadowed by a no-op.
func installFakeBuf(t *testing.T, exit int) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake buf script requires POSIX shell")
	}
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"" + logPath + "\"\n" +
		"echo \"cwd:$(pwd)\" >> \"" + logPath + "\"\n"
	if exit != 0 {
		script += "echo 'fake buf failure' 1>&2\n"
		script += "exit " + itoa(exit) + "\n"
	}
	bufPath := filepath.Join(dir, "buf")
	if err := os.WriteFile(bufPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake buf: %v", err)
	}
	t.Setenv("PATH", dir)
	return logPath
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func TestMakeGRPCGen_InvokesBufWithGenerateInProtoDir(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join("api", "proto"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := installFakeBuf(t, 0)

	if err := MakeGRPCGen(); err != nil {
		t.Fatalf("MakeGRPCGen: %v", err)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("fake buf was not invoked: %v", err)
	}
	s := string(log)
	if !strings.Contains(s, "generate") {
		t.Errorf("expected fake buf to receive `generate` arg, got: %q", s)
	}
	if !strings.Contains(s, filepath.Join("api", "proto")) {
		t.Errorf("expected fake buf to be invoked with cwd=api/proto, got: %q", s)
	}
}

func TestMakeGRPCGen_MissingProtoDir(t *testing.T) {
	t.Chdir(t.TempDir())
	installFakeBuf(t, 0)

	err := MakeGRPCGen()
	if err == nil {
		t.Fatal("expected error when api/proto missing")
	}
	if !strings.Contains(err.Error(), "gen grpc service") {
		t.Errorf("error should hint at running gen grpc service, got: %v", err)
	}
}

func TestMakeGRPCGen_BufNotInPATH(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join("api", "proto"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("PATH", t.TempDir())

	err := MakeGRPCGen()
	if err == nil {
		t.Fatal("expected error when buf missing from PATH")
	}
	if !strings.Contains(err.Error(), "buf not found") {
		t.Errorf("error should mention buf not found, got: %v", err)
	}
	if !strings.Contains(err.Error(), "buf.build") {
		t.Errorf("error should include installation URL, got: %v", err)
	}
}

func TestMakeGRPCGen_BufExitsNonZero(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join("api", "proto"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	installFakeBuf(t, 2)

	err := MakeGRPCGen()
	if err == nil {
		t.Fatal("expected error when buf exits non-zero")
	}
	if !strings.Contains(err.Error(), "buf generate failed") {
		t.Errorf("error should describe failure, got: %v", err)
	}
}

// TestMakeGRPCGen_StreamsBufStderr regression-guards against silencing buf's
// own diagnostics. The fake script writes to stderr; we capture os.Stderr by
// redirecting it through a pipe and verify the message reached the test.
func TestMakeGRPCGen_StreamsBufStderr(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join("api", "proto"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	installFakeBuf(t, 3)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()

	_ = MakeGRPCGen()
	w.Close()

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	if !strings.Contains(buf.String(), "fake buf failure") {
		t.Errorf("expected buf stderr to be streamed, got: %q", buf.String())
	}
}
