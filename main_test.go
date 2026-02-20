package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onllm-dev/onwatch/internal/config"
)

func TestConfigLoad_WithOnlyCodexAuthFile_ReturnsValidationError(t *testing.T) {
	homeDir := t.TempDir()
	codexHome := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("SYNTHETIC_API_KEY", "")
	t.Setenv("ZAI_API_KEY", "")
	t.Setenv("ANTHROPIC_TOKEN", "")
	t.Setenv("COPILOT_TOKEN", "")
	t.Setenv("CODEX_TOKEN", "")

	authPath := filepath.Join(codexHome, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"codex_oauth_access"}}`), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	_, err := config.Load()
	if err == nil {
		t.Fatal("config.Load() should fail without explicit provider env vars")
	}
	if !strings.Contains(err.Error(), "at least one provider must be configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}
