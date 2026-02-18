package api

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func discardLoggerCredentials() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestDetectCodexCredentials_ParsesOAuthTokens(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{
		"tokens": {
			"access_token": "oauth_access",
			"refresh_token": "oauth_refresh",
			"id_token": "oauth_id",
			"account_id": "acct_123"
		}
	}`), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	creds := DetectCodexCredentials(discardLoggerCredentials())
	if creds == nil {
		t.Fatal("DetectCodexCredentials returned nil")
	}
	if creds.AccessToken != "oauth_access" {
		t.Fatalf("AccessToken = %q, want oauth_access", creds.AccessToken)
	}
	if creds.RefreshToken != "oauth_refresh" {
		t.Fatalf("RefreshToken = %q, want oauth_refresh", creds.RefreshToken)
	}
	if creds.IDToken != "oauth_id" {
		t.Fatalf("IDToken = %q, want oauth_id", creds.IDToken)
	}
	if creds.AccountID != "acct_123" {
		t.Fatalf("AccountID = %q, want acct_123", creds.AccountID)
	}
}

func TestDetectCodexCredentials_ParsesAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")

	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}

	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"OPENAI_API_KEY":"sk-openai-key"}`), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	creds := DetectCodexCredentials(discardLoggerCredentials())
	if creds == nil {
		t.Fatal("DetectCodexCredentials returned nil")
	}
	if creds.APIKey != "sk-openai-key" {
		t.Fatalf("APIKey = %q, want sk-openai-key", creds.APIKey)
	}
}

func TestDetectCodexToken_PrefersAccessToken(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{
		"OPENAI_API_KEY": "sk-openai-key",
		"tokens": {"access_token": "oauth_access"}
	}`), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	token := DetectCodexToken(discardLoggerCredentials())
	if token != "oauth_access" {
		t.Fatalf("DetectCodexToken() = %q, want oauth_access", token)
	}
}

func TestDetectCodexToken_RejectsAPIKeyOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")

	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}

	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"OPENAI_API_KEY":"sk-openai-key"}`), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	token := DetectCodexToken(discardLoggerCredentials())
	if token != "" {
		t.Fatalf("DetectCodexToken() = %q, want empty", token)
	}
}
