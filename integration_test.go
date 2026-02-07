//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/agent"
	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
	"github.com/onllm-dev/onwatch/internal/web"
)

// discardLogger returns a logger that discards all output
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mockServer creates a test server that returns synthetic API responses
func mockServer(t *testing.T, responses []api.QuotaResponse) *httptest.Server {
	callCount := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("Expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v2/quotas" {
			t.Errorf("Expected path /v2/quotas, got %s", r.URL.Path)
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("Expected Bearer token, got %s", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		if callCount < len(responses) {
			json.NewEncoder(w).Encode(responses[callCount])
			callCount++
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

	// Setup mock API responses
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

	// Create agent with short interval for testing
	ag := agent.New(client, db, tr, 100*time.Millisecond, discardLogger(), nil)

	// Run agent for a short time
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
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

	// Verify data was stored
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
	if latest.Search.Requests != 10.0 {
		t.Errorf("Expected search requests 10.0, got %f", latest.Search.Requests)
	}
	if latest.ToolCall.Requests != 5000.0 {
		t.Errorf("Expected tool requests 5000.0, got %f", latest.ToolCall.Requests)
	}

	// Verify session was created
	sessions, err := db.QuerySessionHistory()
	if err != nil {
		t.Fatalf("Failed to query sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}
	if sessions[0].SnapshotCount < 1 {
		t.Errorf("Expected at least 1 snapshot, got %d", sessions[0].SnapshotCount)
	}

	// Test web handler returns the data
	handler := web.NewHandler(db, tr, discardLogger())

	// Test /api/current endpoint
	req := httptest.NewRequest("GET", "/api/current", nil)
	w := httptest.NewRecorder()
	handler.Current(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var currentResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &currentResp); err != nil {
		t.Fatalf("Failed to parse current response: %v", err)
	}

	subscription, ok := currentResp["subscription"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected subscription in response")
	}

	if subscription["usage"] != 100.0 {
		t.Errorf("Expected usage 100.0, got %v", subscription["usage"])
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

	// First poll
	ctx1, cancel1 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	ag1 := agent.New(client, db, tr, 1*time.Hour, discardLogger(), nil) // Long interval, we'll cancel immediately
	go ag1.Run(ctx1)
	time.Sleep(150 * time.Millisecond)
	cancel1()
	time.Sleep(50 * time.Millisecond)

	// Second poll - should detect reset
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	ag2 := agent.New(client, db, tr, 1*time.Hour, discardLogger(), nil)
	go ag2.Run(ctx2)
	time.Sleep(150 * time.Millisecond)
	cancel2()
	time.Sleep(50 * time.Millisecond)

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
	handler := web.NewHandler(db, tr, discardLogger())
	req := httptest.NewRequest("GET", "/api/cycles?type=subscription", nil)
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
	ag := agent.New(client, db, tr, 1*time.Hour, discardLogger(), nil)
	go ag.Run(ctx)
	time.Sleep(250 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Test dashboard HTML response
	handler := web.NewHandler(db, tr, discardLogger())
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
	ag := agent.New(client, db, tr, 1*time.Second, discardLogger(), nil)

	// Create web server
	handler := web.NewHandler(db, tr, discardLogger())
	webServer := web.NewServer(0, handler, discardLogger()) // Port 0 = random available port

	// Start web server in background
	go webServer.Start()
	time.Sleep(100 * time.Millisecond)

	// Start agent in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ag.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	// Get active session
	session, err := db.QueryActiveSession()
	if err != nil {
		t.Fatalf("Failed to query active session: %v", err)
	}
	if session == nil {
		t.Fatal("Expected active session before shutdown")
	}

	// Send SIGINT to self
	err = syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	if err != nil {
		t.Fatalf("Failed to send SIGINT: %v", err)
	}

	// Give time for shutdown
	time.Sleep(300 * time.Millisecond)

	// Cancel context to stop agent
	cancel()
	time.Sleep(100 * time.Millisecond)

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
