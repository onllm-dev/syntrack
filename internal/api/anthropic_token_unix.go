//go:build !windows

package api

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// getCredentialsFilePath returns the path to the Claude credentials file.
func getCredentialsFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		if u, err := user.Current(); err == nil {
			home = u.HomeDir
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", ".credentials.json")
}

// detectAnthropicTokenPlatform tries platform-specific credential stores.
func detectAnthropicTokenPlatform(logger *slog.Logger) string {
	if logger == nil {
		logger = slog.Default()
	}

	username := ""
	var homeDir string
	if u, err := user.Current(); err == nil {
		username = u.Username
		homeDir = u.HomeDir
	}

	// macOS: try Keychain
	if runtime.GOOS == "darwin" && username != "" {
		out, err := exec.Command("security", "find-generic-password",
			"-s", "Claude Code-credentials",
			"-a", username,
			"-w").Output()
		if err == nil {
			token, err := parseClaudeCredentials(out)
			if err == nil && token != "" {
				logger.Info("Anthropic token auto-detected from macOS Keychain")
				return token
			}
		}
	}

	// Linux: try secret-tool (GNOME Keyring)
	if runtime.GOOS == "linux" && username != "" {
		out, err := exec.Command("secret-tool", "lookup",
			"service", "Claude Code-credentials",
			"account", username).Output()
		if err == nil {
			token, err := parseClaudeCredentials(out)
			if err == nil && token != "" {
				logger.Info("Anthropic token auto-detected from Linux keyring")
				return token
			}
		}
	}

	// File fallback: ~/.claude/.credentials.json
	// Use os.UserHomeDir() first ($HOME), then fall back to user.Current().HomeDir
	// (handles systemd services where $HOME is not set)
	home, err := os.UserHomeDir()
	if err != nil {
		home = homeDir // fallback to passwd-based home from user.Current()
	}
	if home == "" {
		logger.Debug("Cannot determine home directory for credential file lookup")
		return ""
	}
	credPath := filepath.Join(home, ".claude", ".credentials.json")
	data, err := os.ReadFile(credPath)
	if err != nil {
		logger.Debug("Credential file not readable", "path", credPath, "error", err)
		return ""
	}
	token, err := parseClaudeCredentials(data)
	if err != nil {
		logger.Debug("Failed to parse credentials", "path", credPath, "error", err)
		return ""
	}
	if token == "" {
		logger.Debug("Credentials file has empty access token", "path", credPath)
		return ""
	}
	logger.Info("Anthropic token auto-detected from credentials file", "path", credPath)
	return strings.TrimSpace(token)
}

// detectAnthropicCredentialsPlatform tries to detect full OAuth credentials.
// Currently only supports file-based credentials (not keychain).
func detectAnthropicCredentialsPlatform(logger *slog.Logger) *AnthropicCredentials {
	if logger == nil {
		logger = slog.Default()
	}

	credPath := getCredentialsFilePath()
	if credPath == "" {
		logger.Debug("Cannot determine credentials file path")
		return nil
	}

	data, err := os.ReadFile(credPath)
	if err != nil {
		logger.Debug("Credential file not readable", "path", credPath, "error", err)
		return nil
	}

	creds, err := parseFullClaudeCredentials(data)
	if err != nil {
		logger.Debug("Failed to parse full credentials", "path", credPath, "error", err)
		return nil
	}
	if creds == nil {
		logger.Debug("Credentials file has no OAuth data", "path", credPath)
		return nil
	}

	logger.Debug("Full Anthropic credentials detected",
		"path", credPath,
		"expires_in", creds.ExpiresIn.Round(time.Minute),
		"has_refresh_token", creds.RefreshToken != "",
	)
	return creds
}

// WriteAnthropicCredentials updates the credentials file with new OAuth tokens.
// Creates a backup (.credentials.json.bak) before modifying to prevent data loss.
// Uses atomic write (temp file + rename) to prevent corruption.
// Preserves existing fields (scopes, subscriptionType, etc.) from the original file.
func WriteAnthropicCredentials(accessToken, refreshToken string, expiresIn int) error {
	credPath := getCredentialsFilePath()
	if credPath == "" {
		return os.ErrNotExist
	}

	// Read existing credentials to preserve other fields
	data, err := os.ReadFile(credPath)
	if err != nil {
		return err
	}

	// Create backup BEFORE modifying (overwrites previous backup)
	backupPath := credPath + ".bak"
	if err := os.WriteFile(backupPath, data, 0600); err != nil {
		// Log but don't fail - backup is nice-to-have, not critical
		// The atomic write still protects against corruption
	}

	// Parse into a map to preserve unknown fields
	var rawCreds map[string]interface{}
	if err := json.Unmarshal(data, &rawCreds); err != nil {
		return err
	}

	// Get or create claudeAiOauth section
	oauth, ok := rawCreds["claudeAiOauth"].(map[string]interface{})
	if !ok {
		oauth = make(map[string]interface{})
		rawCreds["claudeAiOauth"] = oauth
	}

	// Update tokens and expiry
	oauth["accessToken"] = accessToken
	oauth["refreshToken"] = refreshToken
	oauth["expiresAt"] = time.Now().Add(time.Duration(expiresIn) * time.Second).UnixMilli()

	// Marshal back to JSON
	newData, err := json.Marshal(rawCreds)
	if err != nil {
		return err
	}

	// Atomic write: write to temp file, then rename
	tempPath := credPath + ".tmp"
	if err := os.WriteFile(tempPath, newData, 0600); err != nil {
		return err
	}

	return os.Rename(tempPath, credPath)
}
