package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/onllm-dev/syntrack/internal/agent"
	"github.com/onllm-dev/syntrack/internal/api"
	"github.com/onllm-dev/syntrack/internal/config"
	"github.com/onllm-dev/syntrack/internal/store"
	"github.com/onllm-dev/syntrack/internal/tracker"
	"github.com/onllm-dev/syntrack/internal/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

var (
	pidDir  = defaultPIDDir()
	pidFile = filepath.Join(pidDir, "syntrack.pid")
)

// stopPreviousInstance stops any running syntrack instance using PID file + port check.
func stopPreviousInstance(port int) {
	myPID := os.Getpid()
	stopped := false

	// Method 1: PID file
	if data, err := os.ReadFile(pidFile); err == nil {
		pidStr := strings.TrimSpace(string(data))
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 && pid != myPID {
			if proc, err := os.FindProcess(pid); err == nil {
				if err := proc.Signal(syscall.SIGTERM); err == nil {
					fmt.Printf("Stopped previous instance (PID %d) via PID file\n", pid)
					stopped = true
				}
			}
		}
		os.Remove(pidFile)
	}

	// Method 2: Check if the port is in use and kill the occupying syntrack process
	if !stopped && port > 0 {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			// Port is occupied — find which process holds it
			if pids := findSyntrackOnPort(port); len(pids) > 0 {
				for _, pid := range pids {
					if pid == myPID {
						continue
					}
					if proc, err := os.FindProcess(pid); err == nil {
						if err := proc.Signal(syscall.SIGTERM); err == nil {
							fmt.Printf("Stopped previous instance (PID %d) on port %d\n", pid, port)
							stopped = true
						}
					}
				}
			}
		}
	}

	if stopped {
		time.Sleep(500 * time.Millisecond)
	}
}

// findSyntrackOnPort uses lsof (macOS/Linux) to find syntrack processes on a port.
func findSyntrackOnPort(port int) []int {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil
	}

	// lsof -ti :PORT gives PIDs listening on that port
	out, err := exec.Command("lsof", "-ti", fmt.Sprintf(":%d", port)).Output()
	if err != nil {
		return nil
	}

	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if pid, err := strconv.Atoi(line); err == nil && pid > 0 {
			// Verify it's a syntrack process by checking the command name
			if isSyntrackProcess(pid) {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

// isSyntrackProcess checks if a PID belongs to a syntrack binary.
func isSyntrackProcess(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false
	}
	cmd := strings.TrimSpace(string(out))
	return strings.Contains(strings.ToLower(cmd), "syntrack")
}

func ensurePIDDir() error {
	return os.MkdirAll(pidDir, 0755)
}

func writePIDFile() error {
	if err := ensurePIDDir(); err != nil {
		return fmt.Errorf("failed to create PID directory: %w", err)
	}
	return os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644)
}

func removePIDFile() {
	os.Remove(pidFile)
}

// daemonize re-executes the current binary as a detached background process.
// The parent writes the child's PID to .syntrack.pid and exits.
func daemonize(cfg *config.Config) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Resolve symlinks so re-exec works correctly
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("failed to resolve executable path: %w", err)
	}

	// Open log file for child's stdout/stderr
	logPath := filepath.Join(filepath.Dir(cfg.DBPath), ".syntrack.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file for daemon: %w", err)
	}

	// Build child command with same args
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "_SYNTRACK_DAEMON=1")
	cmd.SysProcAttr = daemonSysProcAttr()

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// Write child PID
	childPID := cmd.Process.Pid
	if err := ensurePIDDir(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not create PID directory: %v\n", err)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(childPID)), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write PID file: %v\n", err)
	}

	logFile.Close()

	fmt.Printf("Daemon started (PID %d), logs: %s\n", childPID, logPath)
	return nil
}

func run() error {
	// Handle subcommands that don't need full config first
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "stop":
			return runStop()
		case "status":
			return runStatus()
		case "--version", "-v":
			fmt.Printf("SynTrack v%s\n", version)
			return nil
		case "--help", "-h":
			printHelp()
			return nil
		}
	}

	// Also check for flags anywhere in args (backward compat)
	for _, arg := range os.Args[1:] {
		if arg == "--version" || arg == "-v" {
			fmt.Printf("SynTrack v%s\n", version)
			return nil
		}
		if arg == "--help" || arg == "-h" {
			printHelp()
			return nil
		}
	}

	// Parse flags and load config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	isDaemonChild := os.Getenv("_SYNTRACK_DAEMON") == "1"

	// Stop any previous instance (parent does this, daemon child skips it)
	if !isDaemonChild {
		stopPreviousInstance(cfg.Port)
	}

	// Daemonize: if not in debug mode and not already the daemon child, fork
	if !cfg.DebugMode && !isDaemonChild {
		printBanner(cfg, version)
		return daemonize(cfg)
	}

	// From here on, we are either the daemon child or running in --debug mode.

	// In daemon mode, the parent already wrote the PID file with our PID.
	// In debug mode, we write our own PID file.
	if cfg.DebugMode {
		if err := writePIDFile(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not write PID file: %v\n", err)
		}
	}
	defer removePIDFile()

	// Setup logging
	logWriter, err := cfg.LogWriter()
	if err != nil {
		return fmt.Errorf("failed to setup logging: %w", err)
	}
	defer func() {
		if closer, ok := logWriter.(interface{ Close() error }); ok && !cfg.DebugMode {
			closer.Close()
		}
	}()

	// Parse log level
	var logLevel slog.Level
	switch cfg.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	logger := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	// Print startup banner (only in debug/foreground mode)
	if cfg.DebugMode {
		printBanner(cfg, version)
	}

	// Open database
	db, err := store.New(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	logger.Info("Database opened", "path", cfg.DBPath)

	// Create API clients based on configured providers
	var syntheticClient *api.Client
	var zaiClient *api.ZaiClient

	if cfg.HasProvider("synthetic") {
		syntheticClient = api.NewClient(cfg.SyntheticAPIKey, logger)
		logger.Info("Synthetic API client configured")
	}

	if cfg.HasProvider("zai") {
		zaiClient = api.NewZaiClient(cfg.ZaiAPIKey, logger)
		logger.Info("Z.ai API client configured", "base_url", cfg.ZaiBaseURL)
	}

	// Create components
	tr := tracker.New(db, logger)

	// Create agents
	var ag *agent.Agent
	if syntheticClient != nil {
		ag = agent.New(syntheticClient, db, tr, cfg.PollInterval, logger)
	}

	var zaiAg *agent.ZaiAgent
	if zaiClient != nil {
		zaiAg = agent.NewZaiAgent(zaiClient, db, cfg.PollInterval, logger)
	}

	handler := web.NewHandler(db, tr, logger, nil, cfg)
	server := web.NewServer(cfg.Port, handler, logger, cfg.AdminUser, cfg.AdminPass)

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start agents in goroutines
	agentErr := make(chan error, 2)
	if ag != nil {
		go func() {
			logger.Info("Starting Synthetic agent", "interval", cfg.PollInterval)
			if err := ag.Run(ctx); err != nil {
				agentErr <- fmt.Errorf("synthetic agent error: %w", err)
			}
		}()
	}

	if zaiAg != nil {
		go func() {
			logger.Info("Starting Z.ai agent", "interval", cfg.PollInterval)
			if err := zaiAg.Run(ctx); err != nil {
				agentErr <- fmt.Errorf("zai agent error: %w", err)
			}
		}()
	}

	if ag == nil && zaiAg == nil {
		logger.Info("No agents configured")
	}

	// Start web server in goroutine
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("Starting web server", "port", cfg.Port)
		if err := server.Start(); err != nil {
			serverErr <- fmt.Errorf("server error: %w", err)
		}
	}()

	// Wait for signal or error
	select {
	case sig := <-sigChan:
		logger.Info("Received signal, shutting down gracefully", "signal", sig)
	case err := <-agentErr:
		if err != nil {
			logger.Error("Agent failed", "error", err)
			cancel()
		}
	case err := <-serverErr:
		logger.Error("Server failed", "error", err)
		cancel()
	}

	// Graceful shutdown sequence
	logger.Info("Shutting down...")

	// Cancel context to stop agent
	cancel()

	// Give agent a moment to clean up
	time.Sleep(100 * time.Millisecond)

	// Shutdown server with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("Server shutdown error", "error", err)
	}

	// Close database
	if err := db.Close(); err != nil {
		logger.Error("Database close error", "error", err)
	}

	logger.Info("Shutdown complete")
	return nil
}

// runStop stops any running syntrack instance.
func runStop() error {
	myPID := os.Getpid()
	stopped := false

	// Method 1: PID file
	if data, err := os.ReadFile(pidFile); err == nil {
		pidStr := strings.TrimSpace(string(data))
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 && pid != myPID {
			if proc, err := os.FindProcess(pid); err == nil {
				if err := proc.Signal(syscall.SIGTERM); err == nil {
					fmt.Printf("Stopped syntrack (PID %d)\n", pid)
					stopped = true
				} else {
					fmt.Printf("Process %d not running (stale PID file)\n", pid)
				}
			}
		}
		os.Remove(pidFile)
	}

	// Method 2: Port-based fallback — check default and common ports
	if !stopped {
		for _, port := range []int{8932} {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
			if err != nil {
				continue
			}
			conn.Close()
			if pids := findSyntrackOnPort(port); len(pids) > 0 {
				for _, pid := range pids {
					if pid == myPID {
						continue
					}
					if proc, err := os.FindProcess(pid); err == nil {
						if err := proc.Signal(syscall.SIGTERM); err == nil {
							fmt.Printf("Stopped syntrack (PID %d) on port %d\n", pid, port)
							stopped = true
						}
					}
				}
			}
		}
	}

	if !stopped {
		fmt.Println("No running syntrack instance found")
	}
	return nil
}

// runStatus reports the status of any running syntrack instance.
func runStatus() error {
	myPID := os.Getpid()

	// Check PID file
	if data, err := os.ReadFile(pidFile); err == nil {
		pidStr := strings.TrimSpace(string(data))
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 && pid != myPID {
			if proc, err := os.FindProcess(pid); err == nil {
				// On Unix, signal 0 checks if process exists without killing it
				if err := proc.Signal(syscall.Signal(0)); err == nil {
					fmt.Printf("syntrack is running (PID %d)\n", pid)

					// Check which port it's listening on
					for _, port := range []int{8932, 8080, 9000} {
						if pids := findSyntrackOnPort(port); len(pids) > 0 {
							for _, p := range pids {
								if p == pid {
									fmt.Printf("  Dashboard: http://localhost:%d\n", port)
									break
								}
							}
						}
					}

					// Show PID file location
					fmt.Printf("  PID file:  %s\n", pidFile)

					// Show log file if it exists
					logPath := ".syntrack.log"
					if info, err := os.Stat(logPath); err == nil {
						fmt.Printf("  Log file:  %s (%s)\n", logPath, humanSize(info.Size()))
					}

					// Show DB file if it exists
					dbPath := "./syntrack.db"
					if info, err := os.Stat(dbPath); err == nil {
						fmt.Printf("  Database:  %s (%s)\n", dbPath, humanSize(info.Size()))
					}

					return nil
				}
			}
			// Stale PID file
			fmt.Printf("syntrack is not running (stale PID file for PID %d)\n", pid)
			return nil
		}
	}

	// No PID file — try port check
	for _, port := range []int{8932} {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err != nil {
			continue
		}
		conn.Close()
		if pids := findSyntrackOnPort(port); len(pids) > 0 {
			for _, pid := range pids {
				if pid == myPID {
					continue
				}
				fmt.Printf("syntrack is running (PID %d) on port %d\n", pid, port)
				fmt.Printf("  Dashboard: http://localhost:%d\n", port)
				return nil
			}
		}
	}

	fmt.Println("syntrack is not running")
	return nil
}

// humanSize returns a human-readable file size.
func humanSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}

func printBanner(cfg *config.Config, version string) {
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Printf("║  SynTrack v%-25s ║\n", version)
	fmt.Println("╠══════════════════════════════════════╣")

	// Show configured providers
	providers := cfg.AvailableProviders()
	if len(providers) > 0 {
		fmt.Printf("║  Providers: %-24s ║\n", strings.Join(providers, ", "))
	}

	if cfg.HasProvider("synthetic") {
		fmt.Println("║  API:       synthetic.new/v2/quotas  ║")
	}
	if cfg.HasProvider("zai") {
		fmt.Println("║  API:       z.ai/api                ║")
	}

	fmt.Printf("║  Polling:   every %s              ║\n", cfg.PollInterval)
	fmt.Printf("║  Dashboard: http://localhost:%d    ║\n", cfg.Port)
	fmt.Printf("║  Database:  %-24s ║\n", cfg.DBPath)
	fmt.Printf("║  Auth:      %s / ****             ║\n", cfg.AdminUser)
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Println()

	// Show API keys
	if cfg.HasProvider("synthetic") {
		fmt.Printf("Synthetic API Key: %s\n", redactAPIKey(cfg.SyntheticAPIKey))
	}
	if cfg.HasProvider("zai") {
		fmt.Printf("Z.ai API Key:      %s\n", redactAPIKey(cfg.ZaiAPIKey))
	}
	fmt.Println()
}

func printHelp() {
	fmt.Println("SynTrack - Multi-Provider API Usage Tracker")
	fmt.Println()
	fmt.Println("Usage: syntrack [COMMAND] [OPTIONS]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  stop               Stop the running syntrack instance")
	fmt.Println("  status             Show status of the running instance")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --version          Print version and exit")
	fmt.Println("  --help             Print this help message")
	fmt.Println("  --interval SEC     Polling interval in seconds (default: 60)")
	fmt.Println("  --port PORT        Dashboard HTTP port (default: 8932)")
	fmt.Println("  --db PATH          SQLite database file path (default: ./syntrack.db)")
	fmt.Println("  --debug            Run in foreground mode, log to stdout")
	fmt.Println()
	fmt.Println("Environment Variables:")
	fmt.Println("  SYNTHETIC_API_KEY       Synthetic API key (configure at least one provider)")
	fmt.Println("  ZAI_API_KEY            Z.ai API key")
	fmt.Println("  ZAI_BASE_URL           Z.ai base URL (default: https://api.z.ai/api)")
	fmt.Println("  SYNTRACK_POLL_INTERVAL  Polling interval in seconds")
	fmt.Println("  SYNTRACK_PORT           Dashboard HTTP port")
	fmt.Println("  SYNTRACK_ADMIN_USER     Dashboard admin username")
	fmt.Println("  SYNTRACK_ADMIN_PASS     Dashboard admin password")
	fmt.Println("  SYNTRACK_DB_PATH        SQLite database file path")
	fmt.Println("  SYNTRACK_LOG_LEVEL      Log level: debug, info, warn, error")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  syntrack                           # Run in background mode")
	fmt.Println("  syntrack --debug                   # Run in foreground mode")
	fmt.Println("  syntrack --interval 30 --port 8080 # Custom interval and port")
	fmt.Println("  syntrack stop                      # Stop running instance")
	fmt.Println("  syntrack status                    # Check if running")
	fmt.Println()
	fmt.Println("Configure providers in .env file or environment variables.")
	fmt.Println("At least one provider (Synthetic or Z.ai) must be configured.")
}

func redactAPIKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) < 8 {
		return "***"
	}

	// Handle "syn_" prefix for Synthetic keys
	prefix := ""
	if strings.HasPrefix(key, "syn_") {
		prefix = "syn_"
		key = key[4:]
	}

	if len(key) <= 8 {
		return prefix + key[:4] + "***"
	}
	return prefix + key[:4] + "***" + key[len(key)-4:]
}
