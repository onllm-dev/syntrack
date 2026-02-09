package api

import (
	"encoding/json"
	"log/slog"
	"time"
)

// claudeCredentials represents the Claude Code credentials JSON structure.
type claudeCredentials struct {
	ClaudeAiOauth struct {
		AccessToken      string   `json:"accessToken"`
		RefreshToken     string   `json:"refreshToken"`
		ExpiresAt        int64    `json:"expiresAt"` // Unix milliseconds
		Scopes           []string `json:"scopes"`
		SubscriptionType string   `json:"subscriptionType"`
		RateLimitTier    string   `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
}

// AnthropicCredentials contains the parsed OAuth credentials with computed fields.
type AnthropicCredentials struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	ExpiresIn    time.Duration // time until expiry
	Scopes       []string
}

// IsExpiringSoon returns true if the token expires within the given duration.
func (c *AnthropicCredentials) IsExpiringSoon(threshold time.Duration) bool {
	return c.ExpiresIn < threshold
}

// IsExpired returns true if the token has already expired.
func (c *AnthropicCredentials) IsExpired() bool {
	return c.ExpiresIn <= 0
}

// parseClaudeCredentials extracts the OAuth access token from Claude Code credentials JSON.
func parseClaudeCredentials(data []byte) (string, error) {
	var creds claudeCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", err
	}
	return creds.ClaudeAiOauth.AccessToken, nil
}

// parseFullClaudeCredentials extracts all OAuth fields from Claude Code credentials JSON.
func parseFullClaudeCredentials(data []byte) (*AnthropicCredentials, error) {
	var creds claudeCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}

	oauth := creds.ClaudeAiOauth
	if oauth.AccessToken == "" {
		return nil, nil // no credentials
	}

	// Convert expiresAt from Unix milliseconds to time.Time
	expiresAt := time.UnixMilli(oauth.ExpiresAt)
	expiresIn := time.Until(expiresAt)

	return &AnthropicCredentials{
		AccessToken:  oauth.AccessToken,
		RefreshToken: oauth.RefreshToken,
		ExpiresAt:    expiresAt,
		ExpiresIn:    expiresIn,
		Scopes:       oauth.Scopes,
	}, nil
}

// DetectAnthropicToken attempts to auto-detect the Anthropic OAuth token
// from the Claude Code credentials stored in the system keychain or file.
// Returns empty string if not found.
func DetectAnthropicToken(logger *slog.Logger) string {
	return detectAnthropicTokenPlatform(logger)
}

// DetectAnthropicCredentials attempts to auto-detect the full Anthropic OAuth credentials
// from the Claude Code credentials stored in the system keychain or file.
// Returns nil if not found.
func DetectAnthropicCredentials(logger *slog.Logger) *AnthropicCredentials {
	return detectAnthropicCredentialsPlatform(logger)
}
