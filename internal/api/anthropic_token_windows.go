//go:build windows

package api

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

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
