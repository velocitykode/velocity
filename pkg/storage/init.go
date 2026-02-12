package storage

// init no longer auto-initializes from environment.
// Use NewManager(config) to create an instance.
func init() {
	// Intentionally empty — storage instances are created via NewManager().
}
