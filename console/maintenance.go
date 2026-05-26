package console

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/internal/maintpath"
)

// DownOptions configures maintenance mode behavior.
type DownOptions struct {
	Secret     string
	RetryAfter int
}

// downPayload is the JSON structure written to the .vel/down marker file.
type downPayload struct {
	Secret     string `json:"secret,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
	Time       string `json:"time"`
}

// Down puts the application into maintenance mode by creating a .vel/down
// marker file under the resolved maintenance root (see internal/maintpath).
//
// The marker file is written with restrictive permissions (0o600) because it
// may contain a bypass secret that grants per-browser access to the app while
// it is otherwise serving 503. The containing directory is created with 0o700
// for the same reason. Both align with CLAUDE.md security rule #4 on the
// principle of least privilege for files holding sensitive material.
//
// Resolution of the marker location happens through internal/maintpath so
// the runtime middleware and this writer agree on a single absolute path.
// Process cwd at the time of `vel down` no longer matters.
func Down(opts DownOptions) error {
	dir, err := maintpath.MarkerDirPath()
	if err != nil {
		return fmt.Errorf("resolve maintenance root: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s directory: %w", maintpath.MarkerDir, err)
	}

	payload := downPayload{
		Secret:     opts.Secret,
		RetryAfter: opts.RetryAfter,
		Time:       time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal maintenance payload: %w", err)
	}

	path := filepath.Join(dir, maintpath.MarkerFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("failed to write maintenance file: %w", err)
	}

	cli.Success(fmt.Sprintf("Application is now in maintenance mode (marker: %s).", path))
	return nil
}

// Up takes the application out of maintenance mode by removing the .vel/down
// marker file under the resolved maintenance root. Resolves the same path
// Down() writes to so a misconfigured root does not silently leave a marker
// behind.
func Up() error {
	path, err := maintpath.MarkerPath()
	if err != nil {
		return fmt.Errorf("resolve maintenance root: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove maintenance file: %w", err)
	}

	cli.Success("Application is now live.")
	return nil
}
