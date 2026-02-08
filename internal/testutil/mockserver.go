package testutil

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// MockServer provides a unified test server that routes all three provider endpoints.
// Thread-safe for concurrent use from test goroutines and httptest handler goroutines.
type MockServer struct {
	*httptest.Server

	// Per-provider configuration (protected by mu)
	mu sync.RWMutex

	syntheticKey       string
	syntheticResponses []string
	syntheticError     atomic.Int32 // 0 = no error, >0 = HTTP status code

	zaiKey       string
	zaiResponses []string
	zaiError     atomic.Int32

	anthropicToken     string
	anthropicResponses []string
	anthropicError     atomic.Int32

	// Round-robin response indexes (atomic for thread safety)
	syntheticIdx  atomic.Int64
	zaiIdx        atomic.Int64
	anthropicIdx  atomic.Int64

	// Request counters (atomic for thread safety)
	syntheticCount  atomic.Int64
	zaiCount        atomic.Int64
	anthropicCount  atomic.Int64
}

// MockOption configures a MockServer.
type MockOption func(*MockServer)

// WithSyntheticKey sets the expected Synthetic API key.
func WithSyntheticKey(key string) MockOption {
	return func(ms *MockServer) {
		ms.syntheticKey = key
	}
}

// WithSyntheticResponses sets the Synthetic response sequence.
func WithSyntheticResponses(responses []string) MockOption {
	return func(ms *MockServer) {
		ms.syntheticResponses = responses
	}
}

// WithZaiKey sets the expected Z.ai API key.
func WithZaiKey(key string) MockOption {
	return func(ms *MockServer) {
		ms.zaiKey = key
	}
}

// WithZaiResponses sets the Z.ai response sequence.
func WithZaiResponses(responses []string) MockOption {
	return func(ms *MockServer) {
		ms.zaiResponses = responses
	}
}

// WithAnthropicToken sets the expected Anthropic OAuth token.
func WithAnthropicToken(token string) MockOption {
	return func(ms *MockServer) {
		ms.anthropicToken = token
	}
}

// WithAnthropicResponses sets the Anthropic response sequence.
func WithAnthropicResponses(responses []string) MockOption {
	return func(ms *MockServer) {
		ms.anthropicResponses = responses
	}
}

// NewMockServer creates a new mock server with the given options.
// The server routes requests to the appropriate provider handler based on URL path.
func NewMockServer(t *testing.T, opts ...MockOption) *MockServer {
	t.Helper()

	ms := &MockServer{}

	for _, opt := range opts {
		opt(ms)
	}

	// Set default responses if none provided
	if ms.syntheticKey != "" && len(ms.syntheticResponses) == 0 {
		ms.syntheticResponses = []string{DefaultSyntheticResponse()}
	}
	if ms.zaiKey != "" && len(ms.zaiResponses) == 0 {
		ms.zaiResponses = []string{DefaultZaiResponse()}
	}
	if ms.anthropicToken != "" && len(ms.anthropicResponses) == 0 {
		ms.anthropicResponses = []string{DefaultAnthropicResponse()}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/quotas", ms.handleSynthetic)
	mux.HandleFunc("/monitor/usage/quota/limit", ms.handleZai)
	mux.HandleFunc("/api/oauth/usage", ms.handleAnthropic)
	mux.HandleFunc("/admin/scenario", ms.handleAdminScenario)
	mux.HandleFunc("/admin/error", ms.handleAdminError)
	mux.HandleFunc("/admin/requests", ms.handleAdminRequests)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Catch-all for unknown routes
		if r.URL.Path != "/v2/quotas" &&
			r.URL.Path != "/monitor/usage/quota/limit" &&
			r.URL.Path != "/api/oauth/usage" &&
			!strings.HasPrefix(r.URL.Path, "/admin/") {
			http.NotFound(w, r)
		}
	})

	ms.Server = httptest.NewServer(mux)
	return ms
}

// handleSynthetic handles GET /v2/quotas
func (ms *MockServer) handleSynthetic(w http.ResponseWriter, r *http.Request) {
	ms.syntheticCount.Add(1)

	// Check for injected error
	if errCode := ms.syntheticError.Load(); errCode > 0 {
		w.WriteHeader(int(errCode))
		fmt.Fprintf(w, `{"error": "injected error %d"}`, errCode)
		return
	}

	// Validate auth: "Authorization: Bearer <key>"
	ms.mu.RLock()
	expectedKey := ms.syntheticKey
	responses := ms.syntheticResponses
	ms.mu.RUnlock()

	if expectedKey != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+expectedKey {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error": "unauthorized"}`)
			return
		}
	}

	if len(responses) == 0 {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, DefaultSyntheticResponse())
		return
	}

	idx := ms.syntheticIdx.Add(1) - 1
	respIdx := int(idx) % len(responses)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, responses[respIdx])
}

// handleZai handles GET /monitor/usage/quota/limit
func (ms *MockServer) handleZai(w http.ResponseWriter, r *http.Request) {
	ms.zaiCount.Add(1)

	// Check for injected error
	if errCode := ms.zaiError.Load(); errCode > 0 {
		w.WriteHeader(int(errCode))
		fmt.Fprintf(w, `{"error": "injected error %d"}`, errCode)
		return
	}

	// Validate auth: "Authorization: <key>" (no Bearer prefix for Z.ai)
	ms.mu.RLock()
	expectedKey := ms.zaiKey
	responses := ms.zaiResponses
	ms.mu.RUnlock()

	if expectedKey != "" {
		auth := r.Header.Get("Authorization")
		if auth != expectedKey {
			// Z.ai returns 200 with error code in body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, ZaiAuthErrorResponse())
			return
		}
	}

	if len(responses) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, DefaultZaiResponse())
		return
	}

	idx := ms.zaiIdx.Add(1) - 1
	respIdx := int(idx) % len(responses)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, responses[respIdx])
}

// handleAnthropic handles GET /api/oauth/usage
func (ms *MockServer) handleAnthropic(w http.ResponseWriter, r *http.Request) {
	ms.anthropicCount.Add(1)

	// Check for injected error
	if errCode := ms.anthropicError.Load(); errCode > 0 {
		w.WriteHeader(int(errCode))
		fmt.Fprintf(w, `{"error": "injected error %d"}`, errCode)
		return
	}

	// Validate auth: "Authorization: Bearer <token>"
	ms.mu.RLock()
	expectedToken := ms.anthropicToken
	responses := ms.anthropicResponses
	ms.mu.RUnlock()

	if expectedToken != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+expectedToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error": "unauthorized"}`)
			return
		}
	}

	if len(responses) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, DefaultAnthropicResponse())
		return
	}

	idx := ms.anthropicIdx.Add(1) - 1
	respIdx := int(idx) % len(responses)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, responses[respIdx])
}

// handleAdminScenario handles POST /admin/scenario to switch response sequences at runtime.
func (ms *MockServer) handleAdminScenario(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var payload struct {
		Provider  string   `json:"provider"`
		Responses []string `json:"responses"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error": %q}`, err.Error())
		return
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	switch payload.Provider {
	case "synthetic":
		ms.syntheticResponses = payload.Responses
		ms.syntheticIdx.Store(0)
	case "zai":
		ms.zaiResponses = payload.Responses
		ms.zaiIdx.Store(0)
	case "anthropic":
		ms.anthropicResponses = payload.Responses
		ms.anthropicIdx.Store(0)
	default:
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error": "unknown provider: %s"}`, payload.Provider)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"ok": true}`)
}

// handleAdminError handles POST /admin/error to inject HTTP errors.
func (ms *MockServer) handleAdminError(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var payload struct {
		Provider   string `json:"provider"`
		StatusCode int    `json:"status_code"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error": %q}`, err.Error())
		return
	}

	switch payload.Provider {
	case "synthetic":
		ms.syntheticError.Store(int32(payload.StatusCode))
	case "zai":
		ms.zaiError.Store(int32(payload.StatusCode))
	case "anthropic":
		ms.anthropicError.Store(int32(payload.StatusCode))
	default:
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error": "unknown provider: %s"}`, payload.Provider)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"ok": true}`)
}

// handleAdminRequests handles GET /admin/requests to return request counts.
func (ms *MockServer) handleAdminRequests(w http.ResponseWriter, _ *http.Request) {
	counts := map[string]int64{
		"synthetic": ms.syntheticCount.Load(),
		"zai":       ms.zaiCount.Load(),
		"anthropic": ms.anthropicCount.Load(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(counts)
}

// --- Runtime mutation methods (thread-safe) ---

// SetSyntheticError injects an HTTP error code for subsequent Synthetic requests.
func (ms *MockServer) SetSyntheticError(code int) {
	ms.syntheticError.Store(int32(code))
}

// SetZaiError injects an HTTP error code for subsequent Z.ai requests.
func (ms *MockServer) SetZaiError(code int) {
	ms.zaiError.Store(int32(code))
}

// SetAnthropicError injects an HTTP error code for subsequent Anthropic requests.
func (ms *MockServer) SetAnthropicError(code int) {
	ms.anthropicError.Store(int32(code))
}

// SetAnthropicToken changes the expected Anthropic token at runtime.
func (ms *MockServer) SetAnthropicToken(token string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.anthropicToken = token
}

// ClearErrors removes all injected errors.
func (ms *MockServer) ClearErrors() {
	ms.syntheticError.Store(0)
	ms.zaiError.Store(0)
	ms.anthropicError.Store(0)
}

// RequestCount returns the number of requests made to a provider.
func (ms *MockServer) RequestCount(provider string) int {
	switch provider {
	case "synthetic":
		return int(ms.syntheticCount.Load())
	case "zai":
		return int(ms.zaiCount.Load())
	case "anthropic":
		return int(ms.anthropicCount.Load())
	}
	return 0
}
