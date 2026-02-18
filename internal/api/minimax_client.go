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

// Custom errors for MiniMax API failures.
var (
	ErrMiniMaxUnauthorized    = errors.New("minimax: unauthorized - invalid api key")
	ErrMiniMaxServerError     = errors.New("minimax: server error")
	ErrMiniMaxNetworkError    = errors.New("minimax: network error")
	ErrMiniMaxInvalidResponse = errors.New("minimax: invalid response")
)

// MiniMaxClient is an HTTP client for the MiniMax coding plan remains API.
type MiniMaxClient struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	logger     *slog.Logger
}

// MiniMaxOption configures a MiniMaxClient.
type MiniMaxOption func(*MiniMaxClient)

// WithMiniMaxBaseURL sets a custom base URL (for testing).
func WithMiniMaxBaseURL(url string) MiniMaxOption {
	return func(c *MiniMaxClient) {
		c.baseURL = url
	}
}

// WithMiniMaxTimeout sets a custom timeout (for testing).
func WithMiniMaxTimeout(d time.Duration) MiniMaxOption {
	return func(c *MiniMaxClient) {
		c.httpClient.Timeout = d
	}
}

// NewMiniMaxClient creates a new MiniMax API client.
func NewMiniMaxClient(apiKey string, logger *slog.Logger, opts ...MiniMaxOption) *MiniMaxClient {
	client := &MiniMaxClient{
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
		baseURL: "https://www.minimax.io/v1/api/openplatform/coding_plan/remains",
		logger:  logger,
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// FetchRemains retrieves current coding plan remains from MiniMax.
func (c *MiniMaxClient) FetchRemains(ctx context.Context) (*MiniMaxRemainsResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("minimax: creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if c.logger != nil {
		c.logger.Debug("fetching MiniMax remains",
			"url", c.baseURL,
			"apiKey", redactMiniMaxToken(c.apiKey),
		)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrMiniMaxNetworkError, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrMiniMaxUnauthorized
	case resp.StatusCode >= 500:
		return nil, ErrMiniMaxServerError
	default:
		return nil, fmt.Errorf("minimax: unexpected status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("%w: reading body: %v", ErrMiniMaxInvalidResponse, err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("%w: empty response body", ErrMiniMaxInvalidResponse)
	}

	var remainsResp MiniMaxRemainsResponse
	if err := json.Unmarshal(body, &remainsResp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMiniMaxInvalidResponse, err)
	}

	if remainsResp.BaseResp.StatusCode != 0 {
		if remainsResp.BaseResp.StatusCode == 1004 {
			return nil, ErrMiniMaxUnauthorized
		}
		return nil, fmt.Errorf("minimax: api error code=%d, msg=%s", remainsResp.BaseResp.StatusCode, remainsResp.BaseResp.StatusMsg)
	}

	if c.logger != nil {
		c.logger.Debug("MiniMax remains fetched successfully",
			"models", remainsResp.ActiveModelNames(),
		)
	}

	return &remainsResp, nil
}

// redactMiniMaxToken masks the API key for logging.
func redactMiniMaxToken(key string) string {
	if key == "" {
		return "(empty)"
	}

	if len(key) < 8 {
		return "***...***"
	}

	return key[:4] + "***...***" + key[len(key)-3:]
}
