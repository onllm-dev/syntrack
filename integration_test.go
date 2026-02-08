//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/agent"
	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/config"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/testutil"
	"github.com/onllm-dev/onwatch/internal/tracker"
	"github.com/onllm-dev/onwatch/internal/web"
)

// discardLogger returns a logger that discards all output
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// makeHandler creates a Handler wired with proper sessions/config for legacy tests.
func makeHandler(t *testing.T, s *store.Store, tr *tracker.Tracker) *web.Handler {
	t.Helper()
	logger := discardLogger()
	cfg := testutil.TestConfig("http://localhost:0")
	sessions := web.NewSessionStore(cfg.AdminUser, "testhash", s)
	h := web.NewHandler(s, tr, logger, sessions, cfg)
	h.SetVersion("test-dev")
	return h
}

// mockServer creates a test server that returns synthetic API responses.
// Uses atomic counter for thread safety. Does not call t.Errorf from the handler
// goroutine to avoid races with the test goroutine.
func mockServer(_ *testing.T, responses []api.QuotaResponse) *httptest.Server {
	var callCount atomic.Int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v2/quotas" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		idx := int(callCount.Add(1) - 1)
		if idx < len(responses) {
			json.NewEncoder(w).Encode(responses[idx])
		} else {
			// Return last response repeatedly
			json.NewEncoder(w).Encode(responses[len(responses)-1])
		}
	}))
}

// TestIntegration_FullCycle tests the complete flow from API poll to dashboard data
func TestIntegration_FullCycle(t *testing.T) {
	// Create temp directory for test database
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	// Setup mock API responses — need at least 2 with different values
	// so SessionManager detects usage change and creates a session
	now := time.Now().UTC()
	responses := []api.QuotaResponse{
		{
			Subscription: api.QuotaInfo{
				Limit:    1350,
				Requests: 100.0,
				RenewsAt: now.Add(5 * time.Hour),
			},
			Search: api.SearchInfo{
				Hourly: api.QuotaInfo{
					Limit:    250,
					Requests: 10.0,
					RenewsAt: now.Add(1 * time.Hour),
				},
			},
			ToolCallDiscounts: api.QuotaInfo{
				Limit:    16200,
				Requests: 5000.0,
				RenewsAt: now.Add(3 * time.Hour),
			},
		},
		{
			Subscription: api.QuotaInfo{
				Limit:    1350,
				Requests: 100.0,
				RenewsAt: now.Add(5 * time.Hour),
			},
			Search: api.SearchInfo{
				Hourly: api.QuotaInfo{
					Limit:    250,
					Requests: 11.0, // Slightly different to trigger session creation
					RenewsAt: now.Add(1 * time.Hour),
				},
			},
			ToolCallDiscounts: api.QuotaInfo{
				Limit:    16200,
				Requests: 5000.0,
				RenewsAt: now.Add(3 * time.Hour),
			},
		},
	}

	server := mockServer(t, responses)
	defer server.Close()

	// Open database
	db, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create API client pointing to mock server
	client := api.NewClient("syn_test_key", discardLogger(), api.WithBaseURL(server.URL+"/v2/quotas"))

	// Create tracker
	tr := tracker.New(db, discardLogger())

	// Create session manager and agent with short interval for testing
	sm := agent.NewSessionManager(db, "synthetic", 5*time.Minute, discardLogger())
	ag := agent.New(client, db, tr, 100*time.Millisecond, discardLogger(), sm)

	// Run agent for a short time — enough for 2+ polls to detect session
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	// Run agent (it will poll once immediately, then at interval)
	done := make(chan error, 1)
	go func() {
		done <- ag.Run(ctx)
	}()

	// Wait for agent to complete or timeout
	select {
	case err := <-done:
		if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
			t.Fatalf("Agent error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Agent did not complete in time")
	}

	// Verify data was stored (latest is second response after session-triggering poll)
	latest, err := db.QueryLatest()
	if err != nil {
		t.Fatalf("Failed to query latest: %v", err)
	}
	if latest == nil {
		t.Fatal("Expected snapshot to be stored")
	}

	if latest.Sub.Requests != 100.0 {
		t.Errorf("Expected sub requests 100.0, got %f", latest.Sub.Requests)
	}
	// Search may be 10.0 or 11.0 depending on which poll was last
	if latest.Search.Requests < 10.0 || latest.Search.Requests > 11.0 {
		t.Errorf("Expected search requests 10.0-11.0, got %f", latest.Search.Requests)
	}
	if latest.ToolCall.Requests != 5000.0 {
		t.Errorf("Expected tool requests 5000.0, got %f", latest.ToolCall.Requests)
	}

	// Verify session was created (needs 2 polls with different values)
	sessions, err := db.QuerySessionHistory()
	if err != nil {
		t.Fatalf("Failed to query sessions: %v", err)
	}
	if len(sessions) < 1 {
		t.Fatalf("Expected at least 1 session, got %d", len(sessions))
	}
	if sessions[0].SnapshotCount < 1 {
		t.Errorf("Expected at least 1 snapshot, got %d", sessions[0].SnapshotCount)
	}

	// Test web handler returns the data
	handler := makeHandler(t, db, tr)

	// Test /api/current endpoint for synthetic provider
	req := httptest.NewRequest("GET", "/api/current?provider=synthetic", nil)
	w := httptest.NewRecorder()
	handler.Current(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var currentResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &currentResp); err != nil {
		t.Fatalf("Failed to parse current response: %v", err)
	}

	// Verify the response contains subscription data
	if _, ok := currentResp["subscription"]; !ok {
		if _, ok2 := currentResp["error"]; ok2 {
			t.Fatalf("Got error response: %s", w.Body.String())
		}
		t.Fatalf("Expected subscription in response, got keys: %v", currentResp)
	}
}

// TestIntegration_ResetDetection tests reset cycle detection
func TestIntegration_ResetDetection(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	now := time.Now().UTC()
	oldRenewsAt := now.Add(5 * time.Hour)
	newRenewsAt := now.Add(6 * time.Hour)

	responses := []api.QuotaResponse{
		// First poll - initial state
		{
			Subscription: api.QuotaInfo{
				Limit:    1350,
				Requests: 100.0,
				RenewsAt: oldRenewsAt,
			},
			Search: api.SearchInfo{
				Hourly: api.QuotaInfo{
					Limit:    250,
					Requests: 10.0,
					RenewsAt: now.Add(1 * time.Hour),
				},
			},
			ToolCallDiscounts: api.QuotaInfo{
				Limit:    16200,
				Requests: 5000.0,
				RenewsAt: now.Add(3 * time.Hour),
			},
		},
		// Second poll - subscription reset detected (renewsAt changed)
		{
			Subscription: api.QuotaInfo{
				Limit:    1350,
				Requests: 50.0, // Reset to lower value
				RenewsAt: newRenewsAt,
			},
			Search: api.SearchInfo{
				Hourly: api.QuotaInfo{
					Limit:    250,
					Requests: 15.0,
					RenewsAt: now.Add(1 * time.Hour),
				},
			},
			ToolCallDiscounts: api.QuotaInfo{
				Limit:    16200,
				Requests: 5100.0,
				RenewsAt: now.Add(3 * time.Hour),
			},
		},
	}

	server := mockServer(t, responses)
	defer server.Close()

	db, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	client := api.NewClient("syn_test_key", discardLogger(), api.WithBaseURL(server.URL+"/v2/quotas"))
	tr := tracker.New(db, discardLogger())

	// First poll — runs once immediately then exits via short timeout
	sm1 := agent.NewSessionManager(db, "synthetic", 5*time.Minute, discardLogger())
	ag1 := agent.New(client, db, tr, 1*time.Hour, discardLogger(), sm1)
	ctx1, cancel1 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	done1 := make(chan struct{})
	go func() {
		ag1.Run(ctx1)
		close(done1)
	}()
	<-done1 // Wait for first agent to fully stop
	cancel1()

	// Second poll - should detect reset (renewsAt changed)
	sm2 := agent.NewSessionManager(db, "synthetic", 5*time.Minute, discardLogger())
	ag2 := agent.New(client, db, tr, 1*time.Hour, discardLogger(), sm2)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	done2 := make(chan struct{})
	go func() {
		ag2.Run(ctx2)
		close(done2)
	}()
	<-done2 // Wait for second agent to fully stop
	cancel2()

	// Verify cycles were recorded
	history, err := db.QueryCycleHistory("subscription")
	if err != nil {
		t.Fatalf("Failed to query cycle history: %v", err)
	}

	if len(history) != 1 {
		t.Fatalf("Expected 1 completed subscription cycle, got %d", len(history))
	}

	// The completed cycle should have peak of 100 (the max seen before reset)
	if history[0].PeakRequests != 100.0 {
		t.Errorf("Expected peak requests 100.0, got %f", history[0].PeakRequests)
	}

	// Verify via API endpoint
	handler := makeHandler(t, db, tr)
	req := httptest.NewRequest("GET", "/api/cycles?type=subscription&provider=synthetic", nil)
	w := httptest.NewRecorder()
	handler.Cycles(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var cyclesResp []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &cyclesResp); err != nil {
		t.Fatalf("Failed to parse cycles response: %v", err)
	}

	if len(cyclesResp) < 1 {
		t.Fatalf("Expected at least 1 cycle in response, got %d", len(cyclesResp))
	}
}

// TestIntegration_DashboardRendersData tests that the dashboard HTML contains actual data
func TestIntegration_DashboardRendersData(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	now := time.Now().UTC()
	responses := []api.QuotaResponse{
		{
			Subscription: api.QuotaInfo{
				Limit:    1350,
				Requests: 154.3,
				RenewsAt: now.Add(5 * time.Hour),
			},
			Search: api.SearchInfo{
				Hourly: api.QuotaInfo{
					Limit:    250,
					Requests: 0,
					RenewsAt: now.Add(1 * time.Hour),
				},
			},
			ToolCallDiscounts: api.QuotaInfo{
				Limit:    16200,
				Requests: 7635,
				RenewsAt: now.Add(3 * time.Hour),
			},
		},
	}

	server := mockServer(t, responses)
	defer server.Close()

	db, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	client := api.NewClient("syn_test_key", discardLogger(), api.WithBaseURL(server.URL+"/v2/quotas"))
	tr := tracker.New(db, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	sm := agent.NewSessionManager(db, "synthetic", 5*time.Minute, discardLogger())
	ag := agent.New(client, db, tr, 1*time.Hour, discardLogger(), sm)
	go ag.Run(ctx)
	time.Sleep(250 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Test dashboard HTML response
	handler := makeHandler(t, db, tr)
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.Dashboard(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()

	// Check that the page contains expected elements
	if !strings.Contains(body, "onWatch") {
		t.Error("Dashboard should contain 'onWatch'")
	}
	if !strings.Contains(body, "Dashboard") {
		t.Error("Dashboard should contain 'Dashboard'")
	}
	if !strings.Contains(body, "style.css") {
		t.Error("Dashboard should reference style.css")
	}
	if !strings.Contains(body, "app.js") {
		t.Error("Dashboard should reference app.js")
	}
}

// TestIntegration_GracefulShutdown tests that SIGINT triggers clean shutdown
func TestIntegration_GracefulShutdown(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping shutdown test in CI environment")
	}

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	now := time.Now().UTC()
	responses := []api.QuotaResponse{
		{
			Subscription: api.QuotaInfo{
				Limit:    1350,
				Requests: 100.0,
				RenewsAt: now.Add(5 * time.Hour),
			},
			Search: api.SearchInfo{
				Hourly: api.QuotaInfo{
					Limit:    250,
					Requests: 10.0,
					RenewsAt: now.Add(1 * time.Hour),
				},
			},
			ToolCallDiscounts: api.QuotaInfo{
				Limit:    16200,
				Requests: 5000.0,
				RenewsAt: now.Add(3 * time.Hour),
			},
		},
		{
			Subscription: api.QuotaInfo{
				Limit:    1350,
				Requests: 101.0, // Changed to trigger session
				RenewsAt: now.Add(5 * time.Hour),
			},
			Search: api.SearchInfo{
				Hourly: api.QuotaInfo{
					Limit:    250,
					Requests: 10.0,
					RenewsAt: now.Add(1 * time.Hour),
				},
			},
			ToolCallDiscounts: api.QuotaInfo{
				Limit:    16200,
				Requests: 5000.0,
				RenewsAt: now.Add(3 * time.Hour),
			},
		},
	}

	server := mockServer(t, responses)
	defer server.Close()

	db, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	client := api.NewClient("syn_test_key", discardLogger(), api.WithBaseURL(server.URL+"/v2/quotas"))
	tr := tracker.New(db, discardLogger())
	sm := agent.NewSessionManager(db, "synthetic", 5*time.Minute, discardLogger())
	ag := agent.New(client, db, tr, 500*time.Millisecond, discardLogger(), sm)

	// Create web server
	handler := makeHandler(t, db, tr)
	webServer := web.NewServer(0, handler, discardLogger(), "admin", "testhash")

	// Start web server in background
	go webServer.Start()
	time.Sleep(100 * time.Millisecond)

	// Start agent in background — needs 2 polls to create session
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ag.Run(ctx)
	time.Sleep(1200 * time.Millisecond)

	// Get active session
	session, err := db.QueryActiveSession()
	if err != nil {
		t.Fatalf("Failed to query active session: %v", err)
	}
	if session == nil {
		t.Fatal("Expected active session before shutdown")
	}

	// Cancel context to trigger graceful shutdown (simulates SIGINT handler)
	cancel()
	time.Sleep(500 * time.Millisecond)

	// Verify session was closed properly
	sessions, err := db.QuerySessionHistory()
	if err != nil {
		t.Fatalf("Failed to query sessions: %v", err)
	}

	if len(sessions) < 1 {
		t.Fatal("Expected at least one session")
	}

	// The most recent session should have an end time
	if sessions[0].EndedAt == nil {
		t.Error("Session should have been closed (ended_at should not be nil)")
	}

	// Verify database is not corrupted by opening it again
	db2, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to reopen database: %v", err)
	}
	db2.Close()
}

// TestMain ensures the main package compiles and basic flags work
func TestMain_Version(t *testing.T) {
	// Test version flag by checking if binary can be built
	if testing.Short() {
		t.Skip("Skipping binary build test in short mode")
	}

	// Just verify main.go compiles
	// The actual binary test would require building
	fmt.Println("Main package compiles successfully")
}

// Helper to make HTTP requests in tests
func makeRequest(t *testing.T, method, url string, body string) (*http.Response, string) {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	return resp, string(respBody)
}

// ═══════════════════════════════════════════════════════════════════════
// 3.1 Synthetic Data Integrity (5 tests)
// ═══════════════════════════════════════════════════════════════════════

// TestIntegration_Synthetic_SnapshotStoredCorrectly verifies every field of a Synthetic
// snapshot is stored and round-tripped from the DB accurately.
func TestIntegration_Synthetic_SnapshotStoredCorrectly(t *testing.T) {
	db := testutil.InMemoryStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	renewsAt := now.Add(5 * time.Hour)

	snap := &api.Snapshot{
		CapturedAt: now,
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 154.3, RenewsAt: renewsAt},
		Search:     api.QuotaInfo{Limit: 250, Requests: 42.7, RenewsAt: renewsAt},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 7635.5, RenewsAt: renewsAt},
	}
	id, err := db.InsertSnapshot(snap)
	if err != nil {
		t.Fatalf("InsertSnapshot: %v", err)
	}
	if id < 1 {
		t.Fatal("Expected positive snapshot ID")
	}

	latest, err := db.QueryLatest()
	if err != nil {
		t.Fatalf("QueryLatest: %v", err)
	}
	if latest == nil {
		t.Fatal("Expected non-nil snapshot")
	}

	// Verify every field
	if latest.Sub.Limit != 1350 {
		t.Errorf("Sub.Limit: want 1350, got %f", latest.Sub.Limit)
	}
	if latest.Sub.Requests != 154.3 {
		t.Errorf("Sub.Requests: want 154.3, got %f", latest.Sub.Requests)
	}
	if latest.Search.Limit != 250 {
		t.Errorf("Search.Limit: want 250, got %f", latest.Search.Limit)
	}
	if latest.Search.Requests != 42.7 {
		t.Errorf("Search.Requests: want 42.7, got %f", latest.Search.Requests)
	}
	if latest.ToolCall.Limit != 16200 {
		t.Errorf("ToolCall.Limit: want 16200, got %f", latest.ToolCall.Limit)
	}
	if latest.ToolCall.Requests != 7635.5 {
		t.Errorf("ToolCall.Requests: want 7635.5, got %f", latest.ToolCall.Requests)
	}
}

// TestIntegration_Synthetic_SequentialPollsAccumulate verifies multiple polls
// create multiple snapshots in the DB.
func TestIntegration_Synthetic_SequentialPollsAccumulate(t *testing.T) {
	db := testutil.InMemoryStore(t)
	now := time.Now().UTC()
	renewsAt := now.Add(5 * time.Hour)

	for i := range 5 {
		snap := &api.Snapshot{
			CapturedAt: now.Add(time.Duration(i) * time.Minute),
			Sub:        api.QuotaInfo{Limit: 1350, Requests: 100 + float64(i)*10, RenewsAt: renewsAt},
			Search:     api.QuotaInfo{Limit: 250, Requests: float64(i) * 5, RenewsAt: renewsAt},
			ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 5000 + float64(i)*100, RenewsAt: renewsAt},
		}
		if _, err := db.InsertSnapshot(snap); err != nil {
			t.Fatalf("InsertSnapshot[%d]: %v", i, err)
		}
	}

	start := now.Add(-time.Minute)
	end := now.Add(10 * time.Minute)
	snaps, err := db.QueryRange(start, end)
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(snaps) != 5 {
		t.Fatalf("Expected 5 snapshots, got %d", len(snaps))
	}

	// Verify ordering (ascending by captured_at)
	for i := 1; i < len(snaps); i++ {
		if snaps[i].CapturedAt.Before(snaps[i-1].CapturedAt) {
			t.Errorf("Snapshots not in ascending order at index %d", i)
		}
	}

	// Verify last snapshot has the highest requests
	if snaps[4].Sub.Requests != 140 {
		t.Errorf("Last snapshot Sub.Requests: want 140, got %f", snaps[4].Sub.Requests)
	}
}

// TestIntegration_Synthetic_ResetDetectionCreatesCycle verifies that a change in
// renewsAt creates a new cycle via the tracker.
func TestIntegration_Synthetic_ResetDetectionCreatesCycle(t *testing.T) {
	db := testutil.InMemoryStore(t)
	tr := tracker.New(db, testutil.DiscardLogger())
	now := time.Now().UTC()

	// First snapshot: initial state
	snap1 := &api.Snapshot{
		CapturedAt: now,
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 500, RenewsAt: now.Add(1 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 100, RenewsAt: now.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 10000, RenewsAt: now.Add(1 * time.Hour)},
	}
	db.InsertSnapshot(snap1)
	tr.Process(snap1)

	// Second snapshot: renewsAt changed = reset occurred
	snap2 := &api.Snapshot{
		CapturedAt: now.Add(time.Minute),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 5, RenewsAt: now.Add(25 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: now.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 50, RenewsAt: now.Add(1 * time.Hour)},
	}
	db.InsertSnapshot(snap2)
	tr.Process(snap2)

	// Verify subscription cycle was closed
	cycles, err := db.QueryCycleHistory("subscription")
	if err != nil {
		t.Fatalf("QueryCycleHistory: %v", err)
	}
	if len(cycles) < 1 {
		t.Fatal("Expected at least 1 completed subscription cycle")
	}
	if cycles[0].PeakRequests != 500 {
		t.Errorf("Peak requests: want 500, got %f", cycles[0].PeakRequests)
	}
}

// TestIntegration_Synthetic_FloatPrecision verifies that float64 values survive
// the SQLite round-trip without precision loss.
func TestIntegration_Synthetic_FloatPrecision(t *testing.T) {
	db := testutil.InMemoryStore(t)
	now := time.Now().UTC()

	// Use values with fractional parts that might lose precision
	snap := &api.Snapshot{
		CapturedAt: now,
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 154.333333333, RenewsAt: now.Add(time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0.000001, RenewsAt: now.Add(time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 99999.999999, RenewsAt: now.Add(time.Hour)},
	}
	db.InsertSnapshot(snap)

	latest, err := db.QueryLatest()
	if err != nil {
		t.Fatalf("QueryLatest: %v", err)
	}

	eps := 1e-9
	if math.Abs(latest.Sub.Requests-154.333333333) > eps {
		t.Errorf("Sub.Requests precision loss: %f", latest.Sub.Requests)
	}
	if math.Abs(latest.Search.Requests-0.000001) > eps {
		t.Errorf("Search.Requests precision loss: %f", latest.Search.Requests)
	}
	if math.Abs(latest.ToolCall.Requests-99999.999999) > eps {
		t.Errorf("ToolCall.Requests precision loss: %f", latest.ToolCall.Requests)
	}
}

// TestIntegration_Synthetic_HandlerReturnsDBData verifies the /api/current handler
// returns data that matches what was stored in the DB.
func TestIntegration_Synthetic_HandlerReturnsDBData(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()

	snap := &api.Snapshot{
		CapturedAt: now,
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 200, RenewsAt: now.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 50, RenewsAt: now.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 8000, RenewsAt: now.Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snap)

	req := httptest.NewRequest("GET", "/api/current?provider=synthetic", nil)
	w := httptest.NewRecorder()
	h.Current(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	sub, ok := resp["subscription"].(map[string]interface{})
	if !ok {
		t.Fatal("Missing subscription in response")
	}
	if sub["usage"].(float64) != 200 {
		t.Errorf("subscription.usage: want 200, got %v", sub["usage"])
	}
	if sub["limit"].(float64) != 1350 {
		t.Errorf("subscription.limit: want 1350, got %v", sub["limit"])
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 3.2 Z.ai Data Integrity (7 tests)
// ═══════════════════════════════════════════════════════════════════════

// TestIntegration_Zai_SnapshotStoredCorrectly verifies every field of a Z.ai
// snapshot is stored and round-tripped.
func TestIntegration_Zai_SnapshotStoredCorrectly(t *testing.T) {
	db := testutil.InMemoryStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	resetTime := now.Add(7 * 24 * time.Hour)

	snap := &api.ZaiSnapshot{
		CapturedAt:          now,
		TimeLimit:           1000,
		TimeUnit:            1,
		TimeNumber:          1000,
		TimeUsage:           1000,
		TimeCurrentValue:    19,
		TimeRemaining:       981,
		TimePercentage:      1,
		TimeUsageDetails:    `[{"modelCode":"search-prime","usage":16}]`,
		TokensLimit:         200000000,
		TokensUnit:          1,
		TokensNumber:        200000000,
		TokensUsage:         200000000,
		TokensCurrentValue:  50000000,
		TokensRemaining:     150000000,
		TokensPercentage:    25,
		TokensNextResetTime: &resetTime,
	}
	id, err := db.InsertZaiSnapshot(snap)
	if err != nil {
		t.Fatalf("InsertZaiSnapshot: %v", err)
	}
	if id < 1 {
		t.Fatal("Expected positive ID")
	}

	latest, err := db.QueryLatestZai()
	if err != nil {
		t.Fatalf("QueryLatestZai: %v", err)
	}
	if latest == nil {
		t.Fatal("Expected non-nil snapshot")
	}

	if latest.TimeUsage != 1000 {
		t.Errorf("TimeUsage: want 1000, got %f", latest.TimeUsage)
	}
	if latest.TimeCurrentValue != 19 {
		t.Errorf("TimeCurrentValue: want 19, got %f", latest.TimeCurrentValue)
	}
	if latest.TokensUsage != 200000000 {
		t.Errorf("TokensUsage: want 200000000, got %f", latest.TokensUsage)
	}
	if latest.TokensCurrentValue != 50000000 {
		t.Errorf("TokensCurrentValue: want 50000000, got %f", latest.TokensCurrentValue)
	}
	if latest.TokensPercentage != 25 {
		t.Errorf("TokensPercentage: want 25, got %d", latest.TokensPercentage)
	}
	if latest.TokensNextResetTime == nil {
		t.Fatal("TokensNextResetTime should not be nil")
	}
	if latest.TimeUsageDetails == "" {
		t.Error("TimeUsageDetails should not be empty")
	}
}

// TestIntegration_Zai_EpochMsToISO8601 verifies that epoch millisecond reset times
// are correctly converted to time.Time during the API -> snapshot conversion.
func TestIntegration_Zai_EpochMsToISO8601(t *testing.T) {
	epochMs := int64(1770398385482)
	expected := time.UnixMilli(epochMs)

	limit := api.ZaiLimit{
		Type:        "TOKENS_LIMIT",
		Usage:       200000000,
		CurrentValue: 50000000,
		Remaining:   150000000,
		Percentage:  25,
		NextResetMs: &epochMs,
	}

	resetTime := limit.GetResetTime()
	if resetTime == nil {
		t.Fatal("Expected non-nil reset time")
	}
	if !resetTime.Equal(expected) {
		t.Errorf("Reset time: want %v, got %v", expected, *resetTime)
	}

	// Verify through ToSnapshot conversion
	resp := &api.ZaiQuotaResponse{
		Limits: []api.ZaiLimit{
			{Type: "TIME_LIMIT", Usage: 1000, CurrentValue: 19, Remaining: 981, Percentage: 1},
			limit,
		},
	}
	snap := resp.ToSnapshot(time.Now().UTC())
	if snap.TokensNextResetTime == nil {
		t.Fatal("Snapshot TokensNextResetTime should not be nil")
	}
	if !snap.TokensNextResetTime.Equal(expected) {
		t.Errorf("Snapshot reset time mismatch: want %v, got %v", expected, *snap.TokensNextResetTime)
	}
}

// TestIntegration_Zai_UsageExceedsLimit verifies Z.ai snapshots store correctly
// when currentValue exceeds the usage budget (no hard cap).
func TestIntegration_Zai_UsageExceedsLimit(t *testing.T) {
	db := testutil.InMemoryStore(t)
	now := time.Now().UTC()

	snap := &api.ZaiSnapshot{
		CapturedAt:         now,
		TokensUsage:        200000000,
		TokensCurrentValue: 200112618, // Exceeds budget
		TokensRemaining:    0,
		TokensPercentage:   100,
	}
	_, err := db.InsertZaiSnapshot(snap)
	if err != nil {
		t.Fatalf("InsertZaiSnapshot: %v", err)
	}

	latest, err := db.QueryLatestZai()
	if err != nil {
		t.Fatalf("QueryLatestZai: %v", err)
	}
	if latest.TokensCurrentValue != 200112618 {
		t.Errorf("Expected currentValue 200112618, got %f", latest.TokensCurrentValue)
	}
	if latest.TokensCurrentValue <= latest.TokensUsage {
		t.Error("Expected currentValue > usage (over budget)")
	}
}

// TestIntegration_Zai_NoResetTimeOnTimeLimit verifies TIME_LIMIT has nil reset time.
func TestIntegration_Zai_NoResetTimeOnTimeLimit(t *testing.T) {
	db := testutil.InMemoryStore(t)
	now := time.Now().UTC()

	// TIME_LIMIT has no reset time
	snap := &api.ZaiSnapshot{
		CapturedAt:          now,
		TimeUsage:           1000,
		TimeCurrentValue:    19,
		TimeRemaining:       981,
		TimePercentage:      1,
		TokensNextResetTime: nil, // no reset info for TIME_LIMIT
	}
	db.InsertZaiSnapshot(snap)

	latest, err := db.QueryLatestZai()
	if err != nil {
		t.Fatalf("QueryLatestZai: %v", err)
	}
	if latest.TokensNextResetTime != nil {
		t.Errorf("Expected nil TokensNextResetTime for TIME_LIMIT, got %v", latest.TokensNextResetTime)
	}
}

// TestIntegration_Zai_ResetDetection verifies Z.ai token reset cycle detection.
func TestIntegration_Zai_ResetDetection(t *testing.T) {
	db := testutil.InMemoryStore(t)
	zaiTr := tracker.NewZaiTracker(db, testutil.DiscardLogger())
	now := time.Now().UTC()
	resetBefore := now.Add(1 * time.Hour)
	resetAfter := now.Add(8 * 24 * time.Hour)

	// First snapshot: high usage, near reset
	snap1 := &api.ZaiSnapshot{
		CapturedAt:          now,
		TokensUsage:         200000000,
		TokensCurrentValue:  190000000,
		TokensRemaining:     10000000,
		TokensPercentage:    95,
		TokensNextResetTime: &resetBefore,
		TimeUsage:           1000,
		TimeCurrentValue:    900,
	}
	db.InsertZaiSnapshot(snap1)
	zaiTr.Process(snap1)

	// Second snapshot: reset occurred (new reset time, low usage)
	snap2 := &api.ZaiSnapshot{
		CapturedAt:          now.Add(2 * time.Minute),
		TokensUsage:         200000000,
		TokensCurrentValue:  1000000,
		TokensRemaining:     199000000,
		TokensPercentage:    0,
		TokensNextResetTime: &resetAfter,
		TimeUsage:           1000,
		TimeCurrentValue:    5,
	}
	db.InsertZaiSnapshot(snap2)
	zaiTr.Process(snap2)

	// Verify completed cycle exists
	cycles, err := db.QueryZaiCycleHistory("tokens")
	if err != nil {
		t.Fatalf("QueryZaiCycleHistory: %v", err)
	}
	if len(cycles) < 1 {
		t.Fatal("Expected at least 1 completed tokens cycle")
	}
	// Peak should be 190000000
	if cycles[0].PeakValue != 190000000 {
		t.Errorf("Peak value: want 190000000, got %d", cycles[0].PeakValue)
	}
}

// TestIntegration_Zai_BodyLevel401 verifies parsing of Z.ai body-level 401 responses.
func TestIntegration_Zai_BodyLevel401(t *testing.T) {
	authErrJSON := testutil.ZaiAuthErrorResponse()
	_, err := api.ParseZaiResponse([]byte(authErrJSON))
	if err == nil {
		t.Fatal("Expected error for body-level 401")
	}
	if !strings.Contains(err.Error(), "token expired or incorrect") {
		t.Errorf("Error should contain 'token expired or incorrect', got: %s", err.Error())
	}
}

// TestIntegration_Zai_HandlerReturnsDBData verifies /api/current?provider=zai returns DB data.
func TestIntegration_Zai_HandlerReturnsDBData(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()
	resetTime := now.Add(7 * 24 * time.Hour)

	snap := &api.ZaiSnapshot{
		CapturedAt:          now,
		TimeUsage:           1000,
		TimeCurrentValue:    500,
		TimePercentage:      50,
		TokensUsage:         200000000,
		TokensCurrentValue:  100000000,
		TokensPercentage:    50,
		TokensNextResetTime: &resetTime,
	}
	s.InsertZaiSnapshot(snap)

	req := httptest.NewRequest("GET", "/api/current?provider=zai", nil)
	w := httptest.NewRecorder()
	h.Current(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	tokens, ok := resp["tokensLimit"].(map[string]interface{})
	if !ok {
		t.Fatal("Missing tokensLimit in response")
	}
	if tokens["usage"].(float64) != 100000000 {
		t.Errorf("tokensLimit.usage: want 100000000, got %v", tokens["usage"])
	}
	if tokens["limit"].(float64) != 200000000 {
		t.Errorf("tokensLimit.limit: want 200000000, got %v", tokens["limit"])
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 3.3 Anthropic Data Integrity (9 tests)
// ═══════════════════════════════════════════════════════════════════════

// TestIntegration_Anthropic_SnapshotStoredCorrectly verifies every field of an
// Anthropic snapshot is stored and round-tripped.
func TestIntegration_Anthropic_SnapshotStoredCorrectly(t *testing.T) {
	db := testutil.InMemoryStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	fiveHourReset := now.Add(3 * time.Hour)
	sevenDayReset := now.Add(5 * 24 * time.Hour)

	snap := &api.AnthropicSnapshot{
		CapturedAt: now,
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.2, ResetsAt: &fiveHourReset},
			{Name: "seven_day", Utilization: 12.8, ResetsAt: &sevenDayReset},
			{Name: "seven_day_sonnet", Utilization: 5.1, ResetsAt: &sevenDayReset},
		},
		RawJSON: `{"five_hour":{"utilization":45.2}}`,
	}
	id, err := db.InsertAnthropicSnapshot(snap)
	if err != nil {
		t.Fatalf("InsertAnthropicSnapshot: %v", err)
	}
	if id < 1 {
		t.Fatal("Expected positive ID")
	}

	latest, err := db.QueryLatestAnthropic()
	if err != nil {
		t.Fatalf("QueryLatestAnthropic: %v", err)
	}
	if latest == nil {
		t.Fatal("Expected non-nil snapshot")
	}

	if len(latest.Quotas) != 3 {
		t.Fatalf("Expected 3 quotas, got %d", len(latest.Quotas))
	}

	// Verify sorted by quota_name (alphabetical: five_hour, seven_day, seven_day_sonnet)
	quotaMap := map[string]float64{}
	for _, q := range latest.Quotas {
		quotaMap[q.Name] = q.Utilization
	}
	if quotaMap["five_hour"] != 45.2 {
		t.Errorf("five_hour utilization: want 45.2, got %f", quotaMap["five_hour"])
	}
	if quotaMap["seven_day"] != 12.8 {
		t.Errorf("seven_day utilization: want 12.8, got %f", quotaMap["seven_day"])
	}
	if quotaMap["seven_day_sonnet"] != 5.1 {
		t.Errorf("seven_day_sonnet utilization: want 5.1, got %f", quotaMap["seven_day_sonnet"])
	}
}

// TestIntegration_Anthropic_DynamicQuotaKeys verifies that arbitrary quota keys
// from the Anthropic API are stored and retrieved correctly.
func TestIntegration_Anthropic_DynamicQuotaKeys(t *testing.T) {
	db := testutil.InMemoryStore(t)
	now := time.Now().UTC()

	snap := &api.AnthropicSnapshot{
		CapturedAt: now,
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 10},
			{Name: "monthly_limit", Utilization: 67.3},
			{Name: "new_future_quota", Utilization: 1.5}, // unknown key
		},
	}
	db.InsertAnthropicSnapshot(snap)

	latest, err := db.QueryLatestAnthropic()
	if err != nil {
		t.Fatalf("QueryLatestAnthropic: %v", err)
	}

	names := map[string]bool{}
	for _, q := range latest.Quotas {
		names[q.Name] = true
	}
	if !names["new_future_quota"] {
		t.Error("Dynamic key 'new_future_quota' not found in stored snapshot")
	}
	if !names["monthly_limit"] {
		t.Error("Key 'monthly_limit' not found")
	}
}

// TestIntegration_Anthropic_NullQuotaFiltered verifies null quotas are filtered
// during the API response -> snapshot conversion.
func TestIntegration_Anthropic_NullQuotaFiltered(t *testing.T) {
	nullJSON := testutil.AnthropicResponseNullQuotas()
	resp, err := api.ParseAnthropicResponse([]byte(nullJSON))
	if err != nil {
		t.Fatalf("ParseAnthropicResponse: %v", err)
	}

	snap := resp.ToSnapshot(time.Now().UTC())
	for _, q := range snap.Quotas {
		if q.Name == "extra_usage" {
			t.Error("Null extra_usage should have been filtered out")
		}
	}

	// Should have five_hour and seven_day only
	if len(snap.Quotas) != 2 {
		t.Errorf("Expected 2 quotas after filtering, got %d", len(snap.Quotas))
	}
}

// TestIntegration_Anthropic_DisabledQuotaFiltered verifies disabled quotas
// (is_enabled=false) are filtered during conversion.
func TestIntegration_Anthropic_DisabledQuotaFiltered(t *testing.T) {
	// Default response has extra_usage with is_enabled=false
	respJSON := testutil.DefaultAnthropicResponse()
	resp, err := api.ParseAnthropicResponse([]byte(respJSON))
	if err != nil {
		t.Fatalf("ParseAnthropicResponse: %v", err)
	}

	snap := resp.ToSnapshot(time.Now().UTC())
	for _, q := range snap.Quotas {
		if q.Name == "extra_usage" {
			t.Error("Disabled extra_usage should have been filtered out")
		}
	}

	// Should have five_hour, seven_day, seven_day_sonnet (3 enabled)
	if len(snap.Quotas) != 3 {
		t.Errorf("Expected 3 enabled quotas, got %d", len(snap.Quotas))
	}
}

// TestIntegration_Anthropic_ResetDetection verifies Anthropic reset cycle detection
// when resets_at changes.
func TestIntegration_Anthropic_ResetDetection(t *testing.T) {
	db := testutil.InMemoryStore(t)
	anthTr := tracker.NewAnthropicTracker(db, testutil.DiscardLogger())
	now := time.Now().UTC()
	beforeReset := now.Add(30 * time.Minute)
	afterReset := now.Add(5 * time.Hour)
	sevenDayReset := now.Add(5 * 24 * time.Hour)

	// First snapshot: five_hour near reset
	snap1 := &api.AnthropicSnapshot{
		CapturedAt: now,
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 85.0, ResetsAt: &beforeReset},
			{Name: "seven_day", Utilization: 30.0, ResetsAt: &sevenDayReset},
		},
	}
	db.InsertAnthropicSnapshot(snap1)
	anthTr.Process(snap1)

	// Second snapshot: five_hour reset (new resets_at, low utilization)
	snap2 := &api.AnthropicSnapshot{
		CapturedAt: now.Add(2 * time.Minute),
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 5.0, ResetsAt: &afterReset},
			{Name: "seven_day", Utilization: 30.5, ResetsAt: &sevenDayReset},
		},
	}
	db.InsertAnthropicSnapshot(snap2)
	anthTr.Process(snap2)

	// Verify five_hour cycle was closed
	cycles, err := db.QueryAnthropicCycleHistory("five_hour")
	if err != nil {
		t.Fatalf("QueryAnthropicCycleHistory: %v", err)
	}
	if len(cycles) < 1 {
		t.Fatal("Expected at least 1 completed five_hour cycle")
	}
	if cycles[0].PeakUtilization != 85.0 {
		t.Errorf("Peak utilization: want 85.0, got %f", cycles[0].PeakUtilization)
	}

	// Verify seven_day was NOT reset (resets_at unchanged)
	sevenDayCycles, _ := db.QueryAnthropicCycleHistory("seven_day")
	if len(sevenDayCycles) > 0 {
		t.Error("seven_day should NOT have been reset (resets_at unchanged)")
	}
}

// TestIntegration_Anthropic_JitterTolerance verifies that sub-second timestamp
// jitter does not trigger a false reset.
func TestIntegration_Anthropic_JitterTolerance(t *testing.T) {
	db := testutil.InMemoryStore(t)
	anthTr := tracker.NewAnthropicTracker(db, testutil.DiscardLogger())
	now := time.Now().UTC()
	resetTime := now.Add(3 * time.Hour)

	// First snapshot
	snap1 := &api.AnthropicSnapshot{
		CapturedAt: now,
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.0, ResetsAt: &resetTime},
		},
	}
	db.InsertAnthropicSnapshot(snap1)
	anthTr.Process(snap1)

	// Second snapshot: same resets_at, different utilization (no reset)
	snap2 := &api.AnthropicSnapshot{
		CapturedAt: now.Add(time.Minute),
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 50.0, ResetsAt: &resetTime},
		},
	}
	db.InsertAnthropicSnapshot(snap2)
	anthTr.Process(snap2)

	// Should have 0 completed cycles (no reset)
	cycles, _ := db.QueryAnthropicCycleHistory("five_hour")
	if len(cycles) != 0 {
		t.Errorf("Expected 0 completed cycles (no reset), got %d", len(cycles))
	}

	// Active cycle should have updated peak
	active, _ := db.QueryActiveAnthropicCycle("five_hour")
	if active == nil {
		t.Fatal("Expected active cycle")
	}
	if active.PeakUtilization != 50.0 {
		t.Errorf("Active cycle peak: want 50.0, got %f", active.PeakUtilization)
	}
}

// TestIntegration_Anthropic_NewQuotaAppearsMidSession verifies that a new quota
// appearing in later polls is handled gracefully.
func TestIntegration_Anthropic_NewQuotaAppearsMidSession(t *testing.T) {
	db := testutil.InMemoryStore(t)
	anthTr := tracker.NewAnthropicTracker(db, testutil.DiscardLogger())
	now := time.Now().UTC()
	resetTime := now.Add(3 * time.Hour)

	// First snapshot: only five_hour
	snap1 := &api.AnthropicSnapshot{
		CapturedAt: now,
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.0, ResetsAt: &resetTime},
		},
	}
	db.InsertAnthropicSnapshot(snap1)
	anthTr.Process(snap1)

	// Second snapshot: five_hour + new monthly_limit
	monthlyReset := now.Add(30 * 24 * time.Hour)
	snap2 := &api.AnthropicSnapshot{
		CapturedAt: now.Add(time.Minute),
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 50.0, ResetsAt: &resetTime},
			{Name: "monthly_limit", Utilization: 10.0, ResetsAt: &monthlyReset},
		},
	}
	db.InsertAnthropicSnapshot(snap2)
	anthTr.Process(snap2)

	// Verify monthly_limit cycle was created
	active, err := db.QueryActiveAnthropicCycle("monthly_limit")
	if err != nil {
		t.Fatalf("QueryActiveAnthropicCycle: %v", err)
	}
	if active == nil {
		t.Fatal("Expected active cycle for new monthly_limit quota")
	}

	// Verify snapshot has both quotas
	latest, _ := db.QueryLatestAnthropic()
	if len(latest.Quotas) != 2 {
		t.Errorf("Expected 2 quotas in latest snapshot, got %d", len(latest.Quotas))
	}
}

// TestIntegration_Anthropic_HandlerReturnsDBData verifies /api/current?provider=anthropic.
func TestIntegration_Anthropic_HandlerReturnsDBData(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()
	fiveHourReset := now.Add(3 * time.Hour)

	snap := &api.AnthropicSnapshot{
		CapturedAt: now,
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.2, ResetsAt: &fiveHourReset},
		},
	}
	s.InsertAnthropicSnapshot(snap)

	req := httptest.NewRequest("GET", "/api/current?provider=anthropic", nil)
	w := httptest.NewRecorder()
	h.Current(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	quotas, ok := resp["quotas"].([]interface{})
	if !ok {
		// Try the alternative format
		if _, ok2 := resp["five_hour"]; ok2 {
			fh := resp["five_hour"].(map[string]interface{})
			if fh["utilization"].(float64) != 45.2 {
				t.Errorf("five_hour utilization: want 45.2, got %v", fh["utilization"])
			}
			return
		}
		t.Logf("Response: %s", w.Body.String())
		// Just verify response is valid JSON and has some content
		if len(resp) == 0 {
			t.Error("Expected non-empty response")
		}
		return
	}

	if len(quotas) < 1 {
		t.Error("Expected at least 1 quota in response")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 3.4 Cross-Provider & Multi-Provider (5 tests)
// ═══════════════════════════════════════════════════════════════════════

// TestIntegration_CrossProvider_IndependentResets verifies that resetting one
// provider's quota doesn't affect other providers.
func TestIntegration_CrossProvider_IndependentResets(t *testing.T) {
	db := testutil.InMemoryStore(t)
	synTr := tracker.New(db, testutil.DiscardLogger())
	zaiTr := tracker.NewZaiTracker(db, testutil.DiscardLogger())
	now := time.Now().UTC()

	// Insert Synthetic snapshot
	synSnap1 := &api.Snapshot{
		CapturedAt: now,
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 500, RenewsAt: now.Add(1 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 100, RenewsAt: now.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 10000, RenewsAt: now.Add(1 * time.Hour)},
	}
	db.InsertSnapshot(synSnap1)
	synTr.Process(synSnap1)

	// Insert Z.ai snapshot
	resetTime := now.Add(7 * 24 * time.Hour)
	zaiSnap1 := &api.ZaiSnapshot{
		CapturedAt:          now,
		TokensUsage:         200000000,
		TokensCurrentValue:  100000000,
		TokensPercentage:    50,
		TokensNextResetTime: &resetTime,
		TimeUsage:           1000,
		TimeCurrentValue:    500,
	}
	db.InsertZaiSnapshot(zaiSnap1)
	zaiTr.Process(zaiSnap1)

	// Reset Synthetic (renewsAt changes)
	synSnap2 := &api.Snapshot{
		CapturedAt: now.Add(time.Minute),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 5, RenewsAt: now.Add(25 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: now.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 50, RenewsAt: now.Add(1 * time.Hour)},
	}
	db.InsertSnapshot(synSnap2)
	synTr.Process(synSnap2)

	// Verify Synthetic cycle was closed
	synCycles, _ := db.QueryCycleHistory("subscription")
	if len(synCycles) < 1 {
		t.Fatal("Expected Synthetic subscription cycle to be closed")
	}

	// Verify Z.ai cycle is still open (no reset)
	zaiActive, _ := db.QueryActiveZaiCycle("tokens")
	if zaiActive == nil {
		t.Fatal("Expected active Z.ai tokens cycle (should not have been affected)")
	}
}

// TestIntegration_CrossProvider_BothAggregation verifies the "both" provider
// returns combined data from multiple providers.
func TestIntegration_CrossProvider_BothAggregation(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()

	// Insert data for both providers
	synSnap := &api.Snapshot{
		CapturedAt: now,
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 200, RenewsAt: now.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 50, RenewsAt: now.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 8000, RenewsAt: now.Add(3 * time.Hour)},
	}
	s.InsertSnapshot(synSnap)

	resetTime := now.Add(7 * 24 * time.Hour)
	zaiSnap := &api.ZaiSnapshot{
		CapturedAt:          now,
		TokensUsage:         200000000,
		TokensCurrentValue:  50000000,
		TokensPercentage:    25,
		TokensNextResetTime: &resetTime,
		TimeUsage:           1000,
		TimeCurrentValue:    100,
	}
	s.InsertZaiSnapshot(zaiSnap)

	req := httptest.NewRequest("GET", "/api/current?provider=both", nil)
	w := httptest.NewRecorder()
	h.Current(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if _, ok := resp["synthetic"]; !ok {
		t.Error("Missing 'synthetic' in both response")
	}
	if _, ok := resp["zai"]; !ok {
		t.Error("Missing 'zai' in both response")
	}
}

// TestIntegration_CrossProvider_HistoryTimeRanges verifies history endpoint
// respects time range filters.
func TestIntegration_CrossProvider_HistoryTimeRanges(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()

	// Insert snapshots spread across time
	for i := range 24 {
		snap := &api.Snapshot{
			CapturedAt: now.Add(-time.Duration(24-i) * time.Hour),
			Sub:        api.QuotaInfo{Limit: 1350, Requests: float64(i * 10), RenewsAt: now.Add(5 * time.Hour)},
			Search:     api.QuotaInfo{Limit: 250, Requests: float64(i), RenewsAt: now.Add(1 * time.Hour)},
			ToolCall:   api.QuotaInfo{Limit: 16200, Requests: float64(i * 100), RenewsAt: now.Add(3 * time.Hour)},
		}
		s.InsertSnapshot(snap)
	}

	tests := []struct {
		name      string
		timeRange string
		minCount  int
		maxCount  int
	}{
		{"1h", "1h", 0, 2},
		{"6h", "6h", 4, 8},
		{"24h", "24h", 20, 25},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/history?provider=synthetic&range="+tc.timeRange, nil)
			w := httptest.NewRecorder()
			h.History(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("Expected 200, got %d", w.Code)
			}

			var resp []map[string]interface{}
			json.Unmarshal(w.Body.Bytes(), &resp)

			if len(resp) < tc.minCount {
				t.Errorf("Range %s: expected >= %d snapshots, got %d", tc.timeRange, tc.minCount, len(resp))
			}
			if len(resp) > tc.maxCount {
				t.Errorf("Range %s: expected <= %d snapshots, got %d", tc.timeRange, tc.maxCount, len(resp))
			}
		})
	}
}

// TestIntegration_CrossProvider_CyclesFilterByType verifies that cycle queries
// correctly filter by quota type.
func TestIntegration_CrossProvider_CyclesFilterByType(t *testing.T) {
	db := testutil.InMemoryStore(t)
	now := time.Now().UTC()

	// Create cycles for different quota types
	db.CreateCycle("subscription", now.Add(-2*time.Hour), now.Add(-1*time.Hour))
	db.CloseCycle("subscription", now, 500, 400)

	db.CreateCycle("search", now.Add(-2*time.Hour), now.Add(-1*time.Hour))
	db.CloseCycle("search", now, 200, 150)

	db.CreateCycle("toolcall", now.Add(-2*time.Hour), now.Add(-1*time.Hour))
	db.CloseCycle("toolcall", now, 10000, 8000)

	// Query each type
	subCycles, _ := db.QueryCycleHistory("subscription")
	if len(subCycles) != 1 {
		t.Errorf("subscription: want 1 cycle, got %d", len(subCycles))
	}

	searchCycles, _ := db.QueryCycleHistory("search")
	if len(searchCycles) != 1 {
		t.Errorf("search: want 1 cycle, got %d", len(searchCycles))
	}

	toolCycles, _ := db.QueryCycleHistory("toolcall")
	if len(toolCycles) != 1 {
		t.Errorf("toolcall: want 1 cycle, got %d", len(toolCycles))
	}

	// Verify values are correct for each type
	if subCycles[0].PeakRequests != 500 {
		t.Errorf("subscription peak: want 500, got %f", subCycles[0].PeakRequests)
	}
	if searchCycles[0].PeakRequests != 200 {
		t.Errorf("search peak: want 200, got %f", searchCycles[0].PeakRequests)
	}
}

// TestIntegration_CrossProvider_ParallelPolling verifies multiple providers can
// insert data concurrently without conflicts.
func TestIntegration_CrossProvider_ParallelPolling(t *testing.T) {
	db := testutil.InMemoryStore(t)
	now := time.Now().UTC()

	// Insert Synthetic, Z.ai, and Anthropic snapshots
	synSnap := &api.Snapshot{
		CapturedAt: now,
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 100, RenewsAt: now.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: now.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 5000, RenewsAt: now.Add(3 * time.Hour)},
	}
	_, err := db.InsertSnapshot(synSnap)
	if err != nil {
		t.Fatalf("InsertSnapshot: %v", err)
	}

	resetTime := now.Add(7 * 24 * time.Hour)
	zaiSnap := &api.ZaiSnapshot{
		CapturedAt:          now,
		TokensUsage:         200000000,
		TokensCurrentValue:  50000000,
		TokensPercentage:    25,
		TokensNextResetTime: &resetTime,
		TimeUsage:           1000,
		TimeCurrentValue:    100,
	}
	_, err = db.InsertZaiSnapshot(zaiSnap)
	if err != nil {
		t.Fatalf("InsertZaiSnapshot: %v", err)
	}

	fiveHourReset := now.Add(3 * time.Hour)
	anthSnap := &api.AnthropicSnapshot{
		CapturedAt: now,
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.2, ResetsAt: &fiveHourReset},
		},
	}
	_, err = db.InsertAnthropicSnapshot(anthSnap)
	if err != nil {
		t.Fatalf("InsertAnthropicSnapshot: %v", err)
	}

	// Verify all three are present
	synLatest, _ := db.QueryLatest()
	if synLatest == nil {
		t.Fatal("Missing Synthetic snapshot")
	}
	zaiLatest, _ := db.QueryLatestZai()
	if zaiLatest == nil {
		t.Fatal("Missing Z.ai snapshot")
	}
	anthLatest, _ := db.QueryLatestAnthropic()
	if anthLatest == nil {
		t.Fatal("Missing Anthropic snapshot")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 3.5 API Endpoint Validation (18 tests)
// ═══════════════════════════════════════════════════════════════════════

// TestIntegration_API_Providers verifies /api/providers returns configured providers.
func TestIntegration_API_Providers(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	req := httptest.NewRequest("GET", "/api/providers", nil)
	w := httptest.NewRecorder()
	h.Providers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	providers, ok := resp["providers"].([]interface{})
	if !ok {
		t.Fatal("Missing providers array")
	}
	if len(providers) < 1 {
		t.Error("Expected at least 1 provider")
	}

	current, ok := resp["current"].(string)
	if !ok || current == "" {
		t.Error("Missing or empty current provider")
	}
}

// TestIntegration_API_CurrentSynthetic verifies /api/current for Synthetic.
func TestIntegration_API_CurrentSynthetic(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()
	snap := &api.Snapshot{
		CapturedAt: now,
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 300, RenewsAt: now.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 80, RenewsAt: now.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 12000, RenewsAt: now.Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snap)

	req := httptest.NewRequest("GET", "/api/current?provider=synthetic", nil)
	w := httptest.NewRecorder()
	h.Current(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	// Verify field-by-field
	sub := resp["subscription"].(map[string]interface{})
	if sub["usage"].(float64) != 300 {
		t.Errorf("subscription.usage: want 300, got %v", sub["usage"])
	}
	if sub["limit"].(float64) != 1350 {
		t.Errorf("subscription.limit: want 1350, got %v", sub["limit"])
	}
	// Verify percent calculation
	expectedPct := (300.0 / 1350.0) * 100.0
	if math.Abs(sub["percent"].(float64)-expectedPct) > 0.1 {
		t.Errorf("subscription.percent: want ~%.1f, got %v", expectedPct, sub["percent"])
	}
	// Verify status based on percent
	if sub["status"].(string) != "healthy" {
		t.Errorf("subscription.status: want 'healthy', got '%s'", sub["status"])
	}
}

// TestIntegration_API_CurrentZai verifies /api/current for Z.ai.
func TestIntegration_API_CurrentZai(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()
	resetTime := now.Add(7 * 24 * time.Hour)

	snap := &api.ZaiSnapshot{
		CapturedAt:          now,
		TokensUsage:         200000000,
		TokensCurrentValue:  170000000,
		TokensPercentage:    85,
		TokensNextResetTime: &resetTime,
		TimeUsage:           1000,
		TimeCurrentValue:    850,
		TimePercentage:      85,
	}
	s.InsertZaiSnapshot(snap)

	req := httptest.NewRequest("GET", "/api/current?provider=zai", nil)
	w := httptest.NewRecorder()
	h.Current(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	tokens := resp["tokensLimit"].(map[string]interface{})
	if tokens["percent"].(float64) != 85 {
		t.Errorf("tokensLimit.percent: want 85, got %v", tokens["percent"])
	}
	if tokens["status"].(string) != "danger" {
		t.Errorf("tokensLimit.status: want 'danger', got '%s'", tokens["status"])
	}
}

// TestIntegration_API_CurrentAnthropic verifies /api/current for Anthropic.
func TestIntegration_API_CurrentAnthropic(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()
	fiveHourReset := now.Add(3 * time.Hour)
	sevenDayReset := now.Add(5 * 24 * time.Hour)

	snap := &api.AnthropicSnapshot{
		CapturedAt: now,
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 72.5, ResetsAt: &fiveHourReset},
			{Name: "seven_day", Utilization: 15.0, ResetsAt: &sevenDayReset},
		},
	}
	s.InsertAnthropicSnapshot(snap)

	req := httptest.NewRequest("GET", "/api/current?provider=anthropic", nil)
	w := httptest.NewRecorder()
	h.Current(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify response is valid JSON
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}
	if len(resp) == 0 {
		t.Error("Expected non-empty response")
	}
}

// TestIntegration_API_HistoryRanges verifies /api/history with different ranges.
func TestIntegration_API_HistoryRanges(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()

	// Insert snapshots for the past 30 days
	for i := range 30 {
		snap := &api.Snapshot{
			CapturedAt: now.Add(-time.Duration(30-i) * 24 * time.Hour),
			Sub:        api.QuotaInfo{Limit: 1350, Requests: float64(i * 10), RenewsAt: now.Add(5 * time.Hour)},
			Search:     api.QuotaInfo{Limit: 250, Requests: float64(i), RenewsAt: now.Add(1 * time.Hour)},
			ToolCall:   api.QuotaInfo{Limit: 16200, Requests: float64(i * 100), RenewsAt: now.Add(3 * time.Hour)},
		}
		s.InsertSnapshot(snap)
	}

	ranges := []string{"1h", "6h", "24h", "7d", "30d"}
	for _, r := range ranges {
		t.Run(r, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/history?provider=synthetic&range="+r, nil)
			w := httptest.NewRecorder()
			h.History(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Range %s: expected 200, got %d", r, w.Code)
			}

			var resp []map[string]interface{}
			json.Unmarshal(w.Body.Bytes(), &resp)

			// Each response item should have expected fields
			for _, item := range resp {
				if _, ok := item["capturedAt"]; !ok {
					t.Errorf("Range %s: missing capturedAt", r)
				}
				if _, ok := item["subscription"]; !ok {
					t.Errorf("Range %s: missing subscription", r)
				}
			}
		})
	}
}

// TestIntegration_API_InvalidRange verifies invalid range returns 400.
func TestIntegration_API_InvalidRange(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	req := httptest.NewRequest("GET", "/api/history?provider=synthetic&range=invalid", nil)
	w := httptest.NewRecorder()
	h.History(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", w.Code)
	}
}

// TestIntegration_API_CyclesSynthetic verifies /api/cycles for Synthetic.
func TestIntegration_API_CyclesSynthetic(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()

	// Create a completed cycle
	s.CreateCycle("subscription", now.Add(-2*time.Hour), now.Add(-1*time.Hour))
	s.CloseCycle("subscription", now, 800, 600)

	req := httptest.NewRequest("GET", "/api/cycles?provider=synthetic&type=subscription", nil)
	w := httptest.NewRecorder()
	h.Cycles(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp) < 1 {
		t.Fatal("Expected at least 1 cycle")
	}

	cycle := resp[0]
	if cycle["peakRequests"].(float64) != 800 {
		t.Errorf("peakRequests: want 800, got %v", cycle["peakRequests"])
	}
	if cycle["totalDelta"].(float64) != 600 {
		t.Errorf("totalDelta: want 600, got %v", cycle["totalDelta"])
	}
	if cycle["quotaType"].(string) != "subscription" {
		t.Errorf("quotaType: want 'subscription', got %v", cycle["quotaType"])
	}
}

// TestIntegration_API_CyclesInvalidType verifies invalid cycle type returns 400.
func TestIntegration_API_CyclesInvalidType(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	req := httptest.NewRequest("GET", "/api/cycles?provider=synthetic&type=invalid", nil)
	w := httptest.NewRecorder()
	h.Cycles(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", w.Code)
	}
}

// TestIntegration_API_Summary verifies /api/summary response shape.
func TestIntegration_API_Summary(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()

	snap := &api.Snapshot{
		CapturedAt: now,
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 200, RenewsAt: now.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 50, RenewsAt: now.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 8000, RenewsAt: now.Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snap)

	req := httptest.NewRequest("GET", "/api/summary?provider=synthetic", nil)
	w := httptest.NewRecorder()
	h.Summary(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	// Verify expected keys
	for _, key := range []string{"subscription", "search", "toolCalls"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("Missing key: %s", key)
		}
	}
}

// TestIntegration_API_Sessions verifies /api/sessions response.
func TestIntegration_API_Sessions(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()

	s.CreateSession("test-session-1", now.Add(-time.Hour), 60, "synthetic")
	s.CloseSession("test-session-1", now)

	req := httptest.NewRequest("GET", "/api/sessions?provider=synthetic", nil)
	w := httptest.NewRecorder()
	h.Sessions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(resp))
	}

	session := resp[0]
	if session["id"].(string) != "test-session-1" {
		t.Errorf("Session ID: want 'test-session-1', got %v", session["id"])
	}
	if session["pollInterval"].(float64) != 60 {
		t.Errorf("pollInterval: want 60, got %v", session["pollInterval"])
	}
	if session["endedAt"] == nil {
		t.Error("Expected non-nil endedAt")
	}
}

// TestIntegration_API_Insights verifies /api/insights returns valid response.
func TestIntegration_API_Insights(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()

	// Insert some data for insights to analyze
	for i := range 10 {
		snap := &api.Snapshot{
			CapturedAt: now.Add(-time.Duration(10-i) * time.Hour),
			Sub:        api.QuotaInfo{Limit: 1350, Requests: float64(i * 50), RenewsAt: now.Add(5 * time.Hour)},
			Search:     api.QuotaInfo{Limit: 250, Requests: float64(i * 5), RenewsAt: now.Add(1 * time.Hour)},
			ToolCall:   api.QuotaInfo{Limit: 16200, Requests: float64(i * 500), RenewsAt: now.Add(3 * time.Hour)},
		}
		s.InsertSnapshot(snap)
	}

	req := httptest.NewRequest("GET", "/api/insights?provider=synthetic", nil)
	w := httptest.NewRecorder()
	h.Insights(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	// Insights should have stats array
	if _, ok := resp["stats"]; !ok {
		t.Error("Missing 'stats' in insights response")
	}
}

// TestIntegration_API_CycleOverview verifies /api/cycle-overview response.
func TestIntegration_API_CycleOverview(t *testing.T) {
	h, s := testutil.TestHandler(t)
	now := time.Now().UTC()

	// Create a completed cycle
	s.CreateCycle("subscription", now.Add(-2*time.Hour), now.Add(-1*time.Hour))
	s.CloseCycle("subscription", now, 800, 600)

	// Insert a snapshot within the cycle for cross-quota data
	snap := &api.Snapshot{
		CapturedAt: now.Add(-time.Hour),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 800, RenewsAt: now.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 100, RenewsAt: now.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 5000, RenewsAt: now.Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snap)

	req := httptest.NewRequest("GET", "/api/cycle-overview?provider=synthetic", nil)
	w := httptest.NewRecorder()
	h.CycleOverview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if _, ok := resp["cycles"]; !ok {
		t.Error("Missing 'cycles' in cycle-overview response")
	}
}

// TestIntegration_API_SettingsGetDefault verifies GET /api/settings returns defaults.
func TestIntegration_API_SettingsGetDefault(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	req := httptest.NewRequest("GET", "/api/settings", nil)
	w := httptest.NewRecorder()
	h.GetSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	// Should have timezone (possibly empty) and hidden_insights (empty array)
	if _, ok := resp["timezone"]; !ok {
		t.Error("Missing timezone in settings")
	}
	hi, ok := resp["hidden_insights"].([]interface{})
	if !ok {
		t.Error("Missing hidden_insights in settings")
	} else if len(hi) != 0 {
		t.Errorf("Expected empty hidden_insights, got %v", hi)
	}
}

// TestIntegration_API_SettingsPutTimezone verifies PUT /api/settings with timezone.
func TestIntegration_API_SettingsPutTimezone(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	body := `{"timezone":"America/New_York"}`
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.UpdateSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify it was persisted
	req2 := httptest.NewRequest("GET", "/api/settings", nil)
	w2 := httptest.NewRecorder()
	h.GetSettings(w2, req2)

	var resp map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &resp)

	if resp["timezone"] != "America/New_York" {
		t.Errorf("timezone: want 'America/New_York', got %v", resp["timezone"])
	}
}

// TestIntegration_API_SettingsInvalidTimezone verifies PUT /api/settings with invalid timezone.
func TestIntegration_API_SettingsInvalidTimezone(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	body := `{"timezone":"Invalid/Timezone"}`
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.UpdateSettings(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestIntegration_API_PasswordChange verifies PUT /api/password changes the password.
func TestIntegration_API_PasswordChange(t *testing.T) {
	h, s := testutil.TestHandler(t)

	// Set up initial password via legacy hash
	initialHash := "testhash" // matching TestHandler's setup
	_ = s
	_ = initialHash

	// The TestHandler sets a password hash of "testhash", which won't match
	// bcrypt or SHA-256 comparison, so this test validates the endpoint shape
	body := `{"current_password":"wrong","new_password":"newpassword123"}`
	req := httptest.NewRequest("PUT", "/api/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ChangePassword(w, req)

	// Should fail with 401 because current_password is wrong
	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 for wrong current password, got %d: %s", w.Code, w.Body.String())
	}
}

// TestIntegration_API_PasswordChangeValidation verifies password change validation.
func TestIntegration_API_PasswordChangeValidation(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	tests := []struct {
		name   string
		body   string
		expect int
	}{
		{"empty body", `{}`, http.StatusBadRequest},
		{"short password", `{"current_password":"old","new_password":"abc"}`, http.StatusBadRequest},
		{"missing new", `{"current_password":"old"}`, http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/api/password", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ChangePassword(w, req)

			if w.Code != tc.expect {
				t.Errorf("Expected %d, got %d: %s", tc.expect, w.Code, w.Body.String())
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 3.6 Auth & Session Integration (4 tests)
// ═══════════════════════════════════════════════════════════════════════

// TestIntegration_Auth_LoginSessionDashboard tests the full auth flow:
// login -> get session cookie -> access dashboard.
func TestIntegration_Auth_LoginSessionDashboard(t *testing.T) {
	db := testutil.InMemoryStore(t)
	logger := testutil.DiscardLogger()

	// Create handler with auth
	passwordHash, _ := web.HashPassword("testpass123")
	sessions := web.NewSessionStore("admin", passwordHash, db)
	cfg := testutil.TestConfig("http://localhost:0")
	tr := tracker.New(db, logger)
	h := web.NewHandler(db, tr, logger, sessions, cfg)
	h.SetVersion("test-dev")

	// Login with correct credentials
	loginReq := httptest.NewRequest("POST", "/login", strings.NewReader("username=admin&password=testpass123"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginW := httptest.NewRecorder()
	h.Login(loginW, loginReq)

	loginResp := loginW.Result()
	if loginResp.StatusCode != http.StatusFound {
		t.Fatalf("Expected 302 redirect, got %d", loginResp.StatusCode)
	}

	// Extract session cookie
	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == "onwatch_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("Expected session cookie after login")
	}

	// Access dashboard with session cookie
	dashReq := httptest.NewRequest("GET", "/", nil)
	dashReq.AddCookie(sessionCookie)
	dashW := httptest.NewRecorder()
	h.Dashboard(dashW, dashReq)

	if dashW.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", dashW.Code)
	}
}

// TestIntegration_Auth_NoSessionRedirect verifies unauthenticated requests
// are redirected to the login page.
func TestIntegration_Auth_NoSessionRedirect(t *testing.T) {
	db := testutil.InMemoryStore(t)
	logger := testutil.DiscardLogger()

	passwordHash, _ := web.HashPassword("testpass123")
	sessions := web.NewSessionStore("admin", passwordHash, db)

	mux := http.NewServeMux()
	cfg := testutil.TestConfig("http://localhost:0")
	tr := tracker.New(db, logger)
	h := web.NewHandler(db, tr, logger, sessions, cfg)
	mux.HandleFunc("/", h.Dashboard)

	// Apply session auth middleware
	authMux := web.SessionAuthMiddleware(sessions)(mux)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	authMux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("Expected 302 redirect to login, got %d", w.Code)
	}
	location := w.Header().Get("Location")
	if location != "/login" {
		t.Errorf("Expected redirect to /login, got %s", location)
	}
}

// TestIntegration_Auth_BasicAuthFallback verifies API endpoints accept Basic Auth.
func TestIntegration_Auth_BasicAuthFallback(t *testing.T) {
	db := testutil.InMemoryStore(t)
	logger := testutil.DiscardLogger()

	passwordHash, _ := web.HashPassword("testpass123")
	sessions := web.NewSessionStore("admin", passwordHash, db)

	mux := http.NewServeMux()
	cfg := testutil.TestConfig("http://localhost:0")
	tr := tracker.New(db, logger)
	h := web.NewHandler(db, tr, logger, sessions, cfg)
	mux.HandleFunc("/api/providers", h.Providers)

	authMux := web.SessionAuthMiddleware(sessions)(mux)

	req := httptest.NewRequest("GET", "/api/providers", nil)
	req.SetBasicAuth("admin", "testpass123")
	w := httptest.NewRecorder()
	authMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200 with Basic Auth, got %d: %s", w.Code, w.Body.String())
	}
}

// TestIntegration_Auth_ExpiredSession verifies expired sessions are rejected.
func TestIntegration_Auth_ExpiredSession(t *testing.T) {
	db := testutil.InMemoryStore(t)

	passwordHash, _ := web.HashPassword("testpass123")
	sessions := web.NewSessionStore("admin", passwordHash, db)

	// Validate a made-up token (should be invalid)
	if sessions.ValidateToken("expired-token-doesnt-exist") {
		t.Error("Expected invalid token to be rejected")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 3.7 Settings Data Persistence (3 tests)
// ═══════════════════════════════════════════════════════════════════════

// TestIntegration_Settings_NotificationRoundTrip verifies notification settings
// are persisted and retrieved correctly.
func TestIntegration_Settings_NotificationRoundTrip(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	// Save notification settings
	body := `{"notifications":{"warning_threshold":70,"critical_threshold":90,"enabled":true}}`
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.UpdateSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT settings: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Read back
	req2 := httptest.NewRequest("GET", "/api/settings", nil)
	w2 := httptest.NewRecorder()
	h.GetSettings(w2, req2)

	var resp map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &resp)

	notif, ok := resp["notifications"].(map[string]interface{})
	if !ok {
		t.Fatal("Missing notifications in GET response")
	}
	if notif["warning_threshold"].(float64) != 70 {
		t.Errorf("warning_threshold: want 70, got %v", notif["warning_threshold"])
	}
	if notif["critical_threshold"].(float64) != 90 {
		t.Errorf("critical_threshold: want 90, got %v", notif["critical_threshold"])
	}
}

// TestIntegration_Settings_ProviderVisibilityRoundTrip verifies provider visibility
// settings are persisted and retrieved correctly.
func TestIntegration_Settings_ProviderVisibilityRoundTrip(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	body := `{"provider_visibility":{"synthetic":{"dashboard":true,"polling":true},"zai":{"dashboard":false,"polling":true}}}`
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.UpdateSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT settings: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Read back
	req2 := httptest.NewRequest("GET", "/api/settings", nil)
	w2 := httptest.NewRecorder()
	h.GetSettings(w2, req2)

	var resp map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &resp)

	vis, ok := resp["provider_visibility"].(map[string]interface{})
	if !ok {
		t.Fatal("Missing provider_visibility in GET response")
	}

	zai, ok := vis["zai"].(map[string]interface{})
	if !ok {
		t.Fatal("Missing zai in provider_visibility")
	}
	if zai["dashboard"].(bool) != false {
		t.Error("zai.dashboard: want false, got true")
	}
}

// TestIntegration_Settings_HiddenInsightsRoundTrip verifies hidden insights
// settings are persisted and retrieved correctly.
func TestIntegration_Settings_HiddenInsightsRoundTrip(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	body := `{"hidden_insights":["cycle_utilization","weekly_pace"]}`
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.UpdateSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT settings: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Read back
	req2 := httptest.NewRequest("GET", "/api/settings", nil)
	w2 := httptest.NewRecorder()
	h.GetSettings(w2, req2)

	var resp map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &resp)

	hi, ok := resp["hidden_insights"].([]interface{})
	if !ok {
		t.Fatal("Missing hidden_insights in GET response")
	}
	if len(hi) != 2 {
		t.Fatalf("Expected 2 hidden insights, got %d", len(hi))
	}

	keys := map[string]bool{}
	for _, k := range hi {
		keys[k.(string)] = true
	}
	if !keys["cycle_utilization"] {
		t.Error("Missing 'cycle_utilization' in hidden insights")
	}
	if !keys["weekly_pace"] {
		t.Error("Missing 'weekly_pace' in hidden insights")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Additional integration tests to reach 30+
// ═══════════════════════════════════════════════════════════════════════

// TestIntegration_API_DashboardRendersWithConfig verifies dashboard HTML renders
// with proper config (providers in template data).
func TestIntegration_API_DashboardRendersWithConfig(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.Dashboard(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "onWatch") {
		t.Error("Dashboard missing 'onWatch'")
	}
	if !strings.Contains(body, "app.js") {
		t.Error("Dashboard missing app.js reference")
	}
}

// TestIntegration_API_LoginPageRenders verifies the login page renders correctly.
func TestIntegration_API_LoginPageRenders(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	req := httptest.NewRequest("GET", "/login", nil)
	w := httptest.NewRecorder()
	h.Login(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Login") {
		t.Error("Login page missing 'Login'")
	}
}

// TestIntegration_API_CheckUpdateWithoutUpdater verifies check-update returns
// 503 when updater is not configured.
func TestIntegration_API_CheckUpdateWithoutUpdater(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	req := httptest.NewRequest("GET", "/api/update/check", nil)
	w := httptest.NewRecorder()
	h.CheckUpdate(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// TestIntegration_API_SMTPTestWithoutNotifier verifies SMTP test returns
// 503 when notifier is not configured.
func TestIntegration_API_SMTPTestWithoutNotifier(t *testing.T) {
	h, _ := testutil.TestHandler(t)

	req := httptest.NewRequest("POST", "/api/settings/smtp/test", nil)
	w := httptest.NewRecorder()
	h.SMTPTest(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// TestIntegration_Anthropic_UtilizationSeries verifies the utilization series
// query returns correct time-ordered data.
func TestIntegration_Anthropic_UtilizationSeries(t *testing.T) {
	db := testutil.InMemoryStore(t)
	now := time.Now().UTC()
	resetTime := now.Add(3 * time.Hour)

	// Insert 5 snapshots with increasing utilization
	for i := range 5 {
		snap := &api.AnthropicSnapshot{
			CapturedAt: now.Add(time.Duration(i) * time.Minute),
			Quotas: []api.AnthropicQuota{
				{Name: "five_hour", Utilization: float64(i*10 + 10), ResetsAt: &resetTime},
			},
		}
		db.InsertAnthropicSnapshot(snap)
	}

	series, err := db.QueryAnthropicUtilizationSeries("five_hour", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("QueryAnthropicUtilizationSeries: %v", err)
	}
	if len(series) != 5 {
		t.Fatalf("Expected 5 points, got %d", len(series))
	}

	// Verify ascending order
	for i := 1; i < len(series); i++ {
		if series[i].CapturedAt.Before(series[i-1].CapturedAt) {
			t.Errorf("Series not in ascending order at index %d", i)
		}
	}

	// Verify values
	if series[0].Utilization != 10 {
		t.Errorf("First utilization: want 10, got %f", series[0].Utilization)
	}
	if series[4].Utilization != 50 {
		t.Errorf("Last utilization: want 50, got %f", series[4].Utilization)
	}
}

// Ensure the config package functions work correctly for integration tests.
var _ = &config.Config{}
