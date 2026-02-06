// Package api provides clients for interacting with the Z.ai API.
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

// Custom errors for Z.ai API failures.
var (
	ErrZaiUnauthorized    = errors.New("zai: unauthorized - invalid API key")
	ErrZaiServerError     = errors.New("zai: server error")
	ErrZaiNetworkError    = errors.New("zai: network error")
	ErrZaiInvalidResponse = errors.New("zai: invalid response")
	ErrZaiAPIError        = errors.New("zai: API returned error")
)

// ZaiClient is an HTTP client for the Z.ai API.
type ZaiClient struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	logger     *slog.Logger
}

// ZaiOption configures a ZaiClient.
type ZaiOption func(*ZaiClient)

// WithZaiBaseURL sets a custom base URL (for testing).
func WithZaiBaseURL(url string) ZaiOption {
	return func(c *ZaiClient) {
		c.baseURL = url
	}
}

// WithZaiTimeout sets a custom timeout (for testing).
func WithZaiTimeout(timeout time.Duration) ZaiOption {
	return func(c *ZaiClient) {
		c.httpClient.Timeout = timeout
	}
}

// NewZaiClient creates a new Z.ai API client.
func NewZaiClient(apiKey string, logger *slog.Logger, opts ...ZaiOption) *ZaiClient {
	client := &ZaiClient{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		apiKey:  apiKey,
		baseURL: "https://api.z.ai/api/monitor/usage/quota/limit",
		logger:  logger,
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// FetchQuotas retrieves the current quota information from the Z.ai API.
func (c *ZaiClient) FetchQuotas(ctx context.Context) (*ZaiQuotaResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("zai: creating request: %w", err)
	}

	// Set headers - Z.ai uses API key directly without Bearer prefix
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("User-Agent", "syntrack/1.0")
	req.Header.Set("Accept", "application/json")

	// Log request (with redacted API key)
	c.logger.Debug("fetching Z.ai quotas",
		"url", c.baseURL,
		"api_key", redactZaiAPIKey(c.apiKey),
	)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Check for context cancellation
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrZaiNetworkError, err)
	}
	defer resp.Body.Close()

	// Log response status
	c.logger.Debug("Z.ai quota response received",
		"status", resp.StatusCode,
	)

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: reading body: %v", ErrZaiInvalidResponse, err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("%w: empty response body", ErrZaiInvalidResponse)
	}

	// Parse the response wrapper first to check for API errors
	var wrapper ZaiResponse[ZaiQuotaResponse]
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrZaiInvalidResponse, err)
	}

	// Handle Z.ai's unique error format: HTTP 200 with error code in body
	if wrapper.Code == 401 {
		return nil, ErrZaiUnauthorized
	}

	if !wrapper.Success {
		return nil, fmt.Errorf("%w: code=%d, msg=%s", ErrZaiAPIError, wrapper.Code, wrapper.Msg)
	}

	// The quota response is already parsed in the wrapper
	quotaResp := wrapper.Data

	// Log usage info if we have limits
	if len(quotaResp.Limits) > 0 {
		timeUsage := float64(0)
		tokensUsage := float64(0)
		for _, limit := range quotaResp.Limits {
			if limit.Type == "TIME_LIMIT" {
				timeUsage = limit.CurrentValue
			} else if limit.Type == "TOKENS_LIMIT" {
				tokensUsage = limit.CurrentValue
			}
		}
		c.logger.Debug("Z.ai quotas fetched successfully",
			"time_usage", timeUsage,
			"tokens_usage", tokensUsage,
		)
	}

	return &quotaResp, nil
}

// redactZaiAPIKey masks the API key for logging.
func redactZaiAPIKey(key string) string {
	if key == "" {
		return "(empty)"
	}

	if len(key) < 8 {
		return "***...***"
	}

	// Show first 4 chars and last 3 chars
	return key[:4] + "***...***" + key[len(key)-3:]
}
