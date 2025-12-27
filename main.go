package velocity

import "fmt"

// App represents the Velocity application
type App struct {
	version string
}

// New creates a new Velocity application
func New() *App {
	return &App{
		version: "0.0.1",
	}
}

// Run starts the application
func (a *App) Run() {
	fmt.Printf("🚀 Velocity v%s is running! (Local development mode)\n", a.version)
}

// Version returns the framework version
func (a *App) Version() string {
	return a.version
}
