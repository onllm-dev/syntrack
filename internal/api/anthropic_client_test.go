package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/testutil"
)

func TestAnthropicClient_FetchQuotas_Success(t *testing.T) {
	ms := testutil.NewMockServer(t, testutil.WithAnthropicToken("test_token"))
	defer ms.Close()

	client := api.NewAnthropicClient("test_token", testutil.DiscardLogger(),
		api.WithAnthropicBaseURL(ms.URL+"/api/oauth/usage"),
	)

	resp, err := client.FetchQuotas(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("response should not be nil")
	}

	// The default Anthropic response should have five_hour, seven_day, seven_day_sonnet as active quotas
	names := resp.ActiveQuotaNames()
	if len(names) < 2 {
		t.Errorf("expected at least 2 active quotas, got %d: %v", len(names), names)
	}

	// Verify five_hour exists and has reasonable utilization
	entry, ok := (*resp)["five_hour"]
	if !ok {
		t.Fatal("missing five_hour quota in response")
	}
	if entry == nil || entry.Utilization == nil {
		t.Fatal("five_hour entry or utilization is nil")
	}
	if *entry.Utilization < 0 || *entry.Utilization > 100 {
		t.Errorf("five_hour utilization out of range: %f", *entry.Utilization)
	}
}

func TestAnthropicClient_FetchQuotas_Headers(t *testing.T) {
	var gotAuth atomic.Value
	var gotBeta atomic.Value
	var gotUserAgent atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotBeta.Store(r.Header.Get("anthropic-beta"))
		gotUserAgent.Store(r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, testutil.DefaultAnthropicResponse())
	}))
	defer server.Close()

	client := api.NewAnthropicClient("my_secret_token", testutil.DiscardLogger(),
		api.WithAnthropicBaseURL(server.URL),
	)

	_, err := client.FetchQuotas(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	auth, _ := gotAuth.Load().(string)
	if auth != "Bearer my_secret_token" {
		t.Errorf("expected 'Bearer my_secret_token', got %q", auth)
	}

	beta, _ := gotBeta.Load().(string)
	if beta != "oauth-2025-04-20" {
		t.Errorf("expected 'oauth-2025-04-20', got %q", beta)
	}

	ua, _ := gotUserAgent.Load().(string)
	if ua != "onwatch/1.0" {
		t.Errorf("expected 'onwatch/1.0', got %q", ua)
	}
}

func TestAnthropicClient_FetchQuotas_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error": "unauthorized"}`)
	}))
	defer server.Close()

	client := api.NewAnthropicClient("bad_token", testutil.DiscardLogger(),
		api.WithAnthropicBaseURL(server.URL),
	)

	_, err := client.FetchQuotas(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, api.ErrAnthropicUnauthorized) {
		t.Errorf("expected ErrAnthropicUnauthorized, got %v", err)
	}
}

func TestAnthropicClient_FetchQuotas_ServerError(t *testing.T) {
	for _, code := range []int{500, 502, 503} {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
			}))
			defer server.Close()

			client := api.NewAnthropicClient("token", testutil.DiscardLogger(),
				api.WithAnthropicBaseURL(server.URL),
			)

			_, err := client.FetchQuotas(context.Background())
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, api.ErrAnthropicServerError) {
				t.Errorf("expected ErrAnthropicServerError for %d, got %v", code, err)
			}
		})
	}
}

func TestAnthropicClient_FetchQuotas_UnexpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden) // 403
	}))
	defer server.Close()

	client := api.NewAnthropicClient("token", testutil.DiscardLogger(),
		api.WithAnthropicBaseURL(server.URL),
	)

	_, err := client.FetchQuotas(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Should contain the status code in the error message
	errMsg := err.Error()
	if !strings.Contains(errMsg, "403") {
		t.Errorf("expected error to contain '403', got %q", errMsg)
	}
	// Should NOT be wrapped as ErrAnthropicUnauthorized or ErrAnthropicServerError
	if errors.Is(err, api.ErrAnthropicUnauthorized) {
		t.Error("should not be ErrAnthropicUnauthorized for 403")
	}
	if errors.Is(err, api.ErrAnthropicServerError) {
		t.Error("should not be ErrAnthropicServerError for 403")
	}
}

func TestAnthropicClient_FetchQuotas_EmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write nothing
	}))
	defer server.Close()

	client := api.NewAnthropicClient("token", testutil.DiscardLogger(),
		api.WithAnthropicBaseURL(server.URL),
	)

	_, err := client.FetchQuotas(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, api.ErrAnthropicInvalidResponse) {
		t.Errorf("expected ErrAnthropicInvalidResponse, got %v", err)
	}
}

func TestAnthropicClient_FetchQuotas_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "this is not json {{{")
	}))
	defer server.Close()

	client := api.NewAnthropicClient("token", testutil.DiscardLogger(),
		api.WithAnthropicBaseURL(server.URL),
	)

	_, err := client.FetchQuotas(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, api.ErrAnthropicInvalidResponse) {
		t.Errorf("expected ErrAnthropicInvalidResponse, got %v", err)
	}
}

func TestAnthropicClient_FetchQuotas_ContextCancelled(t *testing.T) {
	// Server that is slow to respond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := api.NewAnthropicClient("token", testutil.DiscardLogger(),
		api.WithAnthropicBaseURL(server.URL),
		api.WithAnthropicTimeout(10*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately
	cancel()

	_, err := client.FetchQuotas(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Should return the context error
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestAnthropicClient_SetToken_UpdatesAuth(t *testing.T) {
	var lastAuth atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, testutil.DefaultAnthropicResponse())
	}))
	defer server.Close()

	client := api.NewAnthropicClient("original_token", testutil.DiscardLogger(),
		api.WithAnthropicBaseURL(server.URL),
	)

	// First request with original token
	_, err := client.FetchQuotas(context.Background())
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	auth1, _ := lastAuth.Load().(string)
	if auth1 != "Bearer original_token" {
		t.Errorf("first request: expected 'Bearer original_token', got %q", auth1)
	}

	// Update token
	client.SetToken("refreshed_token")

	// Second request should use new token
	_, err = client.FetchQuotas(context.Background())
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}

	auth2, _ := lastAuth.Load().(string)
	if auth2 != "Bearer refreshed_token" {
		t.Errorf("second request: expected 'Bearer refreshed_token', got %q", auth2)
	}
}

