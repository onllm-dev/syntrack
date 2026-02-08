// Command mockserver runs a standalone mock API server for E2E (Playwright) tests.
// It wraps the testutil.MockServer and exposes /admin/* endpoints for runtime mutation.
//
// Usage:
//
//	go run ./internal/testutil/cmd/mockserver [flags]
//
// Flags:
//
//	--port        HTTP port (default: 19212)
//	--syn-key     Expected Synthetic API key (default: syn_test_e2e_key)
//	--zai-key     Expected Z.ai API key (default: zai_test_e2e_key)
//	--anth-token  Expected Anthropic OAuth token (default: anth_test_e2e_token)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/onllm-dev/onwatch/internal/testutil"
)

func main() {
	port := flag.Int("port", 19212, "HTTP port for the mock server")
	synKey := flag.String("syn-key", "syn_test_e2e_key", "Expected Synthetic API key")
	zaiKey := flag.String("zai-key", "zai_test_e2e_key", "Expected Z.ai API key")
	anthToken := flag.String("anth-token", "anth_test_e2e_token", "Expected Anthropic OAuth token")
	flag.Parse()

	srv := newStandaloneServer(*synKey, *zaiKey, *anthToken)

	addr := fmt.Sprintf(":%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", addr, err)
	}

	httpSrv := &http.Server{
		Handler:      srv.mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("mock server listening on http://localhost:%d", *port)
		log.Printf("  Synthetic key: %s", *synKey)
		log.Printf("  Z.ai key:      %s", *zaiKey)
		log.Printf("  Anthropic tok: %s", *anthToken)
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Wait for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(ctx)
}

// standaloneServer wraps the mock server logic without using httptest.Server,
// allowing it to run as a real HTTP server on a configurable port.
type standaloneServer struct {
	mux *http.ServeMux

	mu sync.RWMutex

	syntheticKey       string
	syntheticResponses []string
	syntheticError     atomic.Int32
	syntheticIdx       atomic.Int64
	syntheticCount     atomic.Int64

	zaiKey       string
	zaiResponses []string
	zaiError     atomic.Int32
	zaiIdx       atomic.Int64
	zaiCount     atomic.Int64

	anthropicToken     string
	anthropicResponses []string
	anthropicError     atomic.Int32
	anthropicIdx       atomic.Int64
	anthropicCount     atomic.Int64
}

func newStandaloneServer(synKey, zaiKey, anthToken string) *standaloneServer {
	srv := &standaloneServer{
		mux:                http.NewServeMux(),
		syntheticKey:       synKey,
		syntheticResponses: []string{testutil.DefaultSyntheticResponse()},
		zaiKey:             zaiKey,
		zaiResponses:       []string{testutil.DefaultZaiResponse()},
		anthropicToken:     anthToken,
		anthropicResponses: []string{testutil.DefaultAnthropicResponse()},
	}

	srv.mux.HandleFunc("/v2/quotas", srv.handleSynthetic)
	srv.mux.HandleFunc("/monitor/usage/quota/limit", srv.handleZai)
	srv.mux.HandleFunc("/api/oauth/usage", srv.handleAnthropic)
	srv.mux.HandleFunc("/admin/scenario", srv.handleAdminScenario)
	srv.mux.HandleFunc("/admin/error", srv.handleAdminError)
	srv.mux.HandleFunc("/admin/requests", srv.handleAdminRequests)
	srv.mux.HandleFunc("/admin/reset", srv.handleAdminReset)

	return srv
}

func (s *standaloneServer) handleSynthetic(w http.ResponseWriter, r *http.Request) {
	s.syntheticCount.Add(1)

	if errCode := s.syntheticError.Load(); errCode > 0 {
		w.WriteHeader(int(errCode))
		fmt.Fprintf(w, `{"error": "injected error %d"}`, errCode)
		return
	}

	s.mu.RLock()
	expectedKey := s.syntheticKey
	responses := s.syntheticResponses
	s.mu.RUnlock()

	if expectedKey != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+expectedKey {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error": "unauthorized"}`)
			return
		}
	}

	idx := s.syntheticIdx.Add(1) - 1
	respIdx := int(idx) % len(responses)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, responses[respIdx])
}

func (s *standaloneServer) handleZai(w http.ResponseWriter, r *http.Request) {
	s.zaiCount.Add(1)

	if errCode := s.zaiError.Load(); errCode > 0 {
		w.WriteHeader(int(errCode))
		fmt.Fprintf(w, `{"error": "injected error %d"}`, errCode)
		return
	}

	s.mu.RLock()
	expectedKey := s.zaiKey
	responses := s.zaiResponses
	s.mu.RUnlock()

	if expectedKey != "" {
		auth := r.Header.Get("Authorization")
		if auth != expectedKey {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, testutil.ZaiAuthErrorResponse())
			return
		}
	}

	idx := s.zaiIdx.Add(1) - 1
	respIdx := int(idx) % len(responses)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, responses[respIdx])
}

func (s *standaloneServer) handleAnthropic(w http.ResponseWriter, r *http.Request) {
	s.anthropicCount.Add(1)

	if errCode := s.anthropicError.Load(); errCode > 0 {
		w.WriteHeader(int(errCode))
		fmt.Fprintf(w, `{"error": "injected error %d"}`, errCode)
		return
	}

	s.mu.RLock()
	expectedToken := s.anthropicToken
	responses := s.anthropicResponses
	s.mu.RUnlock()

	if expectedToken != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+expectedToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error": "unauthorized"}`)
			return
		}
	}

	idx := s.anthropicIdx.Add(1) - 1
	respIdx := int(idx) % len(responses)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, responses[respIdx])
}

func (s *standaloneServer) handleAdminScenario(w http.ResponseWriter, r *http.Request) {
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

	s.mu.Lock()
	defer s.mu.Unlock()

	switch strings.ToLower(payload.Provider) {
	case "synthetic":
		s.syntheticResponses = payload.Responses
		s.syntheticIdx.Store(0)
	case "zai":
		s.zaiResponses = payload.Responses
		s.zaiIdx.Store(0)
	case "anthropic":
		s.anthropicResponses = payload.Responses
		s.anthropicIdx.Store(0)
	default:
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error": "unknown provider: %s"}`, payload.Provider)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok": true}`)
}

func (s *standaloneServer) handleAdminError(w http.ResponseWriter, r *http.Request) {
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

	switch strings.ToLower(payload.Provider) {
	case "synthetic":
		s.syntheticError.Store(int32(payload.StatusCode))
	case "zai":
		s.zaiError.Store(int32(payload.StatusCode))
	case "anthropic":
		s.anthropicError.Store(int32(payload.StatusCode))
	default:
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error": "unknown provider: %s"}`, payload.Provider)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok": true}`)
}

func (s *standaloneServer) handleAdminRequests(w http.ResponseWriter, _ *http.Request) {
	counts := map[string]int64{
		"synthetic": s.syntheticCount.Load(),
		"zai":       s.zaiCount.Load(),
		"anthropic": s.anthropicCount.Load(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(counts)
}

func (s *standaloneServer) handleAdminReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	s.syntheticError.Store(0)
	s.zaiError.Store(0)
	s.anthropicError.Store(0)
	s.syntheticCount.Store(0)
	s.zaiCount.Store(0)
	s.anthropicCount.Store(0)
	s.syntheticIdx.Store(0)
	s.zaiIdx.Store(0)
	s.anthropicIdx.Store(0)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok": true}`)
}
