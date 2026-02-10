// Package api provides client for interacting with the Synthetic API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Custom errors for different failure modes.
var (
	ErrUnauthorized    = errors.New("api: unauthorized - invalid API key")
	ErrServerError     = errors.New("api: server error")
	ErrNetworkError    = errors.New("api: network error")
	ErrInvalidResponse = errors.New("api: invalid response")
)

// Client is an HTTP client for the Synthetic API.
type Client struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	logger     *slog.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL sets a custom base URL (for testing).
func WithBaseURL(url string) Option {
	return func(c *Client) {
		c.baseURL = url
	}
}

// WithTimeout sets a custom timeout (for testing).
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		c.httpClient.Timeout = timeout
	}
}

// NewClient creates a new API client.
func NewClient(apiKey string, logger *slog.Logger, opts ...Option) *Client {
	client := &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:          1,
				MaxIdleConnsPerHost:   1,
				ResponseHeaderTimeout: 30 * time.Second,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		},
		apiKey:  apiKey,
		baseURL: "https://api.synthetic.new/v2/quotas",
		logger:  logger,
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// FetchQuotas retrieves the current quota information from the API.
func (c *Client) FetchQuotas(ctx context.Context) (*QuotaResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("api: creating request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", "onwatch/1.0")
	req.Header.Set("Accept", "application/json")

	// Log request (with redacted API key)
	c.logger.Debug("fetching quotas",
		"url", c.baseURL,
		"api_key", redactAPIKey(c.apiKey),
	)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Check for context cancellation
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrNetworkError, err)
	}
	defer resp.Body.Close()

	// Log response status
	c.logger.Debug("quota response received",
		"status", resp.StatusCode,
	)

	// Handle HTTP status codes
	switch resp.StatusCode {
	case http.StatusOK:
		// Continue to parse response
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		return nil, ErrServerError
	default:
		return nil, fmt.Errorf("api: unexpected status code %d", resp.StatusCode)
	}

	// Read and parse response body (bounded to 64KB)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("%w: reading body: %v", ErrInvalidResponse, err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("%w: empty response body", ErrInvalidResponse)
	}

	var quotaResp QuotaResponse
	if err := json.Unmarshal(body, &quotaResp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}

	c.logger.Debug("quotas fetched successfully",
		"subscription_requests", quotaResp.Subscription.Requests,
		"search_requests", quotaResp.Search.Hourly.Requests,
		"tool_requests", quotaResp.ToolCallDiscounts.Requests,
	)

	return &quotaResp, nil
}

// redactAPIKey masks the API key for logging.
func redactAPIKey(key string) string {
	if key == "" {
		return "(empty)"
	}

	if !strings.HasPrefix(key, "syn_") || len(key) < 8 {
		return "syn_***...***"
	}

	// Show first 4 chars after syn_ and last 3 chars
	visibleStart := 4 // "syn_" prefix shown
	if len(key) <= visibleStart+7 {
		return "syn_***...***"
	}

	return key[:visibleStart+4] + "***...***" + key[len(key)-3:]
}
