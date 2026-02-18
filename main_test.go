package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/onllm-dev/onwatch/internal/config"
)

func discardMainLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestPrimeCodexTokenFromAuth_AllowsConfigLoadWithOnlyCodexAuth(t *testing.T) {
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

	autoDetected := primeCodexTokenFromAuth(discardMainLogger())
	if !autoDetected {
		t.Fatal("primeCodexTokenFromAuth() should auto-detect token")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() failed: %v", err)
	}
	if cfg.CodexToken != "codex_oauth_access" {
		t.Fatalf("CodexToken = %q, want codex_oauth_access", cfg.CodexToken)
	}
	if !cfg.HasProvider("codex") {
		t.Fatal("HasProvider('codex') should be true after priming")
	}
}
