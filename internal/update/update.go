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

// Apply downloads the latest binary and replaces the current one via atomic swap.
func (u *Updater) Apply() error {
	if u.currentVersion == "dev" || u.currentVersion == "" {
		return fmt.Errorf("update.Apply: cannot update dev build")
	}

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
		return fmt.Errorf("update.Apply: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("update.Apply: %w", err)
	}

	// Check write permission
	exeDir := filepath.Dir(exePath)
	if err := checkWritable(exeDir); err != nil {
		return fmt.Errorf("update.Apply: binary directory not writable: %w", err)
	}

	u.logger.Info("Downloading update",
		"version", info.LatestVersion,
		"url", info.DownloadURL)

	// Download to temp file in same directory (required for atomic rename)
	tmpFile, err := os.CreateTemp(exeDir, "onwatch.tmp.*")
	if err != nil {
		return fmt.Errorf("update.Apply: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // cleanup on error

	// Stream download
	dlClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := dlClient.Get(info.DownloadURL)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("update.Apply: download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmpFile.Close()
		return fmt.Errorf("update.Apply: download returned %d", resp.StatusCode)
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

	// Validate: check magic bytes (ELF, Mach-O, or PE)
	if err := validateBinary(tmpPath); err != nil {
		return fmt.Errorf("update.Apply: %w", err)
	}

	// Set executable permission
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("update.Apply: %w", err)
	}

	// Atomic swap: backup current, move new in place
	backupPath := exePath + ".old"
	if err := os.Rename(exePath, backupPath); err != nil {
		return fmt.Errorf("update.Apply: backup failed: %w", err)
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		// Try to restore backup
		os.Rename(backupPath, exePath)
		return fmt.Errorf("update.Apply: swap failed: %w", err)
	}

	// Best-effort cleanup of backup
	os.Remove(backupPath)

	u.logger.Info("Update applied successfully",
		"from", u.currentVersion,
		"to", info.LatestVersion)

	return nil
}

// Restart spawns the new binary and returns. The new process will call
// stopPreviousInstance on startup, which sends SIGTERM to us.
func (u *Updater) Restart() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("update.Restart: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("update.Restart: %w", err)
	}

	cmd := exec.Command(exePath, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("update.Restart: %w", err)
	}

	u.logger.Info("Spawned new process", "pid", cmd.Process.Pid)
	return nil
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
		numA, _ := strconv.Atoi(partsA[i])
		numB, _ := strconv.Atoi(partsB[i])
		if numA > numB {
			return 1
		}
		if numA < numB {
			return -1
		}
	}
	return 0
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
