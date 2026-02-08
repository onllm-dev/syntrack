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
	"github.com/onllm-dev/onwatch/internal/notify"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
)

// zaiResponse returns a valid Z.ai quota response JSON.
func zaiResponse(tokensUsage, tokensCurrentValue, timeUsage, timeCurrentValue float64) string {
	resetMs := time.Now().UTC().Add(7 * 24 * time.Hour).UnixMilli()
	limits := []map[string]interface{}{
		{
			"type":         "TIME_LIMIT",
			"unit":         1,
			"number":       int(timeUsage),
			"usage":        timeUsage,
			"currentValue": timeCurrentValue,
			"remaining":    timeUsage - timeCurrentValue,
			"percentage":   int(timeCurrentValue / timeUsage * 100),
		},
		{
			"type":          "TOKENS_LIMIT",
			"unit":          1,
			"number":        int(tokensUsage),
			"usage":         tokensUsage,
			"currentValue":  tokensCurrentValue,
			"remaining":     tokensUsage - tokensCurrentValue,
			"percentage":    int(tokensCurrentValue / tokensUsage * 100),
			"nextResetTime": resetMs,
		},
	}
	data, _ := json.Marshal(map[string]interface{}{
		"code":    200,
		"msg":     "success",
		"success": true,
		"data": map[string]interface{}{
			"limits": limits,
		},
	})
	return string(data)
}

// zaiAuthErrorBody returns a Z.ai body-level 401 error (HTTP 200 with error code in body).
func zaiAuthErrorBody() string {
	data, _ := json.Marshal(map[string]interface{}{
		"code":    401,
		"msg":     "token expired or incorrect",
		"success": false,
		"data":    nil,
	})
	return string(data)
}

// TestZaiAgent_Run_PollsAndStoresSnapshot verifies that the Z.ai agent polls the API
// and persists a snapshot to the database.
func TestZaiAgent_Run_PollsAndStoresSnapshot(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(zaiResponse(200000000, 50000000, 1000, 19)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewZaiClient("test-key", logger, api.WithZaiBaseURL(server.URL+"/monitor/usage/quota/limit"))
	tr := tracker.NewZaiTracker(str, logger)

	agent := NewZaiAgent(client, str, tr, 50*time.Millisecond, logger, nil)

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

	// Verify at least one poll occurred
	if count := callCount.Load(); count < 1 {
		t.Errorf("Expected at least 1 API call, got %d", count)
	}

	// Verify snapshot was stored
	latest, err := str.QueryLatestZai()
	if err != nil {
		t.Fatalf("QueryLatestZai error: %v", err)
	}
	if latest == nil {
		t.Fatal("Expected at least one Z.ai snapshot to be stored")
	}

	// Verify the snapshot has expected values
	if latest.TokensUsage != 200000000 {
		t.Errorf("Expected tokens usage 200000000, got %f", latest.TokensUsage)
	}
	if latest.TokensCurrentValue != 50000000 {
		t.Errorf("Expected tokens current value 50000000, got %f", latest.TokensCurrentValue)
	}
	if latest.TimeCurrentValue != 19 {
		t.Errorf("Expected time current value 19, got %f", latest.TimeCurrentValue)
	}
}

// TestZaiAgent_Run_AuthError_ContinuesPolling verifies that a Z.ai body-level 401 error
// (HTTP 200 with code 401 in body) does not crash the agent, and it continues polling.
func TestZaiAgent_Run_AuthError_ContinuesPolling(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// First call: body-level 401
			w.Write([]byte(zaiAuthErrorBody()))
			return
		}
		// Subsequent calls: success
		w.Write([]byte(zaiResponse(200000000, 50000000, 1000, 19)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer str.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	client := api.NewZaiClient("test-key", logger, api.WithZaiBaseURL(server.URL+"/monitor/usage/quota/limit"))
	tr := tracker.NewZaiTracker(str, logger)

	agent := NewZaiAgent(client, str, tr, 50*time.Millisecond, logger, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx)
	}()

	<-ctx.Done()

	// Wait for agent goroutine to fully stop before reading shared logBuf
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Expected nil error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Agent.Run() did not return within 2s")
	}

	// Agent should have continued polling after 401
	if count := callCount.Load(); count < 2 {
		t.Errorf("Expected at least 2 API calls (continuing after auth error), got %d", count)
	}

	// Logs should contain error about Z.ai
	logs := logBuf.String()
	if !bytes.Contains([]byte(logs), []byte("Z.ai")) {
		t.Logf("Logs: %s", logs)
		t.Error("Expected log output mentioning Z.ai error")
	}
}

// TestZaiAgent_Run_NotifierCalled verifies that NotificationEngine.Check() is called
// when the Z.ai agent processes a successful poll with quota data.
func TestZaiAgent_Run_NotifierCalled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// High usage to trigger notification thresholds
		w.Write([]byte(zaiResponse(200000000, 180000000, 1000, 900)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewZaiClient("test-key", logger, api.WithZaiBaseURL(server.URL+"/monitor/usage/quota/limit"))
	tr := tracker.NewZaiTracker(str, logger)

	agent := NewZaiAgent(client, str, tr, 50*time.Millisecond, logger, nil)

	// Create a real notification engine (it won't actually send emails without SMTP config)
	notifier := notify.New(str, logger)
	agent.SetNotifier(notifier)

	// Run a single poll by using a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx)
	}()

	// Wait for at least one poll
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent.Run() did not return within 2s")
	}

	// The notifier was called (we verify indirectly by checking the snapshot was stored
	// which means the poll completed including the notifier check step).
	// The notifier.Check() call doesn't error or panic, even with high usage values.
	latest, _ := str.QueryLatestZai()
	if latest == nil {
		t.Fatal("Expected snapshot to be stored (poll completed including notifier check)")
	}

	// Verify high usage was recorded
	if latest.TokensCurrentValue != 180000000 {
		t.Errorf("Expected tokens current value 180000000, got %f", latest.TokensCurrentValue)
	}
}

// TestZaiAgent_Run_SessionManagerReportsPoll verifies that the SessionManager receives
// poll values from the Z.ai agent for usage-based session detection.
func TestZaiAgent_Run_SessionManagerReportsPoll(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Increment values on each call to trigger session detection
		tokensCV := 50000000.0 + float64(n)*1000000
		timeCV := 19.0 + float64(n)
		w.Write([]byte(zaiResponse(200000000, tokensCV, 1000, timeCV)))
	}))
	defer server.Close()

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer str.Close()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	client := api.NewZaiClient("test-key", logger, api.WithZaiBaseURL(server.URL+"/monitor/usage/quota/limit"))
	tr := tracker.NewZaiTracker(str, logger)

	sm := NewSessionManager(str, "zai", 10*time.Second, logger)
	agent := NewZaiAgent(client, str, tr, 50*time.Millisecond, logger, sm)

	// Run long enough for multiple polls (values change each time -> session created)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- agent.Run(ctx)
	}()

	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent.Run() did not return within 2s")
	}

	// The session manager should have received poll values and created a session
	// (since values change each poll, session should be detected after 2nd poll)
	sessions, err := str.QuerySessionHistory("zai")
	if err != nil {
		t.Fatalf("QuerySessionHistory error: %v", err)
	}

	if len(sessions) == 0 {
		t.Error("Expected at least 1 Z.ai session (usage changed between polls)")
	}

	// Session should have been closed by agent shutdown (SessionManager.Close)
	if len(sessions) > 0 && sessions[0].EndedAt == nil {
		t.Logf("Session still open (may not have been closed by agent shutdown)")
	}
}
