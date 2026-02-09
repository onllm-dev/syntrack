package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// AnthropicOAuthClientID is the Claude Code OAuth client ID.
	AnthropicOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// AnthropicOAuthTokenURL is the endpoint for OAuth token operations.
	AnthropicOAuthTokenURL = "https://console.anthropic.com/v1/oauth/token"
)

// ErrOAuthRefreshFailed indicates the OAuth token refresh failed.
var ErrOAuthRefreshFailed = errors.New("oauth: token refresh failed")

// OAuthTokenResponse represents the response from the OAuth token endpoint.
type OAuthTokenResponse struct {
	TokenType    string `json:"token_type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // seconds
	Scope        string `json:"scope"`
}

// oauthRefreshRequest represents the request body for token refresh.
type oauthRefreshRequest struct {
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
}

// oauthErrorResponse represents an error response from the OAuth endpoint.
type oauthErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// RefreshAnthropicToken exchanges a refresh token for a new access token.
// Returns the new tokens and expiry, or an error if the refresh fails.
func RefreshAnthropicToken(ctx context.Context, refreshToken string) (*OAuthTokenResponse, error) {
	reqBody := oauthRefreshRequest{
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
		ClientID:     AnthropicOAuthClientID,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("oauth: marshal request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, AnthropicOAuthTokenURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("oauth: create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "onwatch/1.0")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("oauth: network error: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("oauth: read response: %w", err)
	}

	// Handle error responses
	if resp.StatusCode != http.StatusOK {
		var errResp oauthErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("%w: %s - %s", ErrOAuthRefreshFailed, errResp.Error, errResp.ErrorDescription)
		}
		return nil, fmt.Errorf("%w: HTTP %d", ErrOAuthRefreshFailed, resp.StatusCode)
	}

	var tokenResp OAuthTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("oauth: parse response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("%w: empty access token in response", ErrOAuthRefreshFailed)
	}

	return &tokenResp, nil
}
