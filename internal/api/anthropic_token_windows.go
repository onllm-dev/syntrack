//go:build windows

package api

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// detectAnthropicCredentialsPlatform tries to detect full OAuth credentials on Windows.
func detectAnthropicCredentialsPlatform(logger *slog.Logger) *AnthropicCredentials {
	if logger == nil {
		logger = slog.Default()
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	credPath := filepath.Join(home, ".claude", ".credentials.json")
	data, err := os.ReadFile(credPath)
	if err != nil {
		return nil
	}
	creds, err := parseFullClaudeCredentials(data)
	if err != nil || creds == nil {
		return nil
	}
	logger.Debug("Full Anthropic credentials detected", "path", credPath)
	return creds
}

// WriteAnthropicCredentials updates the credentials file with new OAuth tokens on Windows.
// Creates a backup before modifying. Uses atomic write (temp file + rename).
// Preserves existing fields (scopes, subscriptionType, etc.) from the original file.
func WriteAnthropicCredentials(accessToken, refreshToken string, expiresIn int) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	credPath := filepath.Join(home, ".claude", ".credentials.json")
	data, err := os.ReadFile(credPath)
	if err != nil {
		return err
	}

	// Create backup before modifying
	backupPath := credPath + ".bak"
	_ = os.WriteFile(backupPath, data, 0600)

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

	// Atomic write: temp file + rename
	tmpPath := credPath + ".tmp"
	if err := os.WriteFile(tmpPath, newData, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, credPath)
}

// detectAnthropicTokenPlatform reads credentials from file on Windows.
func detectAnthropicTokenPlatform(logger *slog.Logger) string {
	if logger == nil {
		logger = slog.Default()
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	credPath := filepath.Join(home, ".claude", ".credentials.json")
	data, err := os.ReadFile(credPath)
	if err != nil {
		return ""
	}
	token, err := parseClaudeCredentials(data)
	if err == nil && token != "" {
		logger.Info("Anthropic token auto-detected from credentials file", "path", credPath)
		return strings.TrimSpace(token)
	}

	return ""
}
