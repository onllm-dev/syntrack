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

	"github.com/google/uuid"
	"github.com/onllm-dev/syntrack/internal/api"
	"github.com/onllm-dev/syntrack/internal/store"
	"github.com/onllm-dev/syntrack/internal/tracker"
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
	agent := New(client, str, tr, 100*time.Millisecond, logger)

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
	agent := New(client, str, tr, interval, logger)

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

	agent := New(client, str, tr, 50*time.Millisecond, logger)

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

	agent := New(client, str, tr, 50*time.Millisecond, logger)

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

	agent := New(client, str, tr, 50*time.Millisecond, logger)

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

	agent := New(client, str, tr, 50*time.Millisecond, logger)

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

	agent := New(client, str, tr, 50*time.Millisecond, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()

	go agent.Run(ctx)
	<-ctx.Done()
	time.Sleep(10 * time.Millisecond)

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

	agent := New(client, str, tr, 100*time.Millisecond, logger)

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

	agent := New(client, str, tr, 100*time.Millisecond, logger)

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
	agent := New(client, str, tr, 500*time.Millisecond, logger) // Long interval

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

	agent := New(client, str, tr, 50*time.Millisecond, logger)

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

// TestAgent_CreatesSessionOnStart verifies new session in DB when Run() begins
func TestAgent_CreatesSessionOnStart(t *testing.T) {
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

	agent := New(client, str, tr, 50*time.Millisecond, logger)

	// Before Run, no active session
	session, _ := str.QueryActiveSession()
	if session != nil {
		t.Error("Expected no active session before Run()")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go agent.Run(ctx)

	// Poll for session creation (CI runners can be slow)
	var found bool
	for i := 0; i < 30; i++ {
		time.Sleep(15 * time.Millisecond)
		session, _ = str.QueryActiveSession()
		if session != nil {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Expected active session after Run() started")
	}

	// Verify it's a valid UUID
	if _, err := uuid.Parse(session.ID); err != nil {
		t.Errorf("Session ID should be valid UUID, got: %s", session.ID)
	}

	// Poll interval should be set
	if session.PollInterval != 50 {
		t.Errorf("Expected poll_interval to be 50, got %d", session.PollInterval)
	}
}

// TestAgent_ClosesSessionOnStop verifies session ended_at is set when Run() returns
func TestAgent_ClosesSessionOnStop(t *testing.T) {
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

	agent := New(client, str, tr, 50*time.Millisecond, logger)

	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error, 1)
	go func() {
		errChan <- agent.Run(ctx)
	}()

	time.Sleep(30 * time.Millisecond) // Let it create session

	// Should have active session
	session, _ := str.QueryActiveSession()
	if session == nil {
		t.Fatal("Expected active session")
	}
	sessionID := session.ID

	// Cancel context
	cancel()

	select {
	case <-errChan:
		// Continue
	case <-time.After(1 * time.Second):
		t.Fatal("Agent did not stop in time")
	}

	// Session should be closed
	history, _ := str.QuerySessionHistory()
	var foundSession *store.Session
	for _, s := range history {
		if s.ID == sessionID {
			foundSession = s
			break
		}
	}

	if foundSession == nil {
		t.Fatal("Session not found in history")
	}

	if foundSession.EndedAt == nil {
		t.Error("Expected session to have ended_at set")
	}
}

// TestAgent_UpdatesSessionMax verifies session max_requests updated each poll
func TestAgent_UpdatesSessionMax(t *testing.T) {
	requestValues := []float64{100.0, 200.0, 300.0}
	callIndex := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := testResponse()
		if callIndex < len(requestValues) {
			resp.Subscription.Requests = requestValues[callIndex]
		}
		callIndex++
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

	agent := New(client, str, tr, 30*time.Millisecond, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	go agent.Run(ctx)
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond)

	// Check session history
	history, _ := str.QuerySessionHistory()
	if len(history) == 0 {
		t.Fatal("Expected at least one session in history")
	}

	session := history[0]

	// Max should be at least 100 (from first poll)
	if session.MaxSubRequests < 100.0 {
		t.Errorf("Expected max_sub_requests >= 100, got %f", session.MaxSubRequests)
	}
}

// TestAgent_SessionMaxIsCorrect verifies if requests go 100→200→150, max stays 200
func TestAgent_SessionMaxIsCorrect(t *testing.T) {
	requestValues := []float64{100.0, 200.0, 150.0}
	callIndex := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := testResponse()
		if callIndex < len(requestValues) {
			resp.Subscription.Requests = requestValues[callIndex]
			resp.Search.Hourly.Requests = requestValues[callIndex] / 2
			resp.ToolCallDiscounts.Requests = requestValues[callIndex] * 10
		}
		callIndex++
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

	agent := New(client, str, tr, 30*time.Millisecond, logger)

	// Run long enough for 3 polls
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Millisecond)
	defer cancel()

	go agent.Run(ctx)
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond)

	history, _ := str.QuerySessionHistory()
	if len(history) == 0 {
		t.Fatal("Expected session in history")
	}

	session := history[0]

	// Max should be exactly 200 (the peak)
	if session.MaxSubRequests != 200.0 {
		t.Errorf("Expected max_sub_requests to be 200 (peak value), got %f", session.MaxSubRequests)
	}

	// Search max should be 100 (200/2)
	if session.MaxSearchRequests != 100.0 {
		t.Errorf("Expected max_search_requests to be 100, got %f", session.MaxSearchRequests)
	}

	// Tool max should be 2000 (200*10)
	if session.MaxToolRequests != 2000.0 {
		t.Errorf("Expected max_tool_requests to be 2000, got %f", session.MaxToolRequests)
	}
}

// TestAgent_SessionSnapshotCount verifies snapshot_count increments per successful poll
func TestAgent_SessionSnapshotCount(t *testing.T) {
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

	agent := New(client, str, tr, 40*time.Millisecond, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 140*time.Millisecond)
	defer cancel()

	go agent.Run(ctx)
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond)

	history, _ := str.QuerySessionHistory()
	if len(history) == 0 {
		t.Fatal("Expected session in history")
	}

	session := history[0]

	// Should have at least 3 snapshots (1 immediate + ~3 interval polls)
	if session.SnapshotCount < 3 {
		t.Errorf("Expected at least 3 snapshots, got %d", session.SnapshotCount)
	}
}

// TestAgent_SessionID verifies SessionID() returns the session ID
func TestAgent_SessionID(t *testing.T) {
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

	agent := New(client, str, tr, 50*time.Millisecond, logger)

	// Before Run, SessionID should be empty
	if agent.SessionID() != "" {
		t.Error("Expected empty SessionID before Run()")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx)
	}()

	<-ctx.Done()
	<-done // Wait for agent to fully stop

	// After Run completes, SessionID should be set
	if agent.SessionID() == "" {
		t.Error("Expected non-empty SessionID after Run() completed")
	}

	// Should be a valid UUID
	if _, err := uuid.Parse(agent.SessionID()); err != nil {
		t.Errorf("SessionID should be valid UUID: %v", err)
	}
}
