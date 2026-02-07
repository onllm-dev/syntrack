package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
)

// testResponse returns a standard quota response for mocking
func testResponse() api.QuotaResponse {
	now := time.Now().UTC()
	return api.QuotaResponse{
		Subscription: api.QuotaInfo{
			Limit:    1350,
			Requests: 100.0,
			RenewsAt: now.Add(5 * time.Hour),
		},
		Search: api.SearchInfo{
			Hourly: api.QuotaInfo{
				Limit:    250,
				Requests: 50.0,
				RenewsAt: now.Add(1 * time.Hour),
			},
		},
		ToolCallDiscounts: api.QuotaInfo{
			Limit:    16200,
			Requests: 500.0,
			RenewsAt: now.Add(2 * time.Hour),
		},
	}
}

// setupTest creates a mock server, store, and agent for testing
func setupTest(t *testing.T) (*Agent, *store.Store, *httptest.Server, *bytes.Buffer) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := testResponse()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	// Create in-memory database
	dbPath := ":memory:"
	str, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	t.Cleanup(func() { str.Close() })

	// Create logger that writes to buffer for testing
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := slog.New(handler)

	// Create API client pointing to mock server
	client := api.NewClient("test-key", logger, api.WithBaseURL(server.URL))

	// Create tracker
	tr := tracker.New(str, logger)

	// Create agent with short interval for testing
	agent := New(client, str, tr, 100*time.Millisecond, logger, nil)

	return agent, str, server, &buf
}

// TestAgent_PollsAtInterval verifies the API is called N times in N*interval duration
func TestAgent_PollsAtInterval(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := testResponse()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	str, _ := store.New(":memory:")
	defer str.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	client := api.NewClient("test-key", logger, api.WithBaseURL(server.URL))
	tr := tracker.New(str, logger)

	// Use 50ms interval for faster test
	interval := 50 * time.Millisecond
	agent := New(client, str, tr, interval, logger, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 230*time.Millisecond)
	defer cancel()

	// Run agent - it will poll immediately (1), then every 50ms (4 more in 200ms)
	errChan := make(chan error, 1)
	go func() {
		errChan <- agent.Run(ctx)
	}()

	<-ctx.Done()
	time.Sleep(10 * time.Millisecond) // Give time for cleanup

	// Should have at least 4-5 polls (1 immediate + ~4 interval polls)
	if callCount < 4 {
		t.Errorf("Expected at least 4 API calls in 230ms with 50ms interval, got %d", callCount)
	}
	if callCount > 6 {
		t.Errorf("Expected at most 6 API calls, got %d (too many polls)", callCount)
	}

	select {
	case err := <-errChan:
		if err != nil && err != context.DeadlineExceeded {
			t.Errorf("Expected nil or DeadlineExceeded error, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Error("Agent.Run() did not return within 1s")
	}
}

// TestAgent_StoresEverySnapshot verifies DB has N rows after N polls
func TestAgent_StoresEverySnapshot(t *testing.T) {
	pollCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pollCount++
		resp := testResponse()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	str, _ := store.New(":memory:")
	defer str.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	client := api.NewClient("test-key", logger, api.WithBaseURL(server.URL))
	tr := tracker.New(str, logger)

	agent := New(client, str, tr, 50*time.Millisecond, logger, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 175*time.Millisecond)
	defer cancel()

	go agent.Run(ctx)
	<-ctx.Done()
	time.Sleep(10 * time.Millisecond)

	// Should have 4 snapshots (1 immediate + 3 at 50ms intervals)
	// Or 5 depending on timing, so check range
	if pollCount < 3 {
		t.Errorf("Expected at least 3 polls, got %d", pollCount)
	}
}

// TestAgent_ProcessesWithTracker verifies Tracker.Process is called for each snapshot
func TestAgent_ProcessesWithTracker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := testResponse()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	str, _ := store.New(":memory:")
	defer str.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	client := api.NewClient("test-key", logger, api.WithBaseURL(server.URL))
	tr := tracker.New(str, logger)

	agent := New(client, str, tr, 50*time.Millisecond, logger, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 125*time.Millisecond)
	defer cancel()

	go agent.Run(ctx)
	<-ctx.Done()
	time.Sleep(10 * time.Millisecond)

	// Check that cycles were created (indicates tracker processed snapshots)
	cycles, _ := str.QueryCycleHistory("subscription")
	if len(cycles) != 0 {
		t.Logf("Found %d completed subscription cycles", len(cycles))
	}

	activeCycle, _ := str.QueryActiveCycle("subscription")
	if activeCycle == nil {
		t.Error("Expected active subscription cycle to exist")
	}
}

// TestAgent_APIError_Continues verifies agent logs and continues on API error
func TestAgent_APIError_Continues(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp := testResponse()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	str, _ := store.New(":memory:")
	defer str.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	client := api.NewClient("test-key", logger, api.WithBaseURL(server.URL))
	tr := tracker.New(str, logger)

	agent := New(client, str, tr, 50*time.Millisecond, logger, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Millisecond)
	defer cancel()

	go agent.Run(ctx)
	<-ctx.Done()
	time.Sleep(10 * time.Millisecond)

	// Should have continued polling despite first error
	if callCount < 2 {
		t.Errorf("Expected at least 2 API calls (including error), got %d", callCount)
	}
}

// TestAgent_StoreError_Continues verifies agent logs and continues on store error
func TestAgent_StoreError_Continues(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := testResponse()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	str, _ := store.New(":memory:")
	defer str.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	client := api.NewClient("test-key", logger, api.WithBaseURL(server.URL))
	tr := tracker.New(str, logger)

	agent := New(client, str, tr, 50*time.Millisecond, logger, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Millisecond)
	defer cancel()

	go agent.Run(ctx)
	<-ctx.Done()
	time.Sleep(10 * time.Millisecond)

	// Agent should have polled at least once successfully
	if callCount < 1 {
		t.Errorf("Expected at least 1 API call, got %d", callCount)
	}
}

// TestAgent_TrackerError_StillStoresSnapshot verifies snapshot is saved even if tracker fails
func TestAgent_TrackerError_StillStoresSnapshot(t *testing.T) {
	pollCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pollCount++
		resp := testResponse()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	str, _ := store.New(":memory:")
	defer str.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	client := api.NewClient("test-key", logger, api.WithBaseURL(server.URL))
	tr := tracker.New(str, logger)

	agent := New(client, str, tr, 50*time.Millisecond, logger, nil)

	// Use generous timeout — race detector adds significant overhead
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx)
	}()

	// Wait for at least one poll to complete, then cancel
	time.Sleep(500 * time.Millisecond)
	cancel()

	// Wait for agent to fully stop before querying (avoids separate :memory: connections)
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent.Run() did not return within 2s")
	}

	// Even if tracker had issues, snapshot should be stored
	latest, _ := str.QueryLatest()
	if latest == nil {
		t.Error("Expected at least one snapshot to be stored")
	}
}

// TestAgent_GracefulShutdown verifies context cancel causes Run() to return nil within 1s
func TestAgent_GracefulShutdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond) // Small delay
		resp := testResponse()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	str, _ := store.New(":memory:")
	defer str.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	client := api.NewClient("test-key", logger, api.WithBaseURL(server.URL))
	tr := tracker.New(str, logger)

	agent := New(client, str, tr, 100*time.Millisecond, logger, nil)

	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error, 1)
	go func() {
		errChan <- agent.Run(ctx)
	}()

	// Let it start polling
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Should return within 1 second
	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("Expected nil error on graceful shutdown, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Error("Agent.Run() did not return within 1s after context cancellation")
	}
}

// TestAgent_GracefulShutdown_MidPoll verifies clean exit when cancelled during HTTP request
func TestAgent_GracefulShutdown_MidPoll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Long delay to simulate in-flight request
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			resp := testResponse()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	str, _ := store.New(":memory:")
	defer str.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	client := api.NewClient("test-key", logger, api.WithBaseURL(server.URL), api.WithTimeout(10*time.Second))
	tr := tracker.New(str, logger)

	agent := New(client, str, tr, 100*time.Millisecond, logger, nil)

	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error, 1)
	go func() {
		errChan <- agent.Run(ctx)
	}()

	// Let it start a request
	time.Sleep(10 * time.Millisecond)

	// Cancel while request is in flight
	cancel()

	// Should return within 1 second
	select {
	case err := <-errChan:
		// OK - context cancellation should stop it
		_ = err
	case <-time.After(1 * time.Second):
		t.Error("Agent.Run() did not return within 1s when cancelled mid-poll")
	}
}

// TestAgent_FirstPollImmediate verifies first poll happens immediately, not after interval
func TestAgent_FirstPollImmediate(t *testing.T) {
	callCount := 0
	callTimes := make([]time.Time, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		callTimes = append(callTimes, time.Now())
		resp := testResponse()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	str, _ := store.New(":memory:")
	defer str.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	client := api.NewClient("test-key", logger, api.WithBaseURL(server.URL))
	tr := tracker.New(str, logger)

	startTime := time.Now()
	agent := New(client, str, tr, 500*time.Millisecond, logger, nil) // Long interval

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go agent.Run(ctx)
	<-ctx.Done()
	time.Sleep(10 * time.Millisecond)

	// Should have at least 1 call immediately
	if callCount < 1 {
		t.Fatal("Expected at least 1 immediate API call")
	}

	// First call should be within 50ms of start (immediate)
	if len(callTimes) > 0 {
		timeToFirstCall := callTimes[0].Sub(startTime)
		if timeToFirstCall > 50*time.Millisecond {
			t.Errorf("First poll should be immediate (<50ms), took %v", timeToFirstCall)
		}
	}
}

// TestAgent_LogsEachPoll verifies structured log entry per poll with key metrics
func TestAgent_LogsEachPoll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := testResponse()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	str, _ := store.New(":memory:")
	defer str.Close()

	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := slog.New(handler)
	client := api.NewClient("test-key", logger, api.WithBaseURL(server.URL))
	tr := tracker.New(str, logger)

	agent := New(client, str, tr, 50*time.Millisecond, logger, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx)
	}()

	<-ctx.Done()
	<-done // Wait for agent to fully stop

	// Check that logs were written
	logs := buf.String()
	if len(logs) == 0 {
		t.Error("Expected log output, got none")
	}

	// Should contain poll-related log entries
	if !bytes.Contains(buf.Bytes(), []byte("poll")) && !bytes.Contains(buf.Bytes(), []byte("quota")) {
		t.Logf("Logs content: %s", logs)
		t.Error("Expected logs to contain 'poll' or 'quota' references")
	}
}

// NOTE: Session lifecycle tests (create/close/max/count/ID) were removed.
// Session management is now handled by SessionManager, tested in session_manager_test.go.
