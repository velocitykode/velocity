package console

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/fsnotify/fsnotify"
)

// ServeOptions holds flags for the serve command.
type ServeOptions struct {
	Port      string
	Env       string
	Watch     bool
	BuildTags string
}

// Serve starts the development server with optional hot reload.
func Serve(opts ServeOptions) error {
	if opts.Port == "" {
		opts.Port = "4000"
	}
	if opts.Env == "" {
		opts.Env = "development"
	}

	if opts.Watch {
		cli.Info(fmt.Sprintf("Starting on port %s with hot reload...", opts.Port))
	} else {
		cli.Info(fmt.Sprintf("Starting on port %s...", opts.Port))
	}

	// Start Vite dev server if package.json exists
	var viteCmd *exec.Cmd
	if _, err := os.Stat("package.json"); err == nil {
		viteCmd = startVite()
	}

	// Handle graceful shutdown
	setupGracefulShutdown(viteCmd)

	if opts.Watch {
		return runWithWatcher(opts)
	}
	return runServer(opts)
}

func startVite() *exec.Cmd {
	runner := "npm"
	if _, err := exec.LookPath("bun"); err == nil {
		if _, err := os.Stat("bun.lock"); err == nil {
			runner = "bun"
		}
	}

	cli.Info(fmt.Sprintf("Starting Vite (%s run dev)...", runner))
	cmd := exec.Command(runner, "run", "dev")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		cli.Warning(fmt.Sprintf("Failed to start Vite: %v", err))
		return nil
	}
	return cmd
}

func setupGracefulShutdown(viteCmd *exec.Cmd) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-c
		cli.Info("Shutting down...")
		if viteCmd != nil && viteCmd.Process != nil {
			syscall.Kill(-viteCmd.Process.Pid, syscall.SIGTERM)
		}
		os.Exit(0)
	}()
}

func runServer(opts ServeOptions) error {
	os.Setenv("APP_ENV", opts.Env)
	os.Setenv("APP_PORT", opts.Port)

	os.MkdirAll(".vel/tmp", 0755)

	buildArgs := []string{"build", "-o", ".vel/tmp/server"}
	if opts.BuildTags != "" {
		buildArgs = append(buildArgs, "-tags", opts.BuildTags)
	}
	buildArgs = append(buildArgs, ".")

	buildCmd := exec.Command("go", buildArgs...)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	serverCmd := exec.Command(".vel/tmp/server", "serve:run")
	serverCmd.Stdout = os.Stdout
	serverCmd.Stderr = os.Stderr
	serverCmd.Env = os.Environ()

	if err := serverCmd.Run(); err != nil {
		return fmt.Errorf("server failed: %w", err)
	}
	return nil
}

func runWithWatcher(opts ServeOptions) error {
	os.MkdirAll(".vel/tmp", 0755)

	rebuild := make(chan bool, 1)
	errChan := make(chan error, 1)

	go func() {
		if err := watchFiles(rebuild); err != nil {
			errChan <- err
		}
	}()

	var serverCmd *exec.Cmd
	var mu sync.Mutex

	startServer := func() error {
		mu.Lock()
		defer mu.Unlock()

		if serverCmd != nil && serverCmd.Process != nil {
			serverCmd.Process.Kill()
			serverCmd.Wait()
		}

		cli.Info("Building...")
		buildArgs := []string{"build", "-o", ".vel/tmp/server"}
		if opts.BuildTags != "" {
			buildArgs = append(buildArgs, "-tags", opts.BuildTags)
		}
		buildArgs = append(buildArgs, ".")

		buildCmd := exec.Command("go", buildArgs...)
		if output, err := buildCmd.CombinedOutput(); err != nil {
			cli.Error(fmt.Sprintf("Build failed:\n%s", string(output)))
			return err
		}

		// Refresh the project's ./vel binary so one-shot CLI commands
		// (route:list, migrate, make:*) in other terminals see current
		// source while serve --watch is running. Go's build cache makes
		// this ~100ms since the source was just compiled above. Failure
		// here is non-fatal — the server itself rebuilt successfully,
		// and the user keeps whatever ./vel snapshot they had.
		velArgs := []string{"build", "-o", "./vel"}
		if opts.BuildTags != "" {
			velArgs = append(velArgs, "-tags", opts.BuildTags)
		}
		velArgs = append(velArgs, ".")
		if output, err := exec.Command("go", velArgs...).CombinedOutput(); err != nil {
			cli.Warning(fmt.Sprintf("./vel refresh failed (server keeps running on previous): %s", strings.TrimSpace(string(output))))
		}

		cli.Info(fmt.Sprintf("Starting server on port %s...", opts.Port))
		serverCmd = exec.Command(".vel/tmp/server", "serve:run")
		serverCmd.Stdout = os.Stdout
		serverCmd.Stderr = os.Stderr
		serverCmd.Env = append(os.Environ(),
			fmt.Sprintf("APP_ENV=%s", opts.Env),
			fmt.Sprintf("APP_PORT=%s", opts.Port),
		)

		if err := serverCmd.Start(); err != nil {
			return fmt.Errorf("failed to start server: %w", err)
		}
		return nil
	}

	if err := startServer(); err != nil {
		return err
	}

	for {
		select {
		case err := <-errChan:
			return err
		case <-rebuild:
			cli.Warning("File changed, reloading...")
			time.Sleep(100 * time.Millisecond)
			startServer()
		}
	}
}

func watchFiles(rebuild chan bool) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer watcher.Close()

	err = filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name := info.Name()
		if info.IsDir() && (name == "vendor" || name == ".vel" || name == "node_modules" || name == ".git" || name == "tmp") {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return watcher.Add(path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to setup watcher: %w", err)
	}

	var debounce *time.Timer

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if !strings.HasSuffix(event.Name, ".go") {
				continue
			}
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(500*time.Millisecond, func() {
				select {
				case rebuild <- true:
				default:
				}
			})
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			cli.Error(fmt.Sprintf("Watcher error: %v", err))
		}
	}
}
