package agent

import (
	"context"
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

func setupCodexTest(t *testing.T) (*CodexAgent, *store.Store, *httptest.Server) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		if r.Header.Get("Authorization") != "Bearer oauth_token" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"unauthorized"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":25,"reset_at":1766000000,"limit_window_seconds":18000}}}`)
	}))
	t.Cleanup(server.Close)

	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	logger := slog.Default()
	client := api.NewCodexClient("oauth_token", logger, api.WithCodexBaseURL(server.URL))
	tr := tracker.NewCodexTracker(st, logger)
	sm := NewSessionManager(st, "codex", 600*time.Second, logger)
	ag := NewCodexAgent(client, st, tr, 100*time.Millisecond, logger, sm)
	return ag, st, server
}

func TestCodexAgent_SinglePoll(t *testing.T) {
	ag, st, _ := setupCodexTest(t)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go ag.Run(ctx)
	time.Sleep(250 * time.Millisecond)
	cancel()

	latest, err := st.QueryLatestCodex()
	if err != nil {
		t.Fatalf("QueryLatestCodex: %v", err)
	}
	if latest == nil {
		t.Fatal("expected snapshot after poll")
	}
	if latest.PlanType != "pro" {
		t.Fatalf("PlanType = %q, want pro", latest.PlanType)
	}
	if len(latest.Quotas) == 0 {
		t.Fatal("expected at least one quota")
	}
}

func TestCodexAgent_PollingCheck(t *testing.T) {
	ag, st, _ := setupCodexTest(t)
	ag.SetPollingCheck(func() bool { return false })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	go ag.Run(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()

	latest, err := st.QueryLatestCodex()
	if err != nil {
		t.Fatalf("QueryLatestCodex: %v", err)
	}
	if latest != nil {
		t.Fatal("expected no snapshot when polling disabled")
	}
}

func TestCodexAgent_ContextCancellation(t *testing.T) {
	ag, _, _ := setupCodexTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ag.Run(ctx)
	}()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not stop within timeout")
	}
}

func TestCodexAgent_AuthFailuresPauseUntilTokenChanges(t *testing.T) {
	var currentToken atomic.Value
	currentToken.Store("bad")

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":25,"reset_at":1766000000,"limit_window_seconds":18000}}}`)
	}))
	defer server.Close()

	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer st.Close()

	logger := slog.Default()
	client := api.NewCodexClient("bad", logger, api.WithCodexBaseURL(server.URL))
	tr := tracker.NewCodexTracker(st, logger)
	sm := NewSessionManager(st, "codex", 600*time.Second, logger)
	ag := NewCodexAgent(client, st, tr, 50*time.Millisecond, logger, sm)
	ag.SetTokenRefresh(func() string {
		v, _ := currentToken.Load().(string)
		return v
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ag.Run(ctx)

	// Wait for agent to hit max auth failures and pause.
	deadline := time.After(2 * time.Second)
	for {
		if calls.Load() >= 6 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("expected codex agent to hit repeated auth failures")
		case <-time.After(20 * time.Millisecond):
		}
	}

	pausedCalls := calls.Load()
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != pausedCalls {
		t.Fatalf("expected no fetch calls while paused, got %d -> %d", pausedCalls, calls.Load())
	}

	// Change token and ensure polling resumes.
	currentToken.Store("good")
	deadline = time.After(2 * time.Second)
	for {
		if calls.Load() > pausedCalls {
			break
		}
		select {
		case <-deadline:
			t.Fatal("expected codex polling to resume after token change")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
