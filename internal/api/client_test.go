package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClient_FetchQuotas_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(realAPIResponse))
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient("syn_test_key_12345", logger, WithBaseURL(server.URL))

	ctx := context.Background()
	resp, err := client.FetchQuotas(ctx)

	if err != nil {
		t.Fatalf("FetchQuotas() failed: %v", err)
	}

	// Verify subscription
	if resp.Subscription.Limit != 1350 {
		t.Errorf("Subscription.Limit = %v, want %v", resp.Subscription.Limit, 1350)
	}
	if resp.Subscription.Requests != 154.3 {
		t.Errorf("Subscription.Requests = %v, want %v", resp.Subscription.Requests, 154.3)
	}

	// Verify search
	if resp.Search.Hourly.Limit != 250 {
		t.Errorf("Search.Hourly.Limit = %v, want %v", resp.Search.Hourly.Limit, 250)
	}

	// Verify tool call discounts
	if resp.ToolCallDiscounts.Limit != 16200 {
		t.Errorf("ToolCallDiscounts.Limit = %v, want %v", resp.ToolCallDiscounts.Limit, 16200)
	}
}

func TestClient_FetchQuotas_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid API key"})
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient("syn_invalid_key", logger, WithBaseURL(server.URL))

	ctx := context.Background()
	_, err := client.FetchQuotas(ctx)

	if err == nil {
		t.Fatal("FetchQuotas() should fail on 401")
	}

	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Expected ErrUnauthorized, got: %v", err)
	}
}

func TestClient_FetchQuotas_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Internal server error"})
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient("syn_test_key_12345", logger, WithBaseURL(server.URL))

	ctx := context.Background()
	_, err := client.FetchQuotas(ctx)

	if err == nil {
		t.Fatal("FetchQuotas() should fail on 500")
	}

	if !errors.Is(err, ErrServerError) {
		t.Errorf("Expected ErrServerError, got: %v", err)
	}
}

func TestClient_FetchQuotas_Timeout(t *testing.T) {
	var requestStarted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestStarted.Store(true)
		time.Sleep(2 * time.Second) // Simulate slow response
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Use 100ms timeout for fast test
	client := NewClient("syn_test_key_12345", logger, WithBaseURL(server.URL), WithTimeout(100*time.Millisecond))

	ctx := context.Background()
	start := time.Now()
	_, err := client.FetchQuotas(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("FetchQuotas() should fail on timeout")
	}

	// Should fail fast, not wait 2 seconds
	if elapsed > 500*time.Millisecond {
		t.Errorf("Timeout took too long: %v", elapsed)
	}

	if !requestStarted.Load() {
		t.Error("Request should have started before timeout")
	}
}

func TestClient_FetchQuotas_NetworkError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Use a server that will refuse connections (closed server)
	client := NewClient("syn_test_key_12345", logger, WithBaseURL("http://localhost:1"))

	ctx := context.Background()
	_, err := client.FetchQuotas(ctx)

	if err == nil {
		t.Fatal("FetchQuotas() should fail on connection refused")
	}

	if !errors.Is(err, ErrNetworkError) {
		t.Errorf("Expected ErrNetworkError, got: %v", err)
	}
}

func TestClient_FetchQuotas_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{invalid json`))
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient("syn_test_key_12345", logger, WithBaseURL(server.URL))

	ctx := context.Background()
	_, err := client.FetchQuotas(ctx)

	if err == nil {
		t.Fatal("FetchQuotas() should fail on malformed JSON")
	}

	if !errors.Is(err, ErrInvalidResponse) {
		t.Errorf("Expected ErrInvalidResponse, got: %v", err)
	}
}

func TestClient_FetchQuotas_EmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Write empty body
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient("syn_test_key_12345", logger, WithBaseURL(server.URL))

	ctx := context.Background()
	_, err := client.FetchQuotas(ctx)

	if err == nil {
		t.Fatal("FetchQuotas() should fail on empty body")
	}

	if !errors.Is(err, ErrInvalidResponse) {
		t.Errorf("Expected ErrInvalidResponse, got: %v", err)
	}
}

func TestClient_SetsAuthHeader(t *testing.T) {
	var authHeader string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeader = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(realAPIResponse))
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient("syn_test_key_12345", logger, WithBaseURL(server.URL))

	ctx := context.Background()
	client.FetchQuotas(ctx)

	mu.Lock()
	defer mu.Unlock()

	expected := "Bearer syn_test_key_12345"
	if authHeader != expected {
		t.Errorf("Authorization header = %q, want %q", authHeader, expected)
	}
}

func TestClient_SetsUserAgent(t *testing.T) {
	var userAgent string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		userAgent = r.Header.Get("User-Agent")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(realAPIResponse))
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient("syn_test_key_12345", logger, WithBaseURL(server.URL))

	ctx := context.Background()
	client.FetchQuotas(ctx)

	mu.Lock()
	defer mu.Unlock()

	expected := "onwatch/1.0"
	if userAgent != expected {
		t.Errorf("User-Agent header = %q, want %q", userAgent, expected)
	}
}

func TestClient_NeverLogsAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(realAPIResponse))
	}))
	defer server.Close()

	// Capture all log output
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	apiKey := "syn_secret_api_key_xyz789"
	client := NewClient(apiKey, logger, WithBaseURL(server.URL))

	ctx := context.Background()
	client.FetchQuotas(ctx)

	logOutput := buf.String()

	// Check that the full API key is not in the logs
	if strings.Contains(logOutput, apiKey) {
		t.Errorf("Log output contains full API key! Output: %s", logOutput)
	}

	// Check that partial matches with "syn_" prefix are not present
	// The redacted form should be something like "syn_abc***...***xyz"
	if strings.Contains(logOutput, "syn_secret") {
		t.Errorf("Log output contains API key prefix! Output: %s", logOutput)
	}
}

func TestClient_RespectsContext(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(realAPIResponse))
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient("syn_test_key_12345", logger, WithBaseURL(server.URL))

	ctx, cancel := context.WithCancel(context.Background())

	// Start the request
	errChan := make(chan error, 1)
	go func() {
		_, err := client.FetchQuotas(ctx)
		errChan <- err
	}()

	// Wait for request to start, then cancel context
	<-requestStarted
	cancel()

	// Should get an error due to context cancellation
	select {
	case err := <-errChan:
		if err == nil {
			t.Fatal("FetchQuotas() should fail when context is cancelled")
		}
		// Should be a context error
		if !errors.Is(err, context.Canceled) {
			t.Logf("Got error (may be wrapped): %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Timeout waiting for error")
	}
}

func TestClient_DefaultTimeout(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient("syn_test_key_12345", logger)

	// Default timeout should be 30 seconds
	if client.httpClient.Timeout != 30*time.Second {
		t.Errorf("Default timeout = %v, want %v", client.httpClient.Timeout, 30*time.Second)
	}
}

func TestClient_DefaultBaseURL(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient("syn_test_key_12345", logger)

	expected := "https://api.synthetic.new/v2/quotas"
	if client.baseURL != expected {
		t.Errorf("Default baseURL = %q, want %q", client.baseURL, expected)
	}
}
