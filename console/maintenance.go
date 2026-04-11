package console

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cli "github.com/velocitykode/velocity-cli"
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

// Down puts the application into maintenance mode by creating a .vel/down marker file.
func Down(opts DownOptions) error {
	dir := filepath.Join(".", ".vel")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create .vel directory: %w", err)
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

	path := filepath.Join(dir, "down")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write maintenance file: %w", err)
	}

	cli.Success("Application is now in maintenance mode.")
	return nil
}

// Up takes the application out of maintenance mode by removing the .vel/down marker file.
func Up() error {
	path := filepath.Join(".", ".vel", "down")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove maintenance file: %w", err)
	}

	cli.Success("Application is now live.")
	return nil
}
