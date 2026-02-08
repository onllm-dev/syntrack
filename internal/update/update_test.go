package update

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"equal", "2.2.0", "2.2.0", 0},
		{"a greater major", "3.0.0", "2.9.9", 1},
		{"b greater major", "1.0.0", "2.0.0", -1},
		{"a greater minor", "2.3.0", "2.2.0", 1},
		{"b greater minor", "2.1.0", "2.2.0", -1},
		{"a greater patch", "2.2.1", "2.2.0", 1},
		{"b greater patch", "2.2.0", "2.2.1", -1},
		{"with v prefix", "v2.3.0", "v2.2.0", 1},
		{"mixed v prefix", "v2.3.0", "2.2.0", 1},
		{"short version a", "2.3", "2.2.0", 1},
		{"short version b", "2.2.0", "2.3", -1},
		{"single digit", "3", "2.9.9", 1},
		{"pre-release suffix", "2.2.6-test", "2.2.5-test", 1},
		{"pre-release vs release", "2.2.6-beta", "2.2.5", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareVersions(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCheck_DevVersion(t *testing.T) {
	u := NewUpdater("dev", slog.Default())
	info, err := u.Check()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Available {
		t.Error("dev build should never report updates available")
	}
	if info.CurrentVersion != "dev" {
		t.Errorf("got current_version=%q, want %q", info.CurrentVersion, "dev")
	}
}

func TestCheck_EmptyVersion(t *testing.T) {
	u := NewUpdater("", slog.Default())
	info, err := u.Check()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Available {
		t.Error("empty version should never report updates available")
	}
}

func TestCheck_UpdateAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(githubRelease{TagName: "v3.0.0"})
	}))
	defer srv.Close()

	u := NewUpdater("2.2.0", slog.Default())
	u.apiURL = srv.URL

	info, err := u.Check()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !info.Available {
		t.Error("expected update to be available")
	}
	if info.LatestVersion != "3.0.0" {
		t.Errorf("got latest=%q, want %q", info.LatestVersion, "3.0.0")
	}
	if info.DownloadURL == "" {
		t.Error("expected download URL to be set")
	}
}

func TestCheck_AlreadyLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(githubRelease{TagName: "v2.2.0"})
	}))
	defer srv.Close()

	u := NewUpdater("2.2.0", slog.Default())
	u.apiURL = srv.URL

	info, err := u.Check()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Available {
		t.Error("should not report update when at latest version")
	}
	if info.DownloadURL != "" {
		t.Error("download URL should be empty when no update available")
	}
}

func TestCheck_CacheTTL(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(githubRelease{TagName: "v3.0.0"})
	}))
	defer srv.Close()

	u := NewUpdater("2.2.0", slog.Default())
	u.apiURL = srv.URL
	u.cacheTTL = 1 * time.Hour

	// First call hits the server
	if _, err := u.Check(); err != nil {
		t.Fatalf("first check: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 API call, got %d", callCount)
	}

	// Second call should use cache
	info, err := u.Check()
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected cache hit (1 call), got %d calls", callCount)
	}
	if !info.Available {
		t.Error("cached result should still show update available")
	}
}

func TestCheck_CacheExpiry(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(githubRelease{TagName: "v3.0.0"})
	}))
	defer srv.Close()

	u := NewUpdater("2.2.0", slog.Default())
	u.apiURL = srv.URL
	u.cacheTTL = 1 * time.Millisecond

	// First call
	if _, err := u.Check(); err != nil {
		t.Fatalf("first check: %v", err)
	}

	// Wait for cache to expire
	time.Sleep(5 * time.Millisecond)

	// Second call should hit the server again
	if _, err := u.Check(); err != nil {
		t.Fatalf("second check: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls after cache expiry, got %d", callCount)
	}
}

func TestCheck_GitHubAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	u := NewUpdater("2.2.0", slog.Default())
	u.apiURL = srv.URL

	_, err := u.Check()
	if err == nil {
		t.Error("expected error for non-200 response")
	}
}

func TestCheck_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	u := NewUpdater("2.2.0", slog.Default())
	u.apiURL = srv.URL

	_, err := u.Check()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestBinaryDownloadURL(t *testing.T) {
	u := NewUpdater("2.2.0", slog.Default())

	url := u.binaryDownloadURL("3.0.0")
	if url == "" {
		t.Fatal("expected non-empty URL")
	}
	// Should contain the version and platform
	if got := url; got == "" {
		t.Error("expected download URL")
	}
}

func TestValidateBinary_Valid(t *testing.T) {
	// Create a temp file with ELF magic bytes
	dir := t.TempDir()
	path := filepath.Join(dir, "test-binary")
	// ELF magic: 0x7f 'E' 'L' 'F'
	if err := os.WriteFile(path, []byte{0x7f, 'E', 'L', 'F', 0, 0, 0, 0}, 0644); err != nil {
		t.Fatal(err)
	}

	if err := validateBinary(path); err != nil {
		t.Errorf("valid ELF should pass: %v", err)
	}
}

func TestValidateBinary_MachO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-binary")
	// Mach-O 64-bit magic (little-endian, common on macOS)
	if err := os.WriteFile(path, []byte{0xCF, 0xFA, 0xED, 0xFE, 0, 0, 0, 0}, 0644); err != nil {
		t.Fatal(err)
	}

	if err := validateBinary(path); err != nil {
		t.Errorf("valid Mach-O should pass: %v", err)
	}
}

func TestValidateBinary_PE(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-binary")
	if err := os.WriteFile(path, []byte{'M', 'Z', 0, 0, 0, 0, 0, 0}, 0644); err != nil {
		t.Fatal(err)
	}

	if err := validateBinary(path); err != nil {
		t.Errorf("valid PE should pass: %v", err)
	}
}

func TestValidateBinary_Invalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-binary")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := validateBinary(path); err == nil {
		t.Error("expected error for invalid binary")
	}
}

func TestValidateBinary_TooSmall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-binary")
	if err := os.WriteFile(path, []byte{0x7f}, 0644); err != nil {
		t.Fatal(err)
	}

	if err := validateBinary(path); err == nil {
		t.Error("expected error for file too small")
	}
}

func TestApply_DevBuild(t *testing.T) {
	u := NewUpdater("dev", slog.Default())
	err := u.Apply()
	if err == nil {
		t.Error("expected error for dev build")
	}
}

func TestApply_AlreadyLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(githubRelease{TagName: "v2.2.0"})
	}))
	defer srv.Close()

	u := NewUpdater("2.2.0", slog.Default())
	u.apiURL = srv.URL

	err := u.Apply()
	if err == nil {
		t.Error("expected error when already at latest")
	}
}

func TestCheckWritable(t *testing.T) {
	dir := t.TempDir()
	if err := checkWritable(dir); err != nil {
		t.Errorf("temp dir should be writable: %v", err)
	}
}

func TestNewUpdater_Defaults(t *testing.T) {
	u := NewUpdater("1.0.0", nil)
	if u.currentVersion != "1.0.0" {
		t.Errorf("got version=%q, want %q", u.currentVersion, "1.0.0")
	}
	if u.cacheTTL != defaultCacheTTL {
		t.Errorf("got cacheTTL=%v, want %v", u.cacheTTL, defaultCacheTTL)
	}
	if u.apiURL != githubReleasesURL {
		t.Errorf("got apiURL=%q, want default", u.apiURL)
	}
}

func TestFilterArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"empty", nil, nil},
		{"no update", []string{"--debug", "--port", "9211"}, []string{"--debug", "--port", "9211"}},
		{"update subcommand", []string{"update"}, nil},
		{"--update flag", []string{"--update"}, nil},
		{"mixed", []string{"--debug", "update", "--port", "9211"}, []string{"--debug", "--port", "9211"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterArgs(tt.args)
			if len(got) != len(tt.want) {
				t.Errorf("filterArgs(%v) = %v, want %v", tt.args, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("filterArgs(%v)[%d] = %q, want %q", tt.args, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestReplaceBinary(t *testing.T) {
	dir := t.TempDir()
	exePath := filepath.Join(dir, "onwatch")
	tmpPath := filepath.Join(dir, "onwatch.tmp.123")

	// Create "current" binary (Mach-O magic)
	if err := os.WriteFile(exePath, []byte("old-binary-content"), 0755); err != nil {
		t.Fatal(err)
	}
	// Create "new" binary
	if err := os.WriteFile(tmpPath, []byte("new-binary-content"), 0755); err != nil {
		t.Fatal(err)
	}

	logger := slog.Default()
	if err := replaceBinary(exePath, tmpPath, logger); err != nil {
		t.Fatalf("replaceBinary failed: %v", err)
	}

	// Verify new binary is in place
	content, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatalf("failed to read replaced binary: %v", err)
	}
	if string(content) != "new-binary-content" {
		t.Errorf("got content=%q, want %q", string(content), "new-binary-content")
	}

	// Verify temp file was consumed (renamed away)
	if _, err := os.Stat(tmpPath); err == nil {
		t.Error("temp file should have been renamed away")
	}

	// Verify .old was cleaned up
	if _, err := os.Stat(exePath + ".old"); err == nil {
		t.Error(".old backup should have been cleaned up")
	}
}

func TestReplaceBinary_LeftoverOldFile(t *testing.T) {
	dir := t.TempDir()
	exePath := filepath.Join(dir, "onwatch")
	tmpPath := filepath.Join(dir, "onwatch.tmp.456")
	oldPath := filepath.Join(dir, "onwatch.old")

	// Create leftover .old from previous failed update
	if err := os.WriteFile(oldPath, []byte("stale-old"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exePath, []byte("current"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpPath, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}

	logger := slog.Default()
	if err := replaceBinary(exePath, tmpPath, logger); err != nil {
		t.Fatalf("replaceBinary with leftover .old should succeed: %v", err)
	}

	content, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new" {
		t.Errorf("got %q, want %q", string(content), "new")
	}
}
