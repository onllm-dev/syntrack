package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	ErrCodexUnauthorized    = errors.New("codex: unauthorized")
	ErrCodexForbidden       = errors.New("codex: forbidden")
	ErrCodexServerError     = errors.New("codex: server error")
	ErrCodexNetworkError    = errors.New("codex: network error")
	ErrCodexInvalidResponse = errors.New("codex: invalid response")
)

// CodexClient is an HTTP client for Codex OAuth usage API.
type CodexClient struct {
	httpClient *http.Client
	baseURL    string
	logger     *slog.Logger

	token   string
	tokenMu sync.RWMutex
	account string
	acctMu  sync.RWMutex

	fallbackMu      sync.RWMutex
	fallbackBaseURL string
}

// CodexOption configures a CodexClient.
type CodexOption func(*CodexClient)

// WithCodexBaseURL sets custom base URL.
func WithCodexBaseURL(url string) CodexOption {
	return func(c *CodexClient) {
		c.baseURL = url
	}
}

// WithCodexTimeout sets custom timeout.
func WithCodexTimeout(timeout time.Duration) CodexOption {
	return func(c *CodexClient) {
		c.httpClient.Timeout = timeout
	}
}

// NewCodexClient creates a Codex usage API client.
func NewCodexClient(token string, logger *slog.Logger, opts ...CodexOption) *CodexClient {
	if logger == nil {
		logger = slog.Default()
	}

	client := &CodexClient{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:          1,
				MaxIdleConnsPerHost:   1,
				ResponseHeaderTimeout: 10 * time.Second,
				IdleConnTimeout:       10 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		},
		token:   token,
		baseURL: "https://chatgpt.com/backend-api/wham/usage",
		logger:  logger,
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// SetToken updates bearer token for API calls.
func (c *CodexClient) SetToken(token string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.token = token
}

// SetAccountID updates account id metadata.
func (c *CodexClient) SetAccountID(accountID string) {
	c.acctMu.Lock()
	defer c.acctMu.Unlock()
	c.account = accountID
}

func (c *CodexClient) getToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

func (c *CodexClient) getAccountID() string {
	c.acctMu.RLock()
	defer c.acctMu.RUnlock()
	return c.account
}

func buildCodexFallbackBaseURL(rawBaseURL string) (string, bool) {
	u, err := url.Parse(rawBaseURL)
	if err != nil {
		return "", false
	}
	switch {
	case strings.Contains(u.Path, "/api/codex/usage"):
		u.Path = strings.Replace(u.Path, "/api/codex/usage", "/backend-api/wham/usage", 1)
	case strings.Contains(u.Path, "/backend-api/wham/usage"):
		u.Path = strings.Replace(u.Path, "/backend-api/wham/usage", "/api/codex/usage", 1)
	default:
		return "", false
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), true
}

func (c *CodexClient) getFallbackBaseURL() string {
	c.fallbackMu.RLock()
	defer c.fallbackMu.RUnlock()
	return c.fallbackBaseURL
}

func (c *CodexClient) setFallbackBaseURL(url string) {
	c.fallbackMu.Lock()
	defer c.fallbackMu.Unlock()
	c.fallbackBaseURL = url
}

func (c *CodexClient) doUsageRequest(ctx context.Context, usageURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("codex: creating request: %w", err)
	}

	token := c.getToken()
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "onwatch/1.0")
	if accountID := c.getAccountID(); accountID != "" {
		req.Header.Set("X-Account-Id", accountID)
		req.Header.Set("ChatClaude-Account-Id", accountID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrCodexNetworkError, err)
	}
	return resp, nil
}

// FetchUsage fetches Codex OAuth usage state.
func (c *CodexClient) FetchUsage(ctx context.Context) (*CodexUsageResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	usageURL := c.baseURL
	if fallback := c.getFallbackBaseURL(); fallback != "" {
		usageURL = fallback
	}

	resp, err := c.doUsageRequest(reqCtx, usageURL)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotFound {
		if fallbackURL, ok := buildCodexFallbackBaseURL(usageURL); ok {
			resp.Body.Close()
			c.setFallbackBaseURL(fallbackURL)
			resp, err = c.doUsageRequest(reqCtx, fallbackURL)
			if err != nil {
				return nil, err
			}
		}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrCodexUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		return nil, ErrCodexForbidden
	case resp.StatusCode >= 500:
		return nil, ErrCodexServerError
	default:
		return nil, fmt.Errorf("codex: unexpected status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("%w: reading body: %v", ErrCodexInvalidResponse, err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("%w: empty response body", ErrCodexInvalidResponse)
	}

	var usageResp CodexUsageResponse
	if err := json.Unmarshal(body, &usageResp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCodexInvalidResponse, err)
	}

	return &usageResp, nil
}
