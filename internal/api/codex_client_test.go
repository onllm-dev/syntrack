package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func discardLoggerClient() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestCodexClient_FetchUsage_Success(t *testing.T) {
	var gotAuth atomic.Value
	var gotUA atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotUA.Store(r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":22.5,"reset_at":1766000000,"limit_window_seconds":18000}}}`)
	}))
	defer server.Close()

	client := NewCodexClient("oauth_token", discardLoggerClient(), WithCodexBaseURL(server.URL))
	resp, err := client.FetchUsage(context.Background())
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.PlanType != "pro" {
		t.Fatalf("PlanType = %q, want pro", resp.PlanType)
	}

	auth, _ := gotAuth.Load().(string)
	if auth != "Bearer oauth_token" {
		t.Fatalf("Authorization = %q, want Bearer oauth_token", auth)
	}
	ua, _ := gotUA.Load().(string)
	if ua != "onwatch/1.0" {
		t.Fatalf("User-Agent = %q, want onwatch/1.0", ua)
	}
}

func TestCodexClient_FetchUsage_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewCodexClient("bad", discardLoggerClient(), WithCodexBaseURL(server.URL))
	_, err := client.FetchUsage(context.Background())
	if !errors.Is(err, ErrCodexUnauthorized) {
		t.Fatalf("expected ErrCodexUnauthorized, got %v", err)
	}
}

func TestCodexClient_FetchUsage_Forbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	client := NewCodexClient("bad", discardLoggerClient(), WithCodexBaseURL(server.URL))
	_, err := client.FetchUsage(context.Background())
	if !errors.Is(err, ErrCodexForbidden) {
		t.Fatalf("expected ErrCodexForbidden, got %v", err)
	}
}

func TestCodexClient_FetchUsage_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewCodexClient("token", discardLoggerClient(), WithCodexBaseURL(server.URL))
	_, err := client.FetchUsage(context.Background())
	if !errors.Is(err, ErrCodexServerError) {
		t.Fatalf("expected ErrCodexServerError, got %v", err)
	}
}

func TestCodexClient_FetchUsage_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{invalid`)
	}))
	defer server.Close()

	client := NewCodexClient("token", discardLoggerClient(), WithCodexBaseURL(server.URL))
	_, err := client.FetchUsage(context.Background())
	if !errors.Is(err, ErrCodexInvalidResponse) {
		t.Fatalf("expected ErrCodexInvalidResponse, got %v", err)
	}
}

func TestCodexClient_FetchUsage_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewCodexClient("token", discardLoggerClient(), WithCodexBaseURL(server.URL), WithCodexTimeout(4*time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.FetchUsage(ctx)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestCodexClient_FetchUsage_FallbacksToWhamOnCodexPath404(t *testing.T) {
	var gotPath atomic.Value
	var gotChatClaudeAccount atomic.Value
	var gotXAccount atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		gotChatClaudeAccount.Store(r.Header.Get("ChatClaude-Account-Id"))
		gotXAccount.Store(r.Header.Get("X-Account-Id"))
		switch r.URL.Path {
		case "/api/codex/usage":
			w.WriteHeader(http.StatusNotFound)
		case "/backend-api/wham/usage":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":33.3,"reset_at":1766000000,"limit_window_seconds":18000}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewCodexClient("oauth_token", discardLoggerClient(), WithCodexBaseURL(server.URL+"/api/codex/usage"))
	client.SetAccountID("acct_123")

	resp, err := client.FetchUsage(context.Background())
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.PlanType != "pro" {
		t.Fatalf("PlanType = %q, want pro", resp.PlanType)
	}

	path, _ := gotPath.Load().(string)
	if path != "/backend-api/wham/usage" {
		t.Fatalf("last request path = %q, want /backend-api/wham/usage", path)
	}
	chatClaudeAccount, _ := gotChatClaudeAccount.Load().(string)
	if chatClaudeAccount != "acct_123" {
		t.Fatalf("ChatClaude-Account-Id = %q, want acct_123", chatClaudeAccount)
	}
	xAccount, _ := gotXAccount.Load().(string)
	if xAccount != "acct_123" {
		t.Fatalf("X-Account-Id = %q, want acct_123", xAccount)
	}
}

func TestCodexClient_FetchUsage_Sequential404FallbackSwitchesBaseURL(t *testing.T) {
	var codexCalls atomic.Int32
	var whamCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/codex/usage":
			codexCalls.Add(1)
			w.WriteHeader(http.StatusNotFound)
		case "/backend-api/wham/usage":
			whamCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":11.1,"reset_at":1766000000,"limit_window_seconds":18000}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewCodexClient("oauth_token", discardLoggerClient(), WithCodexBaseURL(server.URL+"/api/codex/usage"))

	for i := 0; i < 3; i++ {
		if _, err := client.FetchUsage(context.Background()); err != nil {
			t.Fatalf("FetchUsage[%d]: %v", i, err)
		}
	}

	if codexCalls.Load() != 1 {
		t.Fatalf("/api/codex/usage calls = %d, want 1", codexCalls.Load())
	}
	if whamCalls.Load() != 3 {
		t.Fatalf("/backend-api/wham/usage calls = %d, want 3", whamCalls.Load())
	}
}

func TestCodexClient_FetchUsage_FallbacksToCodexPathOnWham404(t *testing.T) {
	var gotPath atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			w.WriteHeader(http.StatusNotFound)
		case "/api/codex/usage":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":44.4,"reset_at":1766000000,"limit_window_seconds":18000}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewCodexClient("oauth_token", discardLoggerClient(), WithCodexBaseURL(server.URL+"/backend-api/wham/usage"))

	resp, err := client.FetchUsage(context.Background())
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if resp == nil {
		t.Fatal("response is nil")
	}

	path, _ := gotPath.Load().(string)
	if path != "/api/codex/usage" {
		t.Fatalf("last request path = %q, want /api/codex/usage", path)
	}
}

func TestCodexClient_DefaultBaseURL_UsesChatGPTWhamUsage(t *testing.T) {
	client := NewCodexClient("token", discardLoggerClient())
	if client.baseURL != "https://chatgpt.com/backend-api/wham/usage" {
		t.Fatalf("baseURL = %q, want https://chatgpt.com/backend-api/wham/usage", client.baseURL)
	}
}

func TestCodexClient_DefaultTimeoutBudgetIsTenSeconds(t *testing.T) {
	client := NewCodexClient("token", discardLoggerClient())
	if client.httpClient.Timeout != 10*time.Second {
		t.Fatalf("http client timeout = %v, want 10s", client.httpClient.Timeout)
	}
}

func TestCodexClient_FetchUsage_UsesTenSecondRequestBudget(t *testing.T) {
	deadlineCh := make(chan time.Duration, 1)

	client := NewCodexClient("token", discardLoggerClient(), WithCodexBaseURL("https://example.invalid"))
	client.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			deadlineCh <- -1
		} else {
			deadlineCh <- time.Until(deadline)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"plan_type":"pro"}`)),
			Header:     make(http.Header),
		}, nil
	})

	if _, err := client.FetchUsage(context.Background()); err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}

	remaining := <-deadlineCh
	if remaining < 9*time.Second || remaining > 10*time.Second {
		t.Fatalf("request budget = %v, want about 10s", remaining)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
