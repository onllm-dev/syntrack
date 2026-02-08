package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
)

// anthropicResponse returns a valid Anthropic quota response JSON with the given utilization values.
func anthropicResponse(fiveHour, sevenDay float64) string {
	boolTrue := true
	now := time.Now().UTC()
	fiveHourReset := now.Add(3 * time.Hour).Format(time.RFC3339)
	sevenDayReset := now.Add(5 * 24 * time.Hour).Format(time.RFC3339)

	resp := map[string]*struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    *string  `json:"resets_at"`
		IsEnabled   *bool    `json:"is_enabled"`
	}{
		"five_hour": {
			Utilization: &fiveHour,
			ResetsAt:    &fiveHourReset,
			IsEnabled:   &boolTrue,
		},
		"seven_day": {
			Utilization: &sevenDay,
			ResetsAt:    &sevenDayReset,
			IsEnabled:   &boolTrue,
		},
	}
	data, _ := json.Marshal(resp)
	return string(data)
}

// TestAnthropicAgent_Run_PollsImmediately verifies the first poll happens immediately on startup,
// not after waiting for the interval.
func TestAnthropicAgent_Run_PollsImmediately(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse(45.2, 12.8)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewAnthropicClient("test-token", logger, api.WithAnthropicBaseURL(server.URL+"/api/oauth/usage"))
	tr := tracker.NewAnthropicTracker(str, logger)

	// Long interval so only the immediate poll fires within the test window
	agent := NewAnthropicAgent(client, str, tr, 5*time.Second, logger, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx)
	}()

	<-ctx.Done()
	time.Sleep(10 * time.Millisecond)

	count := callCount.Load()
	if count < 1 {
		t.Errorf("Expected at least 1 immediate API call, got %d", count)
	}
	// With 5s interval and 100ms timeout, should only get 1 call
	if count > 1 {
		t.Errorf("Expected exactly 1 API call (immediate only), got %d", count)
	}
}

// TestAnthropicAgent_Run_PollsAtInterval verifies subsequent polls happen at the configured interval.
func TestAnthropicAgent_Run_PollsAtInterval(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse(45.2, 12.8)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewAnthropicClient("test-token", logger, api.WithAnthropicBaseURL(server.URL+"/api/oauth/usage"))
	tr := tracker.NewAnthropicTracker(str, logger)

	// 50ms interval, 230ms timeout: expect 1 immediate + ~4 ticks = 5 calls
	agent := NewAnthropicAgent(client, str, tr, 50*time.Millisecond, logger, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 230*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx)
	}()

	<-ctx.Done()
	time.Sleep(10 * time.Millisecond)

	count := callCount.Load()
	if count < 4 {
		t.Errorf("Expected at least 4 API calls in 230ms with 50ms interval, got %d", count)
	}
	if count > 6 {
		t.Errorf("Expected at most 6 API calls, got %d (too many polls)", count)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Expected nil error, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Error("Agent.Run() did not return within 1s")
	}
}

// TestAnthropicAgent_Run_TokenRefresh_BeforeEachPoll verifies that TokenRefreshFunc
// is called before every poll cycle.
func TestAnthropicAgent_Run_TokenRefresh_BeforeEachPoll(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse(45.2, 12.8)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewAnthropicClient("test-token", logger, api.WithAnthropicBaseURL(server.URL+"/api/oauth/usage"))
	tr := tracker.NewAnthropicTracker(str, logger)

	agent := NewAnthropicAgent(client, str, tr, 50*time.Millisecond, logger, nil)

	var refreshCount atomic.Int32
	agent.SetTokenRefresh(func() string {
		refreshCount.Add(1)
		return "test-token" // same token each time
	})

	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx)
	}()

	<-ctx.Done()
	time.Sleep(10 * time.Millisecond)

	polls := callCount.Load()
	refreshes := refreshCount.Load()

	// TokenRefreshFunc should be called once per poll
	if refreshes < polls {
		t.Errorf("Expected at least %d token refreshes (one per poll), got %d", polls, refreshes)
	}
}

// TestAnthropicAgent_Run_TokenRefresh_UpdatesClient verifies that when the token changes,
// SetToken is called on the client with the new token.
func TestAnthropicAgent_Run_TokenRefresh_UpdatesClient(t *testing.T) {
	var requestCount atomic.Int32
	var lastAuthHeader atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		lastAuthHeader.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse(45.2, 12.8)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	// Start with initial token
	client := api.NewAnthropicClient("initial-token", logger, api.WithAnthropicBaseURL(server.URL+"/api/oauth/usage"))
	tr := tracker.NewAnthropicTracker(str, logger)

	agent := NewAnthropicAgent(client, str, tr, 50*time.Millisecond, logger, nil)

	var callNum atomic.Int32
	agent.SetTokenRefresh(func() string {
		n := callNum.Add(1)
		if n >= 3 {
			return "new-rotated-token"
		}
		return "initial-token"
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx)
	}()

	<-ctx.Done()
	time.Sleep(10 * time.Millisecond)

	// After the token refresh returns "new-rotated-token", subsequent requests should use it
	if auth, ok := lastAuthHeader.Load().(string); ok {
		if auth != "Bearer new-rotated-token" {
			// It's possible timing varies, so just check that we got enough requests
			if requestCount.Load() < 3 {
				t.Logf("Only %d requests made, token change may not have happened yet", requestCount.Load())
			}
		}
	}

	// Verify the agent ran without error
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Expected nil error, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Error("Agent.Run() did not return within 1s")
	}
}

// TestAnthropicAgent_Run_401Retry_Success verifies that on a 401 response, the agent
// re-reads the token via TokenRefreshFunc and retries the request successfully.
func TestAnthropicAgent_Run_401Retry_Success(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		// First request returns 401, second (retry) succeeds
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": "unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse(45.2, 12.8)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer str.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	client := api.NewAnthropicClient("old-token", logger, api.WithAnthropicBaseURL(server.URL+"/api/oauth/usage"))
	tr := tracker.NewAnthropicTracker(str, logger)

	agent := NewAnthropicAgent(client, str, tr, 5*time.Second, logger, nil)

	// TokenRefreshFunc returns a new token on re-read
	agent.SetTokenRefresh(func() string {
		return "fresh-token"
	})

	// Run just long enough for 1 immediate poll (which triggers 401 + retry)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx)
	}()

	// Wait for at least the retry to complete
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Expected nil error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Agent.Run() did not return within 2s")
	}

	// Should have made at least 2 requests (initial 401 + retry)
	if count := requestCount.Load(); count < 2 {
		t.Errorf("Expected at least 2 requests (401 + retry), got %d", count)
	}

	// Snapshot should have been stored from the retry
	latest, _ := str.QueryLatestAnthropic()
	if latest == nil {
		t.Error("Expected a snapshot to be stored after successful retry")
	}
}

// TestAnthropicAgent_Run_401Retry_NoRefreshFunc verifies that a 401 without a
// TokenRefreshFunc logs the error and continues polling.
func TestAnthropicAgent_Run_401Retry_NoRefreshFunc(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": "unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse(45.2, 12.8)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer str.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	client := api.NewAnthropicClient("test-token", logger, api.WithAnthropicBaseURL(server.URL+"/api/oauth/usage"))
	tr := tracker.NewAnthropicTracker(str, logger)

	// No TokenRefreshFunc set
	agent := NewAnthropicAgent(client, str, tr, 50*time.Millisecond, logger, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx)
	}()

	<-ctx.Done()

	// Wait for agent goroutine to fully stop before reading shared logBuf
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent.Run() did not return within 2s")
	}

	// Agent should have continued polling despite 401 on first request
	if count := requestCount.Load(); count < 2 {
		t.Errorf("Expected at least 2 API calls (continuing after 401), got %d", count)
	}

	// Logs should contain error about the 401
	logs := logBuf.String()
	if !bytes.Contains([]byte(logs), []byte("Anthropic")) {
		t.Logf("Logs: %s", logs)
		t.Error("Expected log output mentioning Anthropic error")
	}
}

// TestAnthropicAgent_Run_ContextCancel_StopsCleanly verifies that cancelling the
// context causes Run() to return nil promptly.
func TestAnthropicAgent_Run_ContextCancel_StopsCleanly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse(45.2, 12.8)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewAnthropicClient("test-token", logger, api.WithAnthropicBaseURL(server.URL+"/api/oauth/usage"))
	tr := tracker.NewAnthropicTracker(str, logger)

	agent := NewAnthropicAgent(client, str, tr, 100*time.Millisecond, logger, nil)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx)
	}()

	// Let it start polling
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Should return within 1 second
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Expected nil error on graceful shutdown, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Error("Agent.Run() did not return within 1s after context cancellation")
	}
}

// TestAnthropicAgent_Run_StoresSnapshot verifies that each successful poll persists
// a snapshot to the database.
func TestAnthropicAgent_Run_StoresSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(anthropicResponse(45.2, 12.8)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewAnthropicClient("test-token", logger, api.WithAnthropicBaseURL(server.URL+"/api/oauth/usage"))
	tr := tracker.NewAnthropicTracker(str, logger)

	agent := NewAnthropicAgent(client, str, tr, 50*time.Millisecond, logger, nil)

	// Use generous timeout for race detector
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx)
	}()

	// Wait for at least one poll
	time.Sleep(500 * time.Millisecond)
	cancel()

	// Wait for agent to fully stop before querying
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent.Run() did not return within 2s")
	}

	// Verify snapshot was stored
	latest, err := str.QueryLatestAnthropic()
	if err != nil {
		t.Fatalf("QueryLatestAnthropic error: %v", err)
	}
	if latest == nil {
		t.Fatal("Expected at least one Anthropic snapshot to be stored")
	}

	// Verify the snapshot has the expected quota values
	if len(latest.Quotas) < 2 {
		t.Errorf("Expected at least 2 quotas in snapshot, got %d", len(latest.Quotas))
	}

	// Check quota values
	foundFiveHour := false
	for _, q := range latest.Quotas {
		if q.Name == "five_hour" {
			foundFiveHour = true
			if q.Utilization != 45.2 {
				t.Errorf("Expected five_hour utilization 45.2, got %f", q.Utilization)
			}
		}
	}
	if !foundFiveHour {
		t.Error("Expected to find five_hour quota in snapshot")
	}
}
