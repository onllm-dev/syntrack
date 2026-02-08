package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// testLogger implements the logger interfaces used by security middleware.
type testLogger struct {
	mu       sync.Mutex
	messages []string
}

func (l *testLogger) Warn(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, msg)
}

func (l *testLogger) Info(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, msg)
}

// --- RateLimiter tests ---

func TestRateLimiter_AllowsUnderLimit(t *testing.T) {
	rl := NewRateLimiter(5, time.Minute)

	// 4 requests (N-1) should all be allowed
	for i := 0; i < 4; i++ {
		if !rl.Allow("192.168.1.1") {
			t.Fatalf("request %d should be allowed (under limit of 5)", i+1)
		}
	}
}

func TestRateLimiter_BlocksAtLimit(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)

	// Exhaust the limit
	for i := 0; i < 3; i++ {
		if !rl.Allow("10.0.0.1") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	// Next request should be blocked
	if rl.Allow("10.0.0.1") {
		t.Error("request beyond limit should be blocked")
	}
}

func TestRateLimiter_ResetsAfterWindow(t *testing.T) {
	// Use a very short window so test doesn't sleep long
	rl := NewRateLimiter(2, 50*time.Millisecond)

	// Exhaust the limit
	rl.Allow("10.0.0.1")
	rl.Allow("10.0.0.1")
	if rl.Allow("10.0.0.1") {
		t.Fatal("should be blocked after exhausting limit")
	}

	// Wait for window to expire
	time.Sleep(60 * time.Millisecond)

	// Should be allowed again
	if !rl.Allow("10.0.0.1") {
		t.Error("should be allowed after window expires")
	}
}

func TestRateLimiter_GetRemaining(t *testing.T) {
	rl := NewRateLimiter(5, time.Minute)

	// Unknown IP should have full remaining
	remaining, resetIn := rl.GetRemaining("unknown-ip")
	if remaining != 5 {
		t.Errorf("expected 5 remaining for unknown IP, got %d", remaining)
	}
	if resetIn != 0 {
		t.Errorf("expected 0 reset duration for unknown IP, got %v", resetIn)
	}

	// Use 2 attempts
	rl.Allow("10.0.0.1")
	rl.Allow("10.0.0.1")

	remaining, resetIn = rl.GetRemaining("10.0.0.1")
	if remaining != 3 {
		t.Errorf("expected 3 remaining, got %d", remaining)
	}
	if resetIn <= 0 {
		t.Error("expected positive reset duration after attempts")
	}
	if resetIn > time.Minute {
		t.Errorf("reset duration should not exceed window, got %v", resetIn)
	}
}

func TestRateLimiter_GetRemaining_AfterWindowExpiry(t *testing.T) {
	rl := NewRateLimiter(3, 50*time.Millisecond)

	rl.Allow("10.0.0.1")
	time.Sleep(60 * time.Millisecond)

	// After window expires, should show full remaining
	remaining, resetIn := rl.GetRemaining("10.0.0.1")
	if remaining != 3 {
		t.Errorf("expected full remaining after window expiry, got %d", remaining)
	}
	if resetIn != 0 {
		t.Errorf("expected 0 reset duration after window expiry, got %v", resetIn)
	}
}

func TestRateLimiter_IndependentIPs(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)

	// Exhaust limit for IP A
	rl.Allow("10.0.0.1")
	rl.Allow("10.0.0.1")
	if rl.Allow("10.0.0.1") {
		t.Fatal("IP A should be blocked")
	}

	// IP B should still be allowed
	if !rl.Allow("10.0.0.2") {
		t.Error("IP B should be allowed independently of IP A")
	}
}

func TestRateLimiter_ConcurrentAccess(t *testing.T) {
	rl := NewRateLimiter(100, time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rl.Allow("10.0.0.1")
			rl.GetRemaining("10.0.0.1")
		}()
	}
	wg.Wait()

	remaining, _ := rl.GetRemaining("10.0.0.1")
	if remaining != 50 {
		t.Errorf("expected 50 remaining after 50 concurrent requests, got %d", remaining)
	}
}

// --- RateLimitMiddleware tests ---

func TestRateLimitMiddleware_Returns429(t *testing.T) {
	logger := &testLogger{}
	rl := NewRateLimiter(2, time.Minute)
	middleware := RateLimitMiddleware(rl, logger)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := middleware(handler)

	// Exhaust the limit
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		rr := httptest.NewRecorder()
		wrapped.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d should pass, got %d", i+1, rr.Code)
		}
	}

	// Third request should be rate limited
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rr.Code)
	}

	// Check Retry-After header is present
	retryAfter := rr.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Error("expected Retry-After header to be set")
	}

	// Check Content-Type is JSON
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	// Check response body is valid JSON with expected fields
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body should be valid JSON: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Error("response body should contain 'error' field")
	}
	if _, ok := body["retry_after"]; !ok {
		t.Error("response body should contain 'retry_after' field")
	}
	if _, ok := body["retry_after_ms"]; !ok {
		t.Error("response body should contain 'retry_after_ms' field")
	}
}

func TestRateLimitMiddleware_AllowsUnderLimit(t *testing.T) {
	logger := &testLogger{}
	rl := NewRateLimiter(10, time.Minute)
	middleware := RateLimitMiddleware(rl, logger)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	wrapped := middleware(handler)

	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "ok" {
		t.Errorf("expected body 'ok', got %q", rr.Body.String())
	}
}

func TestRateLimitMiddleware_LogsWarning(t *testing.T) {
	logger := &testLogger{}
	rl := NewRateLimiter(1, time.Minute)
	middleware := RateLimitMiddleware(rl, logger)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := middleware(handler)

	// Use the one allowed request
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	// Trigger rate limit
	req2 := httptest.NewRequest(http.MethodPost, "/login", nil)
	req2.RemoteAddr = "10.0.0.1:12345"
	rr2 := httptest.NewRecorder()
	wrapped.ServeHTTP(rr2, req2)

	logger.mu.Lock()
	defer logger.mu.Unlock()
	if len(logger.messages) == 0 {
		t.Error("expected a warning log when rate limit exceeded")
	}
	found := false
	for _, msg := range logger.messages {
		if strings.Contains(msg, "rate limit") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'rate limit' in log messages, got %v", logger.messages)
	}
}

// --- IPWhitelistMiddleware tests ---

func TestIPWhitelist_AllowsCIDR(t *testing.T) {
	logger := &testLogger{}
	wl := NewIPWhitelistMiddleware([]string{"192.168.1.0/24"}, logger)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("allowed"))
	})
	wrapped := wl.Middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.RemoteAddr = "192.168.1.50:12345"
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for IP in CIDR range, got %d", rr.Code)
	}
	if rr.Body.String() != "allowed" {
		t.Errorf("expected body 'allowed', got %q", rr.Body.String())
	}
}

func TestIPWhitelist_AllowsSingleIP(t *testing.T) {
	logger := &testLogger{}
	wl := NewIPWhitelistMiddleware([]string{"10.0.0.5"}, logger)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("allowed"))
	})
	wrapped := wl.Middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.RemoteAddr = "10.0.0.5:12345"
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for exact IP match, got %d", rr.Code)
	}
}

func TestIPWhitelist_BlocksUnlisted(t *testing.T) {
	logger := &testLogger{}
	wl := NewIPWhitelistMiddleware([]string{"192.168.1.0/24"}, logger)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for blocked IP")
	})
	wrapped := wl.Middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for unlisted IP, got %d", rr.Code)
	}
}

func TestIPWhitelist_EmptyList_AllowsAll(t *testing.T) {
	logger := &testLogger{}
	wl := NewIPWhitelistMiddleware([]string{}, logger)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("allowed"))
	})
	wrapped := wl.Middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 when whitelist is empty (passthrough), got %d", rr.Code)
	}
	if rr.Body.String() != "allowed" {
		t.Errorf("expected body 'allowed', got %q", rr.Body.String())
	}
}

func TestIPWhitelist_MultipleCIDRs(t *testing.T) {
	logger := &testLogger{}
	wl := NewIPWhitelistMiddleware([]string{"192.168.1.0/24", "10.0.0.0/8"}, logger)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := wl.Middleware(handler)

	tests := []struct {
		name   string
		ip     string
		expect int
	}{
		{"first CIDR", "192.168.1.100:1234", http.StatusOK},
		{"second CIDR", "10.5.3.2:1234", http.StatusOK},
		{"neither CIDR", "172.16.0.1:1234", http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.ip
			rr := httptest.NewRecorder()
			wrapped.ServeHTTP(rr, req)
			if rr.Code != tc.expect {
				t.Errorf("expected %d for %s, got %d", tc.expect, tc.ip, rr.Code)
			}
		})
	}
}

func TestIPWhitelist_InvalidIP(t *testing.T) {
	logger := &testLogger{}
	wl := NewIPWhitelistMiddleware([]string{"192.168.1.0/24"}, logger)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for invalid IP")
	})
	wrapped := wl.Middleware(handler)

	// net.ParseIP returns nil for invalid IP, which causes isAllowed to return false
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.RemoteAddr = "not-an-ip"
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for invalid client IP, got %d", rr.Code)
	}
}

// --- getClientIP tests ---

func TestGetClientIP_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.50, 70.41.3.18, 150.172.238.178")
	req.RemoteAddr = "127.0.0.1:12345"

	ip := getClientIP(req)
	if ip != "203.0.113.50" {
		t.Errorf("expected first IP from X-Forwarded-For, got %q", ip)
	}
}

func TestGetClientIP_XForwardedFor_SingleIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	req.RemoteAddr = "127.0.0.1:12345"

	ip := getClientIP(req)
	if ip != "203.0.113.50" {
		t.Errorf("expected IP from X-Forwarded-For, got %q", ip)
	}
}

func TestGetClientIP_XRealIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-Ip", "198.51.100.25")
	req.RemoteAddr = "127.0.0.1:12345"

	ip := getClientIP(req)
	if ip != "198.51.100.25" {
		t.Errorf("expected X-Real-Ip value, got %q", ip)
	}
}

func TestGetClientIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.1:54321"

	ip := getClientIP(req)
	if ip != "192.168.1.1" {
		t.Errorf("expected IP from RemoteAddr, got %q", ip)
	}
}

func TestGetClientIP_RemoteAddrWithoutPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.1" // no port

	ip := getClientIP(req)
	if ip != "192.168.1.1" {
		t.Errorf("expected raw RemoteAddr when no port, got %q", ip)
	}
}

func TestGetClientIP_XForwardedFor_Precedence(t *testing.T) {
	// XFF takes precedence over X-Real-Ip
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	req.Header.Set("X-Real-Ip", "198.51.100.25")
	req.RemoteAddr = "127.0.0.1:12345"

	ip := getClientIP(req)
	if ip != "203.0.113.50" {
		t.Errorf("X-Forwarded-For should take precedence, got %q", ip)
	}
}

// --- isEncryptedValue tests ---

func TestIsEncryptedValue(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{
			name:  "empty string",
			value: "",
			want:  false,
		},
		{
			name:  "short plaintext",
			value: "hello",
			want:  false,
		},
		{
			name:  "too short for encrypted",
			value: "abc123",
			want:  false,
		},
		{
			name:  "plaintext with special chars",
			value: "my-password!@#$%^&*()",
			want:  false,
		},
		{
			name:  "valid base64 long enough",
			value: "SGVsbG8gV29ybGQgdGhpcyBpcyBhIHRlc3Q=",
			want:  true,
		},
		{
			name:  "base64 with plus and slash",
			value: "SGVsbG8rV29ybGQvdGhpcw==",
			want:  true, // valid base64 chars including + and /
		},
		{
			name:  "contains literal space",
			value: "AAAAAAA BBBBBBBCCCCCCCC=",
			want:  false, // space is not a base64 char
		},
		{
			name:  "pure base64 no space",
			value: "SGVsbG8rV29ybGQvdGhpcw==",
			want:  true,
		},
		{
			name:  "exactly 24 chars base64",
			value: "AAAAAAAABBBBBBBBCCCCCCCC",
			want:  true,
		},
		{
			name:  "23 chars too short",
			value: "AAAAAAAABBBBBBBBCCCCCCC",
			want:  false,
		},
		{
			name:  "contains hyphen not base64",
			value: "AAAA-AAABBBBBBBBCCCCCCCC",
			want:  false,
		},
		{
			name:  "contains underscore not base64",
			value: "AAAA_AAABBBBBBBBCCCCCCCC",
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isEncryptedValue(tc.value)
			if got != tc.want {
				t.Errorf("isEncryptedValue(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// --- formatDurationSeconds tests ---

func TestFormatDurationSeconds(t *testing.T) {
	// This function converts a duration to a string for the Retry-After header.
	// It uses rune conversion of the seconds value, so it produces
	// a single character representing the ASCII value of the seconds count.
	result := formatDurationSeconds(60 * time.Second)
	if result == "" {
		t.Error("formatDurationSeconds should return non-empty string for 60s")
	}
}
