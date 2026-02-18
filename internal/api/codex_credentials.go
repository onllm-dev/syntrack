package api

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// CodexCredentials contains parsed Codex auth state.
type CodexCredentials struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	APIKey       string
	AccountID    string
}

type codexAuthFile struct {
	OpenAIAPIKey string `json:"OPENAI_API_KEY"`
	Tokens       struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

// DetectCodexCredentials loads Codex credentials from CODEX_HOME/auth.json or ~/.codex/auth.json.
func DetectCodexCredentials(logger *slog.Logger) *CodexCredentials {
	if logger == nil {
		logger = slog.Default()
	}

	authPath := codexAuthPath()
	if authPath == "" {
		logger.Debug("Codex auth path unavailable")
		return nil
	}

	data, err := os.ReadFile(authPath)
	if err != nil {
		logger.Debug("Codex auth file not readable", "path", authPath, "error", err)
		return nil
	}

	var auth codexAuthFile
	if err := json.Unmarshal(data, &auth); err != nil {
		logger.Debug("Codex auth file parse failed", "path", authPath, "error", err)
		return nil
	}

	creds := &CodexCredentials{
		AccessToken:  strings.TrimSpace(auth.Tokens.AccessToken),
		RefreshToken: strings.TrimSpace(auth.Tokens.RefreshToken),
		IDToken:      strings.TrimSpace(auth.Tokens.IDToken),
		APIKey:       strings.TrimSpace(auth.OpenAIAPIKey),
		AccountID:    strings.TrimSpace(auth.Tokens.AccountID),
	}

	if creds.AccessToken == "" && creds.APIKey == "" {
		logger.Debug("Codex auth file has no usable token", "path", authPath)
		return nil
	}

	return creds
}

// DetectCodexToken returns OAuth access token when available.
func DetectCodexToken(logger *slog.Logger) string {
	creds := DetectCodexCredentials(logger)
	if creds == nil {
		return ""
	}
	if creds.AccessToken != "" {
		return creds.AccessToken
	}
	return ""
}

func codexAuthPath() string {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return filepath.Join(codexHome, "auth.json")
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codex", "auth.json")
}
