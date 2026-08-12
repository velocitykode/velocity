package console

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/velocitykode/prism"
)

// GenGRPCGen runs `buf generate` inside api/proto. It surfaces buf's stdout
// and stderr to the user and fails loudly when buf is missing.
func GenGRPCGen() error {
	dir := filepath.Join("api", "proto")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("no api/proto directory; run `vel gen grpc service <Name>` first")
	}
	if _, err := exec.LookPath("buf"); err != nil {
		return fmt.Errorf("buf not found in PATH; install from https://buf.build/docs/installation")
	}

	prism.Muted("cd api/proto && buf generate")
	cmd := exec.Command("buf", "generate")
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buf generate failed: %w", err)
	}
	prism.Success("Generated Go code in api/gen/go/")
	return nil
}
