//go:build !windows

package api

import (
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

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
