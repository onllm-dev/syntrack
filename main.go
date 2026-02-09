package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/onllm-dev/onwatch/internal/agent"
	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/config"
	"github.com/onllm-dev/onwatch/internal/notify"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
	"github.com/onllm-dev/onwatch/internal/update"
	"github.com/onllm-dev/onwatch/internal/web"
)

//go:embed VERSION
var embeddedVersion string

var version = "dev"

func init() {
	if version == "dev" {
		version = strings.TrimSpace(embeddedVersion)
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

var (
	pidDir  = defaultPIDDir()
	pidFile = filepath.Join(pidDir, "onwatch.pid")
)

// hasFlag checks if a flag exists anywhere in os.Args[1:].
func hasFlag(flag string) bool {
	for _, arg := range os.Args[1:] {
		if arg == flag {
			return true
		}
	}
	return false
}

// hasCommand checks if any of the given commands/flags exist in os.Args[1:].
func hasCommand(cmds ...string) bool {
	for _, arg := range os.Args[1:] {
		for _, cmd := range cmds {
			if arg == cmd {
				return true
			}
		}
	}
	return false
}

// stopPreviousInstance stops any running onwatch instance using PID file + port check.
// In test mode, only PID file is used (no port scanning) to avoid killing production.
func stopPreviousInstance(port int, testMode bool) {
	myPID := os.Getpid()
	stopped := false

	// Method 1: PID file (handles both "PID" and "PID:PORT" formats)
	if data, err := os.ReadFile(pidFile); err == nil {
		content := strings.TrimSpace(string(data))
		var pid, filePort int

		// Parse PID:PORT format (new) or just PID (legacy)
		if strings.Contains(content, ":") {
			parts := strings.Split(content, ":")
			if len(parts) >= 2 {
				pid, _ = strconv.Atoi(parts[0])
				filePort, _ = strconv.Atoi(parts[1])
			}
		} else {
			pid, _ = strconv.Atoi(content)
		}

		if pid > 0 && pid != myPID {
			if proc, err := os.FindProcess(pid); err == nil {
				if err := proc.Signal(syscall.SIGTERM); err == nil {
					fmt.Printf("Stopped previous instance (PID %d) via PID file\n", pid)
					stopped = true
				}
			}
		}
		os.Remove(pidFile)

		// If PID file had a port and we didn't stop it, try that specific port
		if !stopped && filePort > 0 {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", filePort), 500*time.Millisecond)
			if err == nil {
				conn.Close()
				if pids := findOnwatchOnPort(filePort); len(pids) > 0 {
					for _, foundPID := range pids {
						if foundPID == myPID {
							continue
						}
						if proc, err := os.FindProcess(foundPID); err == nil {
							if err := proc.Signal(syscall.SIGTERM); err == nil {
								fmt.Printf("Stopped previous instance (PID %d) on port %d\n", foundPID, filePort)
								stopped = true
							}
						}
					}
				}
			}
		}
	}

	// Method 2: Check if the port is in use and kill the occupying onwatch process
	// Skip in test mode to avoid accidentally killing production instances
	if !testMode && !stopped && port > 0 {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			// Port is occupied — find which process holds it
			if pids := findOnwatchOnPort(port); len(pids) > 0 {
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

// findOnwatchOnPort uses lsof (macOS/Linux) to find onwatch processes on a port.
func findOnwatchOnPort(port int) []int {
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
			// Verify it's an onwatch process by checking the command name
			if isOnwatchProcess(pid) {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

// isOnwatchProcess checks if a PID belongs to an onwatch (or legacy syntrack) binary.
func isOnwatchProcess(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false
	}
	cmd := strings.ToLower(strings.TrimSpace(string(out)))
	return strings.Contains(cmd, "onwatch") || strings.Contains(cmd, "syntrack")
}

func ensurePIDDir() error {
	return os.MkdirAll(pidDir, 0755)
}

// sha256hex returns the SHA-256 hex hash of a string.
func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

// deriveEncryptionKey derives a 32-byte encryption key from the admin password hash.
// The password hash is expected to be a SHA-256 hex string (64 characters).
func deriveEncryptionKey(passwordHash string) string {
	if len(passwordHash) == 64 {
		return passwordHash
	}
	// Fallback: hash again if not already hex
	h := sha256.Sum256([]byte(passwordHash))
	return fmt.Sprintf("%x", h)
}

// migrateDBLocation moves the database from old default locations to the new one.
// Only runs when no explicit --db or ONWATCH_DB_PATH was set.
func migrateDBLocation(newPath string, logger *slog.Logger) {
	oldPaths := []string{
		"./onwatch.db",
	}
	oldHome := os.Getenv("HOME")
	if oldHome != "" {
		oldPaths = append(oldPaths,
			filepath.Join(oldHome, ".onwatch", "onwatch.db"),
		)
	}

	for _, oldPath := range oldPaths {
		if oldPath == newPath {
			continue
		}
		if _, err := os.Stat(oldPath); err != nil {
			continue
		}
		if _, err := os.Stat(newPath); err == nil {
			break // new already exists, skip
		}

		// Ensure target directory exists
		if err := os.MkdirAll(filepath.Dir(newPath), 0700); err != nil {
			logger.Warn("Failed to create data directory", "path", filepath.Dir(newPath), "error", err)
			continue
		}

		// Move DB + WAL/SHM files
		if err := os.Rename(oldPath, newPath); err != nil {
			logger.Warn("Failed to migrate database", "from", oldPath, "to", newPath, "error", err)
			continue
		}
		os.Rename(oldPath+"-wal", newPath+"-wal")
		os.Rename(oldPath+"-shm", newPath+"-shm")
		logger.Info("Migrated database", "from", oldPath, "to", newPath)
		break
	}
}

// fixExplicitDBPath detects when a user's .env has a misconfigured DB_PATH
// (e.g., ./onwatch.db or ./syntrack.db) while the canonical data/ path holds
// the actual historical data. It redirects to the canonical path so the
// dashboard shows existing data instead of appearing empty.
func fixExplicitDBPath(cfg *config.Config, logger *slog.Logger) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}

	canonicalPath := filepath.Join(home, ".onwatch", "data", "onwatch.db")

	// Already using the canonical path — nothing to fix
	absExplicit, _ := filepath.Abs(cfg.DBPath)
	absCan, _ := filepath.Abs(canonicalPath)
	if absExplicit == absCan {
		return
	}

	// Check if canonical path exists and has data
	canInfo, err := os.Stat(canonicalPath)
	if err != nil || canInfo.Size() == 0 {
		return // canonical doesn't exist or is empty
	}

	// Check if the explicit path exists
	expInfo, err := os.Stat(cfg.DBPath)
	if err != nil {
		// Explicit path doesn't even exist — use canonical
		logger.Info("Explicit DB path not found, redirecting to canonical",
			"explicit", cfg.DBPath, "canonical", canonicalPath)
		cfg.DBPath = canonicalPath
		return
	}

	// Both exist — use whichever is larger (has more data)
	if canInfo.Size() > expInfo.Size() {
		logger.Warn("Explicit DB path has less data than canonical path, redirecting",
			"explicit", cfg.DBPath, "explicitSize", expInfo.Size(),
			"canonical", canonicalPath, "canonicalSize", canInfo.Size())
		cfg.DBPath = canonicalPath
	}
}

func writePIDFile(port int) error {
	if err := ensurePIDDir(); err != nil {
		return fmt.Errorf("failed to create PID directory: %w", err)
	}
	// Store both PID and port for reliable stopping
	content := fmt.Sprintf("%d:%d", os.Getpid(), port)
	return os.WriteFile(pidFile, []byte(content), 0644)
}

func removePIDFile() {
	os.Remove(pidFile)
}

// daemonize re-executes the current binary as a detached background process.
// The parent writes the child's PID to .onwatch.pid and exits.
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
	logName := ".onwatch.log"
	if cfg.TestMode {
		logName = ".onwatch-test.log"
	}
	logPath := filepath.Join(filepath.Dir(cfg.DBPath), logName)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file for daemon: %w", err)
	}

	// Build child command with same args
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "_ONWATCH_DAEMON=1")
	cmd.SysProcAttr = daemonSysProcAttr()

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// Write child PID and port
	childPID := cmd.Process.Pid
	if err := ensurePIDDir(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not create PID directory: %v\n", err)
	}
	pidContent := fmt.Sprintf("%d:%d", childPID, cfg.Port)
	if err := os.WriteFile(pidFile, []byte(pidContent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write PID file: %v\n", err)
	}

	logFile.Close()

	fmt.Printf("Daemon started (PID %d), logs: %s\n", childPID, logPath)
	return nil
}

func run() error {
	// Phase 1: Detect test mode early and configure PID file for isolation
	testMode := hasFlag("--test")
	if testMode {
		pidFile = filepath.Join(pidDir, "onwatch-test.pid")
	}

	// Phase 2: Handle subcommands (both with and without -- prefix)
	if hasCommand("stop", "--stop") {
		return runStop(testMode)
	}
	if hasCommand("status", "--status") {
		return runStatus(testMode)
	}
	if hasCommand("--version", "-v", "version") {
		fmt.Printf("onWatch v%s\n", version)
		fmt.Println("github.com/onllm-dev/onwatch")
		fmt.Println("Powered by onllm.dev")
		return nil
	}
	if hasCommand("update", "--update") {
		return runUpdate()
	}
	if hasCommand("--help", "-h") {
		printHelp()
		return nil
	}

	// Memory tuning: GOMEMLIMIT triggers MADV_DONTNEED which actually shrinks RSS.
	// Without this, Go uses MADV_FREE on macOS — pages are reclaimable but still
	// counted in RSS, causing a permanent ratchet effect.
	debug.SetMemoryLimit(40 * 1024 * 1024) // 40 MiB soft limit
	debug.SetGCPercent(50)                 // GC at 50% heap growth (default 100)

	// Phase 3: Parse flags and load config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	isDaemonChild := os.Getenv("_ONWATCH_DAEMON") == "1"

	// Auto-fix systemd unit file BEFORE stopping the previous instance.
	// When a post-update child runs this, the daemon-reload completes while
	// the parent is still alive (systemd tracks it). After the child kills
	// the parent below, systemd sees Restart=always and auto-starts the new binary.
	// No-op if not under systemd or already up to date.
	update.MigrateSystemdUnit(slog.Default())

	// Stop any previous instance (parent does this, daemon child skips it)
	if !isDaemonChild {
		stopPreviousInstance(cfg.Port, testMode)
	}

	// Daemonize: if not in debug mode, not already the daemon child, and NOT in Docker, fork
	// Docker containers should always run in foreground mode (logs to stdout)
	if !cfg.DebugMode && !isDaemonChild && !cfg.IsDockerEnvironment() {
		printBanner(cfg, version)
		return daemonize(cfg)
	}

	// From here on, we are either the daemon child or running in --debug mode.

	// In daemon mode, the parent already wrote the PID file with our PID.
	// In debug mode, we write our own PID file.
	if cfg.DebugMode {
		if err := writePIDFile(cfg.Port); err != nil {
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

	// Ensure data directory exists and migrate DB if needed
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0700); err != nil {
		logger.Warn("Failed to create database directory", "error", err)
	}
	if !cfg.DBPathExplicit {
		migrateDBLocation(cfg.DBPath, logger)
	} else {
		// Fix for misconfigured DB_PATH: if the user's .env has a relative path
		// like ./onwatch.db or ./syntrack.db but the canonical data/ path has
		// existing data, redirect to the canonical path to avoid empty dashboard.
		fixExplicitDBPath(cfg, logger)
	}

	// Open database
	db, err := store.New(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	logger.Info("Database opened", "path", cfg.DBPath)

	// Password precedence: DB-stored hash takes priority over .env
	dbHash, hashErr := db.GetUser(cfg.AdminUser)
	if hashErr == nil && dbHash != "" {
		// DB has stored password — use it
		cfg.AdminPassHash = dbHash
		logger.Info("Using database-stored password for auth")
	} else {
		// No DB password — hash the .env password and store it
		cfg.AdminPassHash = sha256hex(cfg.AdminPass)
		if storeErr := db.UpsertUser(cfg.AdminUser, cfg.AdminPassHash); storeErr != nil {
			logger.Warn("Failed to store initial password hash", "error", storeErr)
		}
		logger.Info("Stored initial password hash in database")
	}

	// Close any orphaned sessions from previous runs (e.g., process was killed)
	if closed, err := db.CloseOrphanedSessions(); err != nil {
		logger.Warn("Failed to close orphaned sessions", "error", err)
	} else if closed > 0 {
		logger.Info("Closed orphaned sessions", "count", closed)
	}

	// Migrate existing sessions to usage-based detection (runs once)
	if err := db.MigrateSessionsToUsageBased(cfg.SessionIdleTimeout); err != nil {
		logger.Error("Session migration failed", "error", err)
	}

	// Auto-detect Anthropic token if not explicitly configured
	if cfg.AnthropicToken == "" {
		if token := api.DetectAnthropicToken(logger); token != "" {
			cfg.AnthropicToken = token
			cfg.AnthropicAutoToken = true
			logger.Info("Auto-detected Anthropic token from Claude Code credentials")
		}
	}

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

	var anthropicClient *api.AnthropicClient
	if cfg.HasProvider("anthropic") {
		anthropicClient = api.NewAnthropicClient(cfg.AnthropicToken, logger)
		logger.Info("Anthropic API client configured")
	}

	// Create components
	tr := tracker.New(db, logger)

	// Create agents with usage-based session managers
	idleTimeout := cfg.SessionIdleTimeout

	var ag *agent.Agent
	if syntheticClient != nil {
		sm := agent.NewSessionManager(db, "synthetic", idleTimeout, logger)
		ag = agent.New(syntheticClient, db, tr, cfg.PollInterval, logger, sm)
	}

	// Create Z.ai tracker
	var zaiTr *tracker.ZaiTracker
	if cfg.HasProvider("zai") {
		zaiTr = tracker.NewZaiTracker(db, logger)
	}

	var zaiAg *agent.ZaiAgent
	if zaiClient != nil {
		zaiSm := agent.NewSessionManager(db, "zai", idleTimeout, logger)
		zaiAg = agent.NewZaiAgent(zaiClient, db, zaiTr, cfg.PollInterval, logger, zaiSm)
	}

	// Create Anthropic tracker
	var anthropicTr *tracker.AnthropicTracker
	if cfg.HasProvider("anthropic") {
		anthropicTr = tracker.NewAnthropicTracker(db, logger)
	}

	var anthropicAg *agent.AnthropicAgent
	if anthropicClient != nil {
		anthropicSm := agent.NewSessionManager(db, "anthropic", idleTimeout, logger)
		anthropicAg = agent.NewAnthropicAgent(anthropicClient, db, anthropicTr, cfg.PollInterval, logger, anthropicSm)
		// Enable automatic token refresh — re-reads credentials before each poll
		// so expired OAuth tokens get picked up when Claude Code rotates them.
		anthropicAg.SetTokenRefresh(func() string {
			return api.DetectAnthropicToken(logger)
		})
		// Enable proactive OAuth refresh — refreshes token via OAuth API before expiry
		// and saves new tokens to credentials file immediately.
		anthropicAg.SetCredentialsRefresh(func() *api.AnthropicCredentials {
			return api.DetectAnthropicCredentials(logger)
		})
	}

	// Create notification engine
	notifier := notify.New(db, logger)
	notifier.SetEncryptionKey(deriveEncryptionKey(cfg.AdminPassHash))
	notifier.Reload()
	notifier.ConfigureSMTP()
	notifier.ConfigurePush()

	// Wire notifier to agents
	if ag != nil {
		ag.SetNotifier(notifier)
	}
	if zaiAg != nil {
		zaiAg.SetNotifier(notifier)
	}
	if anthropicAg != nil {
		anthropicAg.SetNotifier(notifier)
	}

	// Wire polling checks — agents skip poll when telemetry disabled
	isPollingEnabled := func(providerKey string) bool {
		v, err := db.GetSetting("provider_visibility")
		if err != nil || v == "" {
			return true // default: polling enabled
		}
		var vis map[string]map[string]bool
		if json.Unmarshal([]byte(v), &vis) != nil {
			return true
		}
		if pv, ok := vis[providerKey]; ok {
			if polling, exists := pv["polling"]; exists {
				return polling
			}
		}
		return true
	}
	if ag != nil {
		ag.SetPollingCheck(func() bool { return isPollingEnabled("synthetic") })
	}
	if zaiAg != nil {
		zaiAg.SetPollingCheck(func() bool { return isPollingEnabled("zai") })
	}
	if anthropicAg != nil {
		anthropicAg.SetPollingCheck(func() bool { return isPollingEnabled("anthropic") })
	}

	// Wire reset callbacks to trackers
	tr.SetOnReset(func(quotaName string) {
		notifier.Check(notify.QuotaStatus{Provider: "synthetic", QuotaKey: quotaName, ResetOccurred: true})
	})
	if zaiTr != nil {
		zaiTr.SetOnReset(func(quotaName string) {
			notifier.Check(notify.QuotaStatus{Provider: "zai", QuotaKey: quotaName, ResetOccurred: true})
		})
	}
	if anthropicTr != nil {
		anthropicTr.SetOnReset(func(quotaName string) {
			notifier.Check(notify.QuotaStatus{Provider: "anthropic", QuotaKey: quotaName, ResetOccurred: true})
		})
	}

	handler := web.NewHandler(db, tr, logger, nil, cfg, zaiTr)
	handler.SetVersion(version)
	handler.SetNotifier(notifier)
	if anthropicTr != nil {
		handler.SetAnthropicTracker(anthropicTr)
	}
	updater := update.NewUpdater(version, logger)
	handler.SetUpdater(updater)
	server := web.NewServer(cfg.Port, handler, logger, cfg.AdminUser, cfg.AdminPassHash)

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start agents in goroutines (staggered to avoid SQLite contention on session creation)
	agentErr := make(chan error, 3)
	if ag != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("Synthetic agent panicked", "panic", r)
					agentErr <- fmt.Errorf("synthetic agent panic: %v", r)
				}
			}()
			logger.Info("Starting Synthetic agent", "interval", cfg.PollInterval)
			if err := ag.Run(ctx); err != nil {
				agentErr <- fmt.Errorf("synthetic agent error: %w", err)
			}
		}()
	}

	if zaiAg != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("Z.ai agent panicked", "panic", r)
					agentErr <- fmt.Errorf("zai agent panic: %v", r)
				}
			}()
			time.Sleep(200 * time.Millisecond) // stagger to avoid SQLite BUSY
			logger.Info("Starting Z.ai agent", "interval", cfg.PollInterval)
			if err := zaiAg.Run(ctx); err != nil {
				agentErr <- fmt.Errorf("zai agent error: %w", err)
			}
		}()
	}

	if anthropicAg != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("Anthropic agent panicked", "panic", r)
					agentErr <- fmt.Errorf("anthropic agent panic: %v", r)
				}
			}()
			time.Sleep(400 * time.Millisecond) // stagger to avoid SQLite BUSY
			logger.Info("Starting Anthropic agent", "interval", cfg.PollInterval)
			if err := anthropicAg.Run(ctx); err != nil {
				agentErr <- fmt.Errorf("anthropic agent error: %w", err)
			}
		}()
	}

	if ag == nil && zaiAg == nil && anthropicAg == nil {
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

	// Periodically return freed memory to the OS. On macOS, MADV_FREE pages
	// are reclaimable but still counted in RSS. FreeOSMemory forces MADV_DONTNEED.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				debug.FreeOSMemory()
			}
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

// runStop stops any running onwatch instance.
// In test mode, only the test PID file is used (no port scanning) to avoid killing production.
func runStop(testMode bool) error {
	myPID := os.Getpid()
	stopped := false
	label := "onwatch"
	if testMode {
		label = "onwatch (test)"
	}

	// Method 1: PID file (handles both "PID" and "PID:PORT" formats)
	if data, err := os.ReadFile(pidFile); err == nil {
		content := strings.TrimSpace(string(data))
		var pid, port int

		// Parse PID:PORT format (new) or just PID (legacy)
		if strings.Contains(content, ":") {
			parts := strings.Split(content, ":")
			if len(parts) >= 2 {
				pid, _ = strconv.Atoi(parts[0])
				port, _ = strconv.Atoi(parts[1])
			}
		} else {
			pid, _ = strconv.Atoi(content)
		}

		if pid > 0 && pid != myPID {
			if proc, err := os.FindProcess(pid); err == nil {
				if err := proc.Signal(syscall.SIGTERM); err == nil {
					if port > 0 {
						fmt.Printf("Stopped %s (PID %d) on port %d\n", label, pid, port)
					} else {
						fmt.Printf("Stopped %s (PID %d)\n", label, pid)
					}
					stopped = true
				} else {
					fmt.Printf("Process %d not running (stale PID file)\n", pid)
				}
			}
		}
		os.Remove(pidFile)

		// If we have a port from PID file, try port-based detection on that specific port first
		// Skip in test mode to avoid killing production instances
		if !testMode && !stopped && port > 0 {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
			if err == nil {
				conn.Close()
				if pids := findOnwatchOnPort(port); len(pids) > 0 {
					for _, foundPID := range pids {
						if foundPID == myPID {
							continue
						}
						if proc, err := os.FindProcess(foundPID); err == nil {
							if err := proc.Signal(syscall.SIGTERM); err == nil {
								fmt.Printf("Stopped %s (PID %d) on port %d\n", label, foundPID, port)
								stopped = true
							}
						}
					}
				}
			}
		}
	}

	// Method 2: Port-based fallback — check default ports
	// Skip in test mode to avoid killing production instances
	if !testMode && !stopped {
		// Check both old (8932) and new (9211) default ports for backwards compatibility
		for _, port := range []int{9211, 8932} {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
			if err != nil {
				continue
			}
			conn.Close()
			if pids := findOnwatchOnPort(port); len(pids) > 0 {
				for _, pid := range pids {
					if pid == myPID {
						continue
					}
					if proc, err := os.FindProcess(pid); err == nil {
						if err := proc.Signal(syscall.SIGTERM); err == nil {
							fmt.Printf("Stopped %s (PID %d) on port %d\n", label, pid, port)
							stopped = true
						}
					}
				}
			}
		}
	}

	if !stopped {
		fmt.Printf("No running %s instance found\n", label)
	}
	return nil
}

// runStatus reports the status of any running onwatch instance.
// In test mode, only the test PID file is checked (no port scanning).
func runStatus(testMode bool) error {
	myPID := os.Getpid()
	label := "onwatch"
	if testMode {
		label = "onwatch (test)"
	}

	// Check PID file (handles both "PID" and "PID:PORT" formats)
	if data, err := os.ReadFile(pidFile); err == nil {
		content := strings.TrimSpace(string(data))
		var pid, port int

		// Parse PID:PORT format (new) or just PID (legacy)
		if strings.Contains(content, ":") {
			parts := strings.Split(content, ":")
			if len(parts) >= 2 {
				pid, _ = strconv.Atoi(parts[0])
				port, _ = strconv.Atoi(parts[1])
			}
		} else {
			pid, _ = strconv.Atoi(content)
		}

		if pid > 0 && pid != myPID {
			if proc, err := os.FindProcess(pid); err == nil {
				// On Unix, signal 0 checks if process exists without killing it
				if err := proc.Signal(syscall.Signal(0)); err == nil {
					fmt.Printf("%s is running (PID %d)\n", label, pid)

					// If we have port from PID file, show it directly
					if port > 0 {
						fmt.Printf("  Dashboard: http://localhost:%d\n", port)
					} else if !testMode {
						// Check which port it's listening on (skip in test mode)
						for _, checkPort := range []int{9211, 8932, 8080, 9000} {
							if pids := findOnwatchOnPort(checkPort); len(pids) > 0 {
								for _, p := range pids {
									if p == pid {
										fmt.Printf("  Dashboard: http://localhost:%d\n", checkPort)
										break
									}
								}
							}
						}
					}

					// Show PID file location
					fmt.Printf("  PID file:  %s\n", pidFile)

					// Show log file if it exists
					logPath := ".onwatch.log"
					if testMode {
						logPath = ".onwatch-test.log"
					}
					if info, err := os.Stat(logPath); err == nil {
						fmt.Printf("  Log file:  %s (%s)\n", logPath, humanSize(info.Size()))
					}

					// Show DB file if it exists (check new default path first, then old)
					home, _ := os.UserHomeDir()
					dbPaths := []string{
						filepath.Join(home, ".onwatch", "data", "onwatch.db"),
						"./onwatch.db",
					}
					for _, dbPath := range dbPaths {
						if info, err := os.Stat(dbPath); err == nil {
							fmt.Printf("  Database:  %s (%s)\n", dbPath, humanSize(info.Size()))
							break
						}
					}

					return nil
				}
			}
			// Stale PID file
			fmt.Printf("%s is not running (stale PID file for PID %d)\n", label, pid)
			return nil
		}
	}

	// No PID file — try port check (skip in test mode to avoid confusion with production)
	if !testMode {
		for _, port := range []int{9211, 8932} {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
			if err != nil {
				continue
			}
			conn.Close()
			if pids := findOnwatchOnPort(port); len(pids) > 0 {
				for _, pid := range pids {
					if pid == myPID {
						continue
					}
					fmt.Printf("%s is running (PID %d) on port %d\n", label, pid, port)
					fmt.Printf("  Dashboard: http://localhost:%d\n", port)
					return nil
				}
			}
		}
	}

	fmt.Printf("%s is not running\n", label)
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

func runUpdate() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	u := update.NewUpdater(version, logger)

	fmt.Printf("onWatch v%s — checking for updates...\n", version)

	info, err := u.Check()
	if err != nil {
		return fmt.Errorf("update check failed: %w", err)
	}

	if !info.Available {
		fmt.Printf("Already at the latest version (v%s)\n", version)
		return nil
	}

	fmt.Printf("Update available: v%s → v%s\n", info.CurrentVersion, info.LatestVersion)
	fmt.Printf("Downloading from %s\n", info.DownloadURL)

	if err := u.Apply(); err != nil {
		return fmt.Errorf("update failed: %w", err)
	}

	fmt.Printf("Updated successfully to v%s\n", info.LatestVersion)

	// If a daemon is running, stop it and start a fresh one
	if data, err := os.ReadFile(pidFile); err == nil {
		content := strings.TrimSpace(string(data))
		var pid int
		if strings.Contains(content, ":") {
			parts := strings.Split(content, ":")
			if len(parts) >= 1 {
				pid, _ = strconv.Atoi(parts[0])
			}
		} else {
			pid, _ = strconv.Atoi(content)
		}
		if pid > 0 && pid != os.Getpid() {
			fmt.Println("Restarting daemon...")
			// Stop old daemon
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Signal(syscall.SIGTERM)
				time.Sleep(1 * time.Second)
			}
			// Start new daemon with the updated binary (no args = daemonize with .env config)
			exePath, err := os.Executable()
			if err == nil {
				exePath, _ = filepath.EvalSymlinks(exePath)
				cmd := exec.Command(exePath)
				cmd.Env = os.Environ()
				if err := cmd.Start(); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: restart failed: %v\n", err)
					fmt.Println("Please restart onwatch manually.")
				} else {
					fmt.Printf("New daemon started (PID %d)\n", cmd.Process.Pid)
				}
			}
		}
	}

	return nil
}

func printBanner(cfg *config.Config, version string) {
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Printf("║  onWatch v%-26s ║\n", version)
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
	if cfg.HasProvider("anthropic") {
		if cfg.AnthropicAutoToken {
			fmt.Println("║  API:       anthropic (auto-detect)  ║")
		} else {
			fmt.Println("║  API:       anthropic.com/usage      ║")
		}
	}

	fmt.Printf("║  Polling:   every %s              ║\n", cfg.PollInterval)
	fmt.Printf("║  Dashboard: http://localhost:%d    ║\n", cfg.Port)
	fmt.Printf("║  Database:  %-24s ║\n", cfg.DBPath)
	fmt.Printf("║  Auth:      %s / ****             ║\n", cfg.AdminUser)
	if cfg.TestMode {
		fmt.Println("║  Mode:      TEST (isolated)          ║")
	}
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Println()

	// Show API keys
	if cfg.HasProvider("synthetic") {
		fmt.Printf("Synthetic API Key: %s\n", redactAPIKey(cfg.SyntheticAPIKey))
	}
	if cfg.HasProvider("zai") {
		fmt.Printf("Z.ai API Key:      %s\n", redactAPIKey(cfg.ZaiAPIKey))
	}
	if cfg.HasProvider("anthropic") {
		label := "Anthropic Token:   "
		if cfg.AnthropicAutoToken {
			label = "Anthropic (auto):  "
		}
		fmt.Printf("%s%s\n", label, redactAPIKey(cfg.AnthropicToken))
	}
	fmt.Println()
}

func printHelp() {
	fmt.Println("onWatch - Multi-Provider API Usage Tracker")
	fmt.Println()
	fmt.Println("Usage: onwatch [COMMAND] [OPTIONS]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  stop, --stop       Stop the running onwatch instance")
	fmt.Println("  status, --status   Show status of the running instance")
	fmt.Println("  update, --update   Check for updates and self-update")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  version, --version Print version and exit")
	fmt.Println("  --help             Print this help message")
	fmt.Println("  --interval SEC     Polling interval in seconds (default: 60)")
	fmt.Println("  --port PORT        Dashboard HTTP port (default: 9211)")
	fmt.Println("  --db PATH          SQLite database file path (default: ~/.onwatch/data/onwatch.db)")
	fmt.Println("  --debug            Run in foreground mode, log to stdout")
	fmt.Println("  --test             Test mode: isolated PID/log files, won't affect production")
	fmt.Println()
	fmt.Println("Environment Variables:")
	fmt.Println("  SYNTHETIC_API_KEY       Synthetic API key (configure at least one provider)")
	fmt.Println("  ZAI_API_KEY            Z.ai API key")
	fmt.Println("  ZAI_BASE_URL           Z.ai base URL (default: https://api.z.ai/api)")
	fmt.Println("  ANTHROPIC_TOKEN         Anthropic token (auto-detected if not set)")
	fmt.Println("  ONWATCH_POLL_INTERVAL   Polling interval in seconds")
	fmt.Println("  ONWATCH_PORT            Dashboard HTTP port")
	fmt.Println("  ONWATCH_ADMIN_USER      Dashboard admin username")
	fmt.Println("  ONWATCH_ADMIN_PASS      Dashboard admin password")
	fmt.Println("  ONWATCH_DB_PATH         SQLite database file path")
	fmt.Println("  ONWATCH_LOG_LEVEL       Log level: debug, info, warn, error")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  onwatch                           # Run in background mode")
	fmt.Println("  onwatch --debug                   # Run in foreground mode")
	fmt.Println("  onwatch --interval 30 --port 8080 # Custom interval and port")
	fmt.Println("  onwatch stop                      # Stop running instance")
	fmt.Println("  onwatch --stop                    # Same as 'stop'")
	fmt.Println("  onwatch status                    # Check if running")
	fmt.Println("  onwatch --status                  # Same as 'status'")
	fmt.Println("  onwatch update                    # Check for updates and self-update")
	fmt.Println("  onwatch --test --debug            # Run test instance (isolated)")
	fmt.Println("  onwatch --test stop               # Stop only test instance")
	fmt.Println("  onwatch --test status             # Check test instance status")
	fmt.Println()
	fmt.Println("Test Mode (--test):")
	fmt.Println("  Uses separate PID file (onwatch-test.pid) and log file (.onwatch-test.log).")
	fmt.Println("  Test instances never kill production instances and vice versa.")
	fmt.Println("  Use --db and --port to further isolate test from production.")
	fmt.Println()
	fmt.Println("Configure providers in .env file or environment variables.")
	fmt.Println("At least one provider (Synthetic, Z.ai, or Anthropic) must be configured.")
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
