package update

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	githubReleasesURL = "https://api.github.com/repos/onllm-dev/onwatch/releases/latest"
	downloadBaseURL   = "https://github.com/onllm-dev/onwatch/releases/download"
	defaultCacheTTL   = 1 * time.Hour
)

// UpdateInfo holds the result of a version check.
type UpdateInfo struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	DownloadURL    string `json:"download_url,omitempty"`
}

// Updater checks for and applies self-updates from GitHub releases.
type Updater struct {
	currentVersion string
	logger         *slog.Logger
	httpClient     *http.Client

	mu            sync.Mutex
	cachedVersion string
	cachedAt      time.Time
	cacheTTL      time.Duration

	// Set by Apply() for Restart() to use (avoids /proc/self/exe issues)
	lastAppliedPath string

	// For testing: override the GitHub API URL and download base URL
	apiURL      string
	downloadURL string
}

// NewUpdater creates a new Updater with the given version and logger.
func NewUpdater(version string, logger *slog.Logger) *Updater {
	if logger == nil {
		logger = slog.Default()
	}
	return &Updater{
		currentVersion: version,
		logger:         logger,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		cacheTTL:    defaultCacheTTL,
		apiURL:      githubReleasesURL,
		downloadURL: downloadBaseURL,
	}
}

// githubRelease is a minimal struct for parsing the GitHub API response.
type githubRelease struct {
	TagName string `json:"tag_name"`
}

// Check queries GitHub for the latest release and compares with current version.
// Results are cached for cacheTTL duration.
func (u *Updater) Check() (UpdateInfo, error) {
	info := UpdateInfo{
		CurrentVersion: u.currentVersion,
	}

	// Dev builds can't update
	if u.currentVersion == "dev" || u.currentVersion == "" {
		return info, nil
	}

	// Check cache
	u.mu.Lock()
	if u.cachedVersion != "" && time.Since(u.cachedAt) < u.cacheTTL {
		latest := u.cachedVersion
		u.mu.Unlock()

		info.LatestVersion = latest
		info.Available = compareVersions(latest, u.currentVersion) > 0
		if info.Available {
			info.DownloadURL = u.binaryDownloadURL(latest)
		}
		return info, nil
	}
	u.mu.Unlock()

	// Fetch from GitHub
	req, err := http.NewRequest("GET", u.apiURL, nil)
	if err != nil {
		return info, fmt.Errorf("update.Check: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "onwatch/"+u.currentVersion)

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return info, fmt.Errorf("update.Check: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("update.Check: GitHub API returned %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return info, fmt.Errorf("update.Check: %w", err)
	}

	latest := strings.TrimPrefix(release.TagName, "v")

	// Update cache
	u.mu.Lock()
	u.cachedVersion = latest
	u.cachedAt = time.Now()
	u.mu.Unlock()

	info.LatestVersion = latest
	info.Available = compareVersions(latest, u.currentVersion) > 0
	if info.Available {
		info.DownloadURL = u.binaryDownloadURL(latest)
	}

	u.logger.Info("Version check complete",
		"current", u.currentVersion,
		"latest", latest,
		"available", info.Available)

	return info, nil
}

// Apply downloads the latest binary and replaces the current one.
// On Unix, uses remove+rename (safe for running binaries since the kernel
// keeps the inode alive). Falls back to backup-rename on Windows.
func (u *Updater) Apply() error {
	if u.currentVersion == "dev" || u.currentVersion == "" {
		return fmt.Errorf("update.Apply: cannot update dev build")
	}

	// Force a fresh check (bypass cache) to avoid stale version data
	u.mu.Lock()
	u.cachedVersion = ""
	u.cachedAt = time.Time{}
	u.mu.Unlock()

	info, err := u.Check()
	if err != nil {
		return fmt.Errorf("update.Apply: %w", err)
	}
	if !info.Available {
		return fmt.Errorf("update.Apply: already at latest version %s", u.currentVersion)
	}

	// Get current binary path
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("update.Apply: os.Executable: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("update.Apply: EvalSymlinks(%s): %w", exePath, err)
	}

	// Check write permission
	exeDir := filepath.Dir(exePath)
	if err := checkWritable(exeDir); err != nil {
		return fmt.Errorf("update.Apply: directory %s not writable: %w", exeDir, err)
	}

	u.logger.Info("Applying update",
		"from", u.currentVersion,
		"to", info.LatestVersion,
		"binary", exePath,
		"url", info.DownloadURL)

	// Download to temp file in same directory (required for atomic rename)
	tmpFile, err := os.CreateTemp(exeDir, "onwatch.tmp.*")
	if err != nil {
		return fmt.Errorf("update.Apply: CreateTemp in %s: %w", exeDir, err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // cleanup on error

	// Stream download (2 min timeout for large binaries on slow connections)
	dlClient := &http.Client{Timeout: 2 * time.Minute}
	resp, err := dlClient.Get(info.DownloadURL)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("update.Apply: download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmpFile.Close()
		return fmt.Errorf("update.Apply: download returned HTTP %d", resp.StatusCode)
	}

	written, err := io.Copy(tmpFile, resp.Body)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("update.Apply: download write failed: %w", err)
	}
	tmpFile.Close()

	if written == 0 {
		return fmt.Errorf("update.Apply: downloaded file is empty")
	}

	u.logger.Info("Download complete", "bytes", written, "path", tmpPath)

	// Validate: check magic bytes (ELF, Mach-O, or PE)
	if err := validateBinary(tmpPath); err != nil {
		return fmt.Errorf("update.Apply: %w", err)
	}

	// Set executable permission
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("update.Apply: chmod: %w", err)
	}

	// Replace the binary.
	// Strategy 1 (Unix): remove current binary then rename temp into place.
	// On Unix, deleting a running binary is safe — the kernel keeps the inode
	// alive until all file descriptors are closed (i.e., until this process exits).
	// Strategy 2 (Windows fallback): rename current to .old, rename temp to current.
	if err := replaceBinary(exePath, tmpPath, u.logger); err != nil {
		return fmt.Errorf("update.Apply: %w", err)
	}

	// Store path for Restart() — after Apply, /proc/self/exe may show "(deleted)"
	u.mu.Lock()
	u.lastAppliedPath = exePath
	u.mu.Unlock()

	u.logger.Info("Update applied successfully",
		"from", u.currentVersion,
		"to", info.LatestVersion)

	// Fix systemd unit file NOW, while we're still alive and before Restart().
	// This ensures the unit has Restart=always before the process exits,
	// so systemd will restart the service regardless of how Restart() works.
	MigrateSystemdUnit(u.logger)

	return nil
}

// replaceBinary replaces the binary at exePath with the one at tmpPath.
// Tries remove+rename first (works on Unix), falls back to backup-rename (Windows).
func replaceBinary(exePath, tmpPath string, logger *slog.Logger) error {
	// Clean up any leftover .old file from a previous failed update
	backupPath := exePath + ".old"
	os.Remove(backupPath)

	// Strategy 1: Remove current, move new into place (Unix-safe)
	if err := os.Remove(exePath); err == nil {
		if err := os.Rename(tmpPath, exePath); err != nil {
			logger.Error("CRITICAL: removed old binary but failed to place new one",
				"exePath", exePath, "tmpPath", tmpPath, "error", err)
			return fmt.Errorf("replace failed after remove: %w (binary may be missing, restore from %s)", err, tmpPath)
		}
		return nil
	}

	// Strategy 2: Backup rename (required on Windows where running binaries can't be deleted)
	logger.Info("Remove failed, trying backup-rename strategy", "path", exePath)
	if err := os.Rename(exePath, backupPath); err != nil {
		return fmt.Errorf("backup rename %s → %s: %w", exePath, backupPath, err)
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		// Try to restore backup
		os.Rename(backupPath, exePath)
		return fmt.Errorf("swap rename %s → %s: %w", tmpPath, exePath, err)
	}
	// Best-effort cleanup
	os.Remove(backupPath)
	return nil
}

// IsSystemd returns true if the process is managed by systemd.
// Detected via INVOCATION_ID environment variable which systemd sets for all services.
func IsSystemd() bool {
	return os.Getenv("INVOCATION_ID") != ""
}

// DetectServiceName reads /proc/self/cgroup to find the systemd service name.
// Falls back to "onwatch.service" if detection fails.
func DetectServiceName() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			// cgroup v2 line: "0::/system.slice/onwatch.service"
			if idx := strings.LastIndex(line, "/"); idx >= 0 {
				unit := strings.TrimSpace(line[idx+1:])
				if strings.HasSuffix(unit, ".service") {
					return unit
				}
			}
		}
	}
	return "onwatch.service"
}

// findUnitFile locates the systemd unit file on disk.
// Checks system-level first (/etc/systemd/system/), then user-level (~/.config/systemd/user/).
func findUnitFile(serviceName string) string {
	systemPath := filepath.Join("/etc/systemd/system", serviceName)
	if _, err := os.Stat(systemPath); err == nil {
		return systemPath
	}

	if home, err := os.UserHomeDir(); err == nil {
		userPath := filepath.Join(home, ".config", "systemd", "user", serviceName)
		if _, err := os.Stat(userPath); err == nil {
			return userPath
		}
	}

	return ""
}

// MigrateSystemdUnit auto-fixes the systemd unit file with current best practices.
//
// IMPORTANT: Must be called BEFORE stopPreviousInstance() in main.go.
// When a post-update child process runs this before killing the parent,
// the daemon-reload completes while systemd still tracks the parent PID.
// After the child kills the parent, systemd uses the UPDATED Restart=always
// policy and automatically starts a fresh instance of the new binary.
//
// This is the mechanism that makes self-updates work under systemd with
// zero manual intervention — even when the OLD binary's Restart() code
// used the broken spawn-and-kill approach.
//
// Safe to call on every startup — no-op if already up to date or not under systemd.
func MigrateSystemdUnit(logger *slog.Logger) {
	if !IsSystemd() {
		return
	}

	serviceName := DetectServiceName()
	unitPath := findUnitFile(serviceName)
	if unitPath == "" {
		return
	}

	content, err := os.ReadFile(unitPath)
	if err != nil {
		logger.Warn("Could not read systemd unit file", "path", unitPath, "error", err)
		return
	}

	original := string(content)
	updated := original

	// Migration 1: Restart=on-failure → Restart=always
	// Ensures service restarts after self-update (old code exited with 0)
	updated = strings.Replace(updated, "Restart=on-failure", "Restart=always", 1)

	// Migration 2: RestartSec=10 → RestartSec=5
	// Faster restart after update
	updated = strings.Replace(updated, "RestartSec=10", "RestartSec=5", 1)

	if updated == original {
		return // already up to date
	}

	if err := os.WriteFile(unitPath, []byte(updated), 0644); err != nil {
		logger.Warn("Could not update systemd unit file", "path", unitPath, "error", err)
		return
	}

	// Reload systemd configuration synchronously — MUST complete before
	// stopPreviousInstance() kills the parent, so systemd sees the new
	// Restart=always policy when the parent PID dies.
	var cmd *exec.Cmd
	if strings.HasPrefix(unitPath, "/etc/systemd/system") {
		cmd = exec.Command("systemctl", "daemon-reload")
	} else {
		cmd = exec.Command("systemctl", "--user", "daemon-reload")
	}
	if err := cmd.Run(); err != nil {
		logger.Warn("systemctl daemon-reload failed", "error", err)
		return
	}

	logger.Info("Migrated systemd unit file",
		"path", unitPath,
		"changes", "Restart=always, RestartSec=5")
}

// Restart handles restarting after an update.
// Under systemd: triggers `systemctl restart` so systemd manages the full lifecycle.
// Standalone: spawns the new binary which will stop the old instance via PID file.
func (u *Updater) Restart() error {
	if IsSystemd() {
		serviceName := DetectServiceName()
		u.logger.Info("Running under systemd — triggering service restart", "service", serviceName)

		// Run systemctl restart in a detached process.
		// systemctl will SIGTERM us, then start the new binary.
		cmd := exec.Command("systemctl", "restart", serviceName)
		if err := cmd.Start(); err != nil {
			u.logger.Warn("systemctl restart failed, falling back to exit", "error", err)
			os.Exit(0)
		}

		// Wait for systemd to kill us (it sends SIGTERM which our signal handler catches)
		u.logger.Info("Waiting for systemd to restart us...")
		time.Sleep(30 * time.Second)

		// Fallback: if systemd didn't kill us within 30s, exit anyway
		os.Exit(0)
		return nil // unreachable
	}

	// Standalone mode: spawn new process
	u.mu.Lock()
	exePath := u.lastAppliedPath
	u.mu.Unlock()

	if exePath == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("update.Restart: %w", err)
		}
		exePath = strings.TrimSuffix(exePath, " (deleted)")
	}

	args := filterArgs(os.Args[1:])
	cmd := exec.Command(exePath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("update.Restart: spawn %s: %w", exePath, err)
	}

	u.logger.Info("Spawned new process", "pid", cmd.Process.Pid, "path", exePath, "args", args)
	return nil
}

// filterArgs removes "update" and "--update" from args so the new process
// starts as a server, not as another update command.
func filterArgs(args []string) []string {
	var filtered []string
	for _, a := range args {
		if a != "update" && a != "--update" {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

// binaryDownloadURL constructs the download URL for the current platform.
func (u *Updater) binaryDownloadURL(version string) string {
	name := fmt.Sprintf("onwatch-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return fmt.Sprintf("%s/v%s/%s", u.downloadURL, version, name)
}

// compareVersions compares two semver strings.
// Returns: 1 if a > b, -1 if a < b, 0 if equal.
// Handles pre-release suffixes like "2.2.5-test" by extracting numeric parts.
func compareVersions(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")

	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")

	// Pad shorter version with zeros
	for len(partsA) < 3 {
		partsA = append(partsA, "0")
	}
	for len(partsB) < 3 {
		partsB = append(partsB, "0")
	}

	for i := 0; i < 3; i++ {
		numA := extractLeadingInt(partsA[i])
		numB := extractLeadingInt(partsB[i])
		if numA > numB {
			return 1
		}
		if numA < numB {
			return -1
		}
	}
	return 0
}

// extractLeadingInt parses the leading integer from a string like "5-test" → 5.
func extractLeadingInt(s string) int {
	// Split on hyphen first (pre-release suffix)
	if idx := strings.IndexByte(s, '-'); idx >= 0 {
		s = s[:idx]
	}
	n, _ := strconv.Atoi(s)
	return n
}

// checkWritable tests if the directory is writable by creating a temp file.
func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".onwatch-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// validateBinary checks if the file starts with valid executable magic bytes.
func validateBinary(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot open downloaded binary: %w", err)
	}
	defer f.Close()

	magic := make([]byte, 4)
	n, err := f.Read(magic)
	if err != nil || n < 4 {
		return fmt.Errorf("downloaded file too small to be a valid binary")
	}

	// ELF: 0x7f 'E' 'L' 'F'
	if magic[0] == 0x7f && magic[1] == 'E' && magic[2] == 'L' && magic[3] == 'F' {
		return nil
	}
	// Mach-O: 0xFE 0xED 0xFA 0xCE (32-bit) or 0xFE 0xED 0xFA 0xCF (64-bit)
	// or fat binary: 0xCA 0xFE 0xBA 0xBE
	if magic[0] == 0xFE && magic[1] == 0xED && magic[2] == 0xFA && (magic[3] == 0xCE || magic[3] == 0xCF) {
		return nil
	}
	if magic[0] == 0xCA && magic[1] == 0xFE && magic[2] == 0xBA && magic[3] == 0xBE {
		return nil
	}
	// Mach-O reverse byte order (little-endian)
	if (magic[0] == 0xCE || magic[0] == 0xCF) && magic[1] == 0xFA && magic[2] == 0xED && magic[3] == 0xFE {
		return nil
	}
	// PE (Windows): 'M' 'Z'
	if magic[0] == 'M' && magic[1] == 'Z' {
		return nil
	}

	return fmt.Errorf("downloaded file is not a valid executable (magic: %x)", magic)
}
