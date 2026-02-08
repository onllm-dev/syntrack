package web

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateLimiter implements simple IP-based rate limiting
type RateLimiter struct {
	attempts    map[string]int       // IP -> attempt count
	lastAttempt map[string]time.Time // IP -> last attempt time
	mu          sync.Mutex
	maxAttempts int
	window      time.Duration
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(maxAttempts int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		attempts:    make(map[string]int),
		lastAttempt: make(map[string]time.Time),
		maxAttempts: maxAttempts,
		window:      window,
	}
}

// Allow checks if a request from the given IP should be allowed
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	lastTime, exists := rl.lastAttempt[ip]

	// Reset counter if window has passed
	if exists && now.Sub(lastTime) > rl.window {
		rl.attempts[ip] = 0
	}

	// Check if over limit
	if rl.attempts[ip] >= rl.maxAttempts {
		return false
	}

	// Increment counter
	rl.attempts[ip]++
	rl.lastAttempt[ip] = now

	return true
}

// GetRemaining returns remaining attempts and time until reset
func (rl *RateLimiter) GetRemaining(ip string) (int, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	lastTime, exists := rl.lastAttempt[ip]
	if !exists {
		return rl.maxAttempts, 0
	}

	// If window passed, full reset
	if time.Since(lastTime) > rl.window {
		return rl.maxAttempts, 0
	}

	remaining := rl.maxAttempts - rl.attempts[ip]
	if remaining < 0 {
		remaining = 0
	}

	resetIn := rl.window - time.Since(lastTime)
	return remaining, resetIn
}

// RateLimitMiddleware creates a middleware that rate limits specific endpoints
func RateLimitMiddleware(limiter *RateLimiter, logger interface{ Warn(msg string, args ...any) }) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := getClientIP(r)

			if !limiter.Allow(ip) {
				remaining, resetIn := limiter.GetRemaining(ip)
				logger.Warn("rate limit exceeded", "ip", ip, "path", r.URL.Path, "remaining", remaining, "reset_in", resetIn)

				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", formatDurationSeconds(resetIn))
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error":          "rate limit exceeded",
					"retry_after":    formatDurationSeconds(resetIn),
					"retry_after_ms": int(resetIn.Milliseconds()),
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// getClientIP extracts the client IP from the request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (for proxies)
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		// Take the first IP in the chain
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// Check X-Real-Ip header
	xri := r.Header.Get("X-Real-Ip")
	if xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// formatDurationSeconds formats a duration as seconds for Retry-After header
func formatDurationSeconds(d time.Duration) string {
	return string(rune(int(d.Seconds())))
}

// IPWhitelistMiddleware creates a middleware that restricts access by IP
type IPWhitelistMiddleware struct {
	allowed []string // CIDR notation
	logger  interface{ Info(msg string, args ...any) }
}

// NewIPWhitelistMiddleware creates a new IP whitelist middleware
func NewIPWhitelistMiddleware(allowed []string, logger interface{ Info(msg string, args ...any) }) *IPWhitelistMiddleware {
	return &IPWhitelistMiddleware{
		allowed: allowed,
		logger:  logger,
	}
}

// Middleware returns the middleware handler
func (m *IPWhitelistMiddleware) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(m.allowed) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		clientIP := getClientIP(r)
		if !m.isAllowed(clientIP) {
			m.logger.Info("IP not in whitelist", "ip", clientIP, "path", r.URL.Path)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isAllowed checks if an IP is in the whitelist
func (m *IPWhitelistMiddleware) isAllowed(clientIP string) bool {
	// Parse client IP
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}

	// Check against each CIDR in whitelist
	for _, cidr := range m.allowed {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			// Try as single IP
			allowedIP := net.ParseIP(cidr)
			if allowedIP != nil && allowedIP.Equal(ip) {
				return true
			}
			continue
		}

		if ipNet.Contains(ip) {
			return true
		}
	}

	return false
}

// isEncryptedValue checks if a string looks like an encrypted value
// (base64 encoded with minimum length for nonce + ciphertext)
func isEncryptedValue(value string) bool {
	if value == "" {
		return false
	}

	// Encrypted values are base64 encoded and typically longer than plaintext
	// Minimum: 12 bytes nonce + 1 byte ciphertext + base64 overhead
	if len(value) < 24 {
		return false
	}

	// Check if it looks like base64 (contains only base64 chars)
	base64Chars := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/="
	for _, c := range value {
		if !strings.ContainsRune(base64Chars, c) {
			return false
		}
	}

	return true
}
