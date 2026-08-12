package stubs

import "embed"

//go:embed internal/handlers/*.stub internal/middleware/*.stub internal/models/*.stub internal/events/*.stub internal/listeners/*.stub internal/jobs/*.stub internal/mail/*.stub internal/notifications/*.stub internal/resources/*.stub internal/policies/*.stub internal/modules/*.stub internal/commands/*.stub routes/*.stub config/*.stub database/migrations/*.stub grpc/*.stub main.go.stub
var FS embed.FS

// Get returns the content of a stub file
func Get(name string) ([]byte, error) {
	return FS.ReadFile(name)
}
