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

func TestZaiClient_FetchQuotas_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(realZaiAPIResponse))
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewZaiClient("zai_test_key_12345", logger, WithZaiBaseURL(server.URL))

	ctx := context.Background()
	resp, err := client.FetchQuotas(ctx)

	if err != nil {
		t.Fatalf("FetchQuotas() failed: %v", err)
	}

	// Verify TIME_LIMIT
	if len(resp.Limits) != 2 {
		t.Fatalf("Expected 2 limits, got %d", len(resp.Limits))
	}
	if resp.Limits[0].Type != "TIME_LIMIT" {
		t.Errorf("First limit type = %q, want %q", resp.Limits[0].Type, "TIME_LIMIT")
	}
	if resp.Limits[0].CurrentValue != 19 {
		t.Errorf("TIME_LIMIT currentValue = %v, want %v", resp.Limits[0].CurrentValue, 19)
	}

	// Verify TOKENS_LIMIT
	if resp.Limits[1].Type != "TOKENS_LIMIT" {
		t.Errorf("Second limit type = %q, want %q", resp.Limits[1].Type, "TOKENS_LIMIT")
	}
	if resp.Limits[1].CurrentValue != 200112618 {
		t.Errorf("TOKENS_LIMIT currentValue = %v, want %v", resp.Limits[1].CurrentValue, 200112618)
	}
}

func TestZaiClient_FetchQuotas_Unauthorized_HTTP200(t *testing.T) {
	// Z.ai returns HTTP 200 with code 401 for auth failures
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    401,
			"msg":     "Unauthorized",
			"success": false,
			"data":    nil,
		})
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewZaiClient("zai_invalid_key", logger, WithZaiBaseURL(server.URL))

	ctx := context.Background()
	_, err := client.FetchQuotas(ctx)

	if err == nil {
		t.Fatal("FetchQuotas() should fail on 401 response")
	}

	if !errors.Is(err, ErrZaiUnauthorized) {
		t.Errorf("Expected ErrZaiUnauthorized, got: %v", err)
	}
}

func TestZaiClient_FetchQuotas_APIError(t *testing.T) {
	// Z.ai returns HTTP 200 with success=false for errors
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    500,
			"msg":     "Internal server error",
			"success": false,
			"data":    nil,
		})
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewZaiClient("zai_test_key_12345", logger, WithZaiBaseURL(server.URL))

	ctx := context.Background()
	_, err := client.FetchQuotas(ctx)

	if err == nil {
		t.Fatal("FetchQuotas() should fail on API error")
	}

	if !errors.Is(err, ErrZaiAPIError) {
		t.Errorf("Expected ErrZaiAPIError, got: %v", err)
	}

	// Verify error message contains code and msg
	if !strings.Contains(err.Error(), "code=500") {
		t.Errorf("Error should contain code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Internal server error") {
		t.Errorf("Error should contain msg, got: %v", err)
	}
}

func TestZaiClient_FetchQuotas_Timeout(t *testing.T) {
	var requestStarted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestStarted.Store(true)
		time.Sleep(2 * time.Second) // Simulate slow response
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Use 100ms timeout for fast test
	client := NewZaiClient("zai_test_key_12345", logger, WithZaiBaseURL(server.URL), WithZaiTimeout(100*time.Millisecond))

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

func TestZaiClient_FetchQuotas_NetworkError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Use a server that will refuse connections (closed server)
	client := NewZaiClient("zai_test_key_12345", logger, WithZaiBaseURL("http://localhost:1"))

	ctx := context.Background()
	_, err := client.FetchQuotas(ctx)

	if err == nil {
		t.Fatal("FetchQuotas() should fail on connection refused")
	}

	if !errors.Is(err, ErrZaiNetworkError) {
		t.Errorf("Expected ErrZaiNetworkError, got: %v", err)
	}
}

func TestZaiClient_FetchQuotas_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{invalid json`))
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewZaiClient("zai_test_key_12345", logger, WithZaiBaseURL(server.URL))

	ctx := context.Background()
	_, err := client.FetchQuotas(ctx)

	if err == nil {
		t.Fatal("FetchQuotas() should fail on malformed JSON")
	}

	if !errors.Is(err, ErrZaiInvalidResponse) {
		t.Errorf("Expected ErrZaiInvalidResponse, got: %v", err)
	}
}

func TestZaiClient_FetchQuotas_EmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Write empty body
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewZaiClient("zai_test_key_12345", logger, WithZaiBaseURL(server.URL))

	ctx := context.Background()
	_, err := client.FetchQuotas(ctx)

	if err == nil {
		t.Fatal("FetchQuotas() should fail on empty body")
	}

	if !errors.Is(err, ErrZaiInvalidResponse) {
		t.Errorf("Expected ErrZaiInvalidResponse, got: %v", err)
	}
}

func TestZaiClient_SetsAuthHeader(t *testing.T) {
	var authHeader string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeader = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(realZaiAPIResponse))
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewZaiClient("zai_test_key_12345", logger, WithZaiBaseURL(server.URL))

	ctx := context.Background()
	client.FetchQuotas(ctx)

	mu.Lock()
	defer mu.Unlock()

	// Z.ai uses API key directly without Bearer prefix
	expected := "zai_test_key_12345"
	if authHeader != expected {
		t.Errorf("Authorization header = %q, want %q", authHeader, expected)
	}
}

func TestZaiClient_SetsUserAgent(t *testing.T) {
	var userAgent string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		userAgent = r.Header.Get("User-Agent")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(realZaiAPIResponse))
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewZaiClient("zai_test_key_12345", logger, WithZaiBaseURL(server.URL))

	ctx := context.Background()
	client.FetchQuotas(ctx)

	mu.Lock()
	defer mu.Unlock()

	expected := "onwatch/1.0"
	if userAgent != expected {
		t.Errorf("User-Agent header = %q, want %q", userAgent, expected)
	}
}

func TestZaiClient_NeverLogsAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(realZaiAPIResponse))
	}))
	defer server.Close()

	// Capture all log output
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	apiKey := "zai_secret_api_key_xyz789"
	client := NewZaiClient(apiKey, logger, WithZaiBaseURL(server.URL))

	ctx := context.Background()
	client.FetchQuotas(ctx)

	logOutput := buf.String()

	// Check that the full API key is not in the logs
	if strings.Contains(logOutput, apiKey) {
		t.Errorf("Log output contains full API key! Output: %s", logOutput)
	}

	// Check that partial matches are not present
	if strings.Contains(logOutput, "zai_secret") {
		t.Errorf("Log output contains API key prefix! Output: %s", logOutput)
	}
}

func TestZaiClient_RespectsContext(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(realZaiAPIResponse))
	}))
	defer server.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewZaiClient("zai_test_key_12345", logger, WithZaiBaseURL(server.URL))

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

func TestZaiClient_DefaultTimeout(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewZaiClient("zai_test_key_12345", logger)

	// Default timeout should be 30 seconds
	if client.httpClient.Timeout != 30*time.Second {
		t.Errorf("Default timeout = %v, want %v", client.httpClient.Timeout, 30*time.Second)
	}
}

func TestZaiClient_DefaultBaseURL(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewZaiClient("zai_test_key_12345", logger)

	expected := "https://api.z.ai/api/monitor/usage/quota/limit"
	if client.baseURL != expected {
		t.Errorf("Default baseURL = %q, want %q", client.baseURL, expected)
	}
}
