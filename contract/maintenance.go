package contract

// MaintenanceConfig configures the maintenance-mode gate and the down/up
// commands that write its marker file.
type MaintenanceConfig struct {
	// Root is the absolute directory holding the .vel marker directory.
	// Empty means the process working directory captured at first use.
	// It must be absolute and contain no ".." segment; an invalid Root
	// leaves the gate open (no marker is ever found) and logs once.
	Root string
	// ExcludePaths are path prefixes served normally during maintenance,
	// added to the built-in health probes ("/healthz", "/livez", "/readyz").
	ExcludePaths []string
	// AppKey is mixed into the bypass-cookie MAC as the HKDF salt, so a
	// cookie minted under one app key does not verify under another.
	// Empty uses a fixed default salt.
	AppKey string
}
