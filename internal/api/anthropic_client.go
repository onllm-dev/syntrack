// Package api provides clients for interacting with the Anthropic API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Custom errors for Anthropic API failures.
var (
	ErrAnthropicUnauthorized    = errors.New("anthropic: unauthorized - invalid API key")
	ErrAnthropicServerError     = errors.New("anthropic: server error")
	ErrAnthropicNetworkError    = errors.New("anthropic: network error")
	ErrAnthropicInvalidResponse = errors.New("anthropic: invalid response")
)

// AnthropicClient is an HTTP client for the Anthropic API.
type AnthropicClient struct {
	httpClient *http.Client
	token      string
	baseURL    string
	logger     *slog.Logger
}

// AnthropicOption configures an AnthropicClient.
type AnthropicOption func(*AnthropicClient)

// WithAnthropicBaseURL sets a custom base URL (for testing).
func WithAnthropicBaseURL(url string) AnthropicOption {
	return func(c *AnthropicClient) {
		c.baseURL = url
	}
}

// WithAnthropicTimeout sets a custom timeout (for testing).
func WithAnthropicTimeout(timeout time.Duration) AnthropicOption {
	return func(c *AnthropicClient) {
		c.httpClient.Timeout = timeout
	}
}

// NewAnthropicClient creates a new Anthropic API client.
func NewAnthropicClient(token string, logger *slog.Logger, opts ...AnthropicOption) *AnthropicClient {
	client := &AnthropicClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		},
		token:   token,
		baseURL: "https://api.anthropic.com/api/oauth/usage",
		logger:  logger,
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// SetToken updates the token used for API requests (for token refresh).
func (c *AnthropicClient) SetToken(token string) {
	c.token = token
}

// FetchQuotas retrieves the current quota information from the Anthropic API.
func (c *AnthropicClient) FetchQuotas(ctx context.Context) (*AnthropicQuotaResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("anthropic: creating request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "onwatch/1.0")

	// Log request (with redacted token)
	c.logger.Debug("fetching Anthropic quotas",
		"url", c.baseURL,
		"token", redactAnthropicToken(c.token),
	)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Check for context cancellation
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrAnthropicNetworkError, err)
	}
	defer resp.Body.Close()

	// Log response status
	c.logger.Debug("Anthropic quota response received",
		"status", resp.StatusCode,
	)

	// Handle HTTP status codes
	switch {
	case resp.StatusCode == http.StatusOK:
		// Continue to parse response
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrAnthropicUnauthorized
	case resp.StatusCode >= 500:
		return nil, ErrAnthropicServerError
	default:
		return nil, fmt.Errorf("anthropic: unexpected status code %d", resp.StatusCode)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: reading body: %v", ErrAnthropicInvalidResponse, err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("%w: empty response body", ErrAnthropicInvalidResponse)
	}

	var quotaResp AnthropicQuotaResponse
	if err := json.Unmarshal(body, &quotaResp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAnthropicInvalidResponse, err)
	}

	// Log active quota names on success
	if names := quotaResp.ActiveQuotaNames(); len(names) > 0 {
		c.logger.Debug("Anthropic quotas fetched successfully",
			"active_quotas", names,
		)
	}

	return &quotaResp, nil
}

// redactAnthropicToken masks the token for logging.
func redactAnthropicToken(key string) string {
	if key == "" {
		return "(empty)"
	}

	if len(key) < 8 {
		return "***...***"
	}

	// Show first 4 chars and last 3 chars
	return key[:4] + "***...***" + key[len(key)-3:]
}
