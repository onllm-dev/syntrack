package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

func miniMaxTestResponse() api.MiniMaxRemainsResponse {
	return api.MiniMaxRemainsResponse{
		BaseResp: api.MiniMaxBaseResp{StatusCode: 0, StatusMsg: ""},
		ModelRemains: []api.MiniMaxModelRemain{
			{
				ModelName:                 "MiniMax-M2",
				StartTime:                 "2026-02-15T11:00:00Z",
				EndTime:                   "2026-02-15T13:00:00Z",
				RemainsTime:               7200000,
				CurrentIntervalTotalCount: 200,
				CurrentIntervalUsageCount: 42,
			},
			{
				ModelName:                 "MiniMax-Text-01",
				StartTime:                 "2026-02-15T11:00:00Z",
				EndTime:                   "2026-02-15T13:00:00Z",
				RemainsTime:               7200000,
				CurrentIntervalTotalCount: 100,
				CurrentIntervalUsageCount: 20,
			},
		},
	}
}

func setupMiniMaxTest(t *testing.T) (*MiniMaxAgent, *store.Store, *httptest.Server) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		if r.Header.Get("Authorization") != "Bearer minimax_test_token" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"base_resp":{"status_code":1004,"status_msg":"unauthorized"}}`)
			return
		}
		resp := miniMaxTestResponse()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	t.Cleanup(func() { str.Close() })

	logger := slog.Default()
	client := api.NewMiniMaxClient("minimax_test_token", logger, api.WithMiniMaxBaseURL(server.URL))
	tr := tracker.NewMiniMaxTracker(str, logger)
	sm := NewSessionManager(str, "minimax", 600*time.Second, logger)

	ag := NewMiniMaxAgent(client, str, tr, 100*time.Millisecond, logger, sm)

	return ag, str, server
}

func TestMiniMaxAgent_SinglePoll(t *testing.T) {
	ag, str, _ := setupMiniMaxTest(t)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go ag.Run(ctx)

	time.Sleep(250 * time.Millisecond)
	cancel()

	latest, err := str.QueryLatestMiniMax()
	if err != nil {
		t.Fatalf("QueryLatestMiniMax: %v", err)
	}
	if latest == nil {
		t.Fatal("Expected snapshot after poll")
	}
	if len(latest.Models) < 2 {
		t.Errorf("Expected at least 2 models, got %d", len(latest.Models))
	}
}

func TestMiniMaxAgent_PollingCheck(t *testing.T) {
	ag, str, _ := setupMiniMaxTest(t)

	ag.SetPollingCheck(func() bool { return false })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	go ag.Run(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()

	latest, err := str.QueryLatestMiniMax()
	if err != nil {
		t.Fatalf("QueryLatestMiniMax: %v", err)
	}
	if latest != nil {
		t.Error("Expected no snapshot when polling disabled")
	}
}

func TestMiniMaxAgent_ContextCancellation(t *testing.T) {
	ag, _, _ := setupMiniMaxTest(t)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- ag.Run(ctx)
	}()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Expected nil error on cancel, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Agent did not stop within timeout")
	}
}
