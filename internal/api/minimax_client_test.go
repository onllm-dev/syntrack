package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewMiniMaxClient(t *testing.T) {
	logger := slog.Default()
	client := NewMiniMaxClient("minimax_test123", logger)
	if client == nil {
		t.Fatal("NewMiniMaxClient returned nil")
	}
	if client.baseURL != "https://www.minimax.io/v1/api/openplatform/coding_plan/remains" {
		t.Errorf("baseURL = %q, want default", client.baseURL)
	}
}

func TestNewMiniMaxClient_WithOptions(t *testing.T) {
	logger := slog.Default()
	client := NewMiniMaxClient("minimax_test123", logger,
		WithMiniMaxBaseURL("http://localhost:1234"),
		WithMiniMaxTimeout(5*time.Second),
	)
	if client.baseURL != "http://localhost:1234" {
		t.Errorf("baseURL = %q, want custom", client.baseURL)
	}
}

func TestMiniMaxClient_FetchRemains_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer minimax_testtoken" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type header = %q, want application/json", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept header = %q, want application/json", r.Header.Get("Accept"))
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"base_resp": {"status_code": 0, "status_msg": ""},
			"model_remains": [
				{
					"model_name": "MiniMax-M2",
					"start_time": "2026-02-15T11:00:00Z",
					"end_time": "2026-02-15T13:00:00Z",
					"remains_time": 7200000,
					"current_interval_total_count": 200,
					"current_interval_usage_count": 42
				}
			]
		}`)
	}))
	defer server.Close()

	logger := slog.Default()
	client := NewMiniMaxClient("minimax_testtoken", logger, WithMiniMaxBaseURL(server.URL))

	resp, err := client.FetchRemains(context.Background())
	if err != nil {
		t.Fatalf("FetchRemains: %v", err)
	}

	if resp.BaseResp.StatusCode != 0 {
		t.Errorf("StatusCode = %d, want 0", resp.BaseResp.StatusCode)
	}
	if len(resp.ModelRemains) != 1 {
		t.Fatalf("ModelRemains len = %d, want 1", len(resp.ModelRemains))
	}
	if resp.ModelRemains[0].ModelName != "MiniMax-M2" {
		t.Errorf("ModelName = %q, want %q", resp.ModelRemains[0].ModelName, "MiniMax-M2")
	}
}

func TestMiniMaxClient_FetchRemains_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"base_resp":{"status_code":1004,"status_msg":"unauthorized"}}`)
	}))
	defer server.Close()

	client := NewMiniMaxClient("bad_token", slog.Default(), WithMiniMaxBaseURL(server.URL))
	_, err := client.FetchRemains(context.Background())
	if !errors.Is(err, ErrMiniMaxUnauthorized) {
		t.Errorf("Expected ErrMiniMaxUnauthorized, got %v", err)
	}
}

func TestMiniMaxClient_FetchRemains_UnauthorizedInBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"base_resp":{"status_code":1004,"status_msg":"unauthorized"},"model_remains":[]}`)
	}))
	defer server.Close()

	client := NewMiniMaxClient("bad_token", slog.Default(), WithMiniMaxBaseURL(server.URL))
	_, err := client.FetchRemains(context.Background())
	if !errors.Is(err, ErrMiniMaxUnauthorized) {
		t.Errorf("Expected ErrMiniMaxUnauthorized from body status_code, got %v", err)
	}
}

func TestMiniMaxClient_FetchRemains_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewMiniMaxClient("minimax_test", slog.Default(), WithMiniMaxBaseURL(server.URL))
	_, err := client.FetchRemains(context.Background())
	if !errors.Is(err, ErrMiniMaxServerError) {
		t.Errorf("Expected ErrMiniMaxServerError, got %v", err)
	}
}

func TestMiniMaxClient_FetchRemains_BadGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client := NewMiniMaxClient("minimax_test", slog.Default(), WithMiniMaxBaseURL(server.URL))
	_, err := client.FetchRemains(context.Background())
	if !errors.Is(err, ErrMiniMaxServerError) {
		t.Errorf("Expected ErrMiniMaxServerError, got %v", err)
	}
}

func TestMiniMaxClient_FetchRemains_EmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewMiniMaxClient("minimax_test", slog.Default(), WithMiniMaxBaseURL(server.URL))
	_, err := client.FetchRemains(context.Background())
	if !errors.Is(err, ErrMiniMaxInvalidResponse) {
		t.Errorf("Expected ErrMiniMaxInvalidResponse, got %v", err)
	}
}

func TestMiniMaxClient_FetchRemains_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{invalid json`)
	}))
	defer server.Close()

	client := NewMiniMaxClient("minimax_test", slog.Default(), WithMiniMaxBaseURL(server.URL))
	_, err := client.FetchRemains(context.Background())
	if !errors.Is(err, ErrMiniMaxInvalidResponse) {
		t.Errorf("Expected ErrMiniMaxInvalidResponse, got %v", err)
	}
}

func TestMiniMaxClient_FetchRemains_NetworkError(t *testing.T) {
	client := NewMiniMaxClient("minimax_test", slog.Default(),
		WithMiniMaxBaseURL("http://127.0.0.1:1"),
		WithMiniMaxTimeout(1*time.Second),
	)
	_, err := client.FetchRemains(context.Background())
	if !errors.Is(err, ErrMiniMaxNetworkError) {
		t.Errorf("Expected ErrMiniMaxNetworkError, got %v", err)
	}
}

func TestMiniMaxClient_FetchRemains_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer server.Close()

	client := NewMiniMaxClient("minimax_test", slog.Default(), WithMiniMaxBaseURL(server.URL))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.FetchRemains(ctx)
	if err == nil {
		t.Fatal("Expected error for cancelled context")
	}
}

func TestMiniMaxClient_FetchRemains_UnexpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()

	client := NewMiniMaxClient("minimax_test", slog.Default(), WithMiniMaxBaseURL(server.URL))
	_, err := client.FetchRemains(context.Background())
	if err == nil {
		t.Fatal("Expected error for unexpected status code")
	}
	if errors.Is(err, ErrMiniMaxUnauthorized) || errors.Is(err, ErrMiniMaxServerError) {
		t.Errorf("Should not be a sentinel error, got: %v", err)
	}
}

func TestRedactMiniMaxToken(t *testing.T) {
	tests := []struct {
		token    string
		expected string
	}{
		{"", "(empty)"},
		{"short", "***...***"},
		{"minimax_abcdefghijklmnop", "mini***...***nop"},
	}

	for _, tt := range tests {
		got := redactMiniMaxToken(tt.token)
		if got != tt.expected {
			t.Errorf("redactMiniMaxToken(%q) = %q, want %q", tt.token, got, tt.expected)
		}
	}
}
