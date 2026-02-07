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
	if u, err := user.Current(); err == nil {
		username = u.Username
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
