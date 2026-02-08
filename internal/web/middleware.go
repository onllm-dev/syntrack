// Package web provides HTTP server components for the onWatch dashboard.
package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/onllm-dev/onwatch/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns the bcrypt hash of a password.
// Uses bcrypt.DefaultCost (10) for a good balance of security and performance.
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(bytes), nil
}

// CheckPasswordHash compares a password with a hash.
// Returns true if they match, false otherwise.
func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// IsLegacyHash checks if a password hash is the old SHA-256 format.
// Legacy hashes are exactly 64 hex characters.
func IsLegacyHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	// Check if it's valid hex
	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// legacyHashPassword returns the SHA-256 hex hash of a password.
// Used for backward compatibility with legacy password hashes.
func legacyHashPassword(password string) string {
	h := sha256.Sum256([]byte(password))
	return fmt.Sprintf("%x", h)
}

const sessionCookieName = "onwatch_session"
const sessionMaxAge = 7 * 24 * 3600 // 7 days

// SessionStore manages session tokens with SQLite persistence and in-memory cache.
type SessionStore struct {
	mu           sync.RWMutex
	tokens       map[string]time.Time // in-memory cache: token -> expiry
	username     string
	passwordHash string       // SHA-256 hex hash of password
	store        *store.Store // optional: if set, tokens are persisted across restarts
}

// NewSessionStore creates a session store with the given credentials.
// passwordHash should be a SHA-256 hex hash of the password.
// If a store is provided, tokens are persisted in SQLite.
func NewSessionStore(username, passwordHash string, db *store.Store) *SessionStore {
	ss := &SessionStore{
		tokens:       make(map[string]time.Time),
		username:     username,
		passwordHash: passwordHash,
		store:        db,
	}
	// Clean expired tokens and preload valid ones from DB
	if db != nil {
		db.CleanExpiredAuthTokens()
	}
	return ss
}

// Authenticate validates credentials and returns a session token if valid.
// Supports both bcrypt (new) and SHA-256 (legacy) password hashes.
func (s *SessionStore) Authenticate(username, password string) (string, bool) {
	userMatch := subtle.ConstantTimeCompare([]byte(username), []byte(s.username)) == 1
	if !userMatch {
		return "", false
	}

	s.mu.RLock()
	storedHash := s.passwordHash
	s.mu.RUnlock()

	// Check password using bcrypt or legacy SHA-256
	var passMatch bool
	if IsLegacyHash(storedHash) {
		// Legacy SHA-256 hash - use constant time comparison
		incomingHash := legacyHashPassword(password)
		passMatch = subtle.ConstantTimeCompare([]byte(incomingHash), []byte(storedHash)) == 1
	} else {
		// Modern bcrypt hash
		passMatch = CheckPasswordHash(password, storedHash)
	}

	if !passMatch {
		return "", false
	}

	token := generateToken()
	expiry := time.Now().Add(time.Duration(sessionMaxAge) * time.Second)
	s.mu.Lock()
	s.tokens[token] = expiry
	s.mu.Unlock()
	// Persist to SQLite
	if s.store != nil {
		s.store.SaveAuthToken(token, expiry)
	}
	return token, true
}

// ValidateToken checks if a session token is valid and not expired.
func (s *SessionStore) ValidateToken(token string) bool {
	if token == "" {
		return false
	}
	// Check in-memory cache first
	s.mu.RLock()
	expiry, ok := s.tokens[token]
	s.mu.RUnlock()
	if ok {
		if time.Now().After(expiry) {
			s.mu.Lock()
			delete(s.tokens, token)
			s.mu.Unlock()
			if s.store != nil {
				s.store.DeleteAuthToken(token)
			}
			return false
		}
		return true
	}
	// Not in cache — check SQLite (handles tokens from previous daemon run)
	if s.store != nil {
		dbExpiry, found, err := s.store.GetAuthTokenExpiry(token)
		if err != nil || !found {
			return false
		}
		if time.Now().After(dbExpiry) {
			s.store.DeleteAuthToken(token)
			return false
		}
		// Valid in DB — add to in-memory cache
		s.mu.Lock()
		s.tokens[token] = dbExpiry
		s.mu.Unlock()
		return true
	}
	return false
}

// Invalidate removes a session token.
func (s *SessionStore) Invalidate(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
	if s.store != nil {
		s.store.DeleteAuthToken(token)
	}
}

// UpdatePassword updates the stored password hash.
func (s *SessionStore) UpdatePassword(newHash string) {
	s.mu.Lock()
	s.passwordHash = newHash
	s.mu.Unlock()
}

// InvalidateAll removes all session tokens (used after password change).
func (s *SessionStore) InvalidateAll() {
	s.mu.Lock()
	s.tokens = make(map[string]time.Time)
	s.mu.Unlock()
	if s.store != nil {
		s.store.DeleteAllAuthTokens()
	}
}

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// SessionAuthMiddleware uses session cookies for browser requests and Basic Auth for API.
func SessionAuthMiddleware(sessions *SessionStore, logger ...*slog.Logger) func(http.Handler) http.Handler {
	var log *slog.Logger
	if len(logger) > 0 && logger[0] != nil {
		log = logger[0]
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// Static assets and public files bypass authentication
			if isStaticAsset(path) {
				next.ServeHTTP(w, r)
				return
			}

			// Login page is always accessible
			if path == "/login" {
				next.ServeHTTP(w, r)
				return
			}

			// Check session cookie first
			if cookie, err := r.Cookie(sessionCookieName); err == nil {
				if sessions.ValidateToken(cookie.Value) {
					next.ServeHTTP(w, r)
					return
				}
			}

			// For API endpoints, also accept Basic Auth (for curl/scripts)
			if strings.HasPrefix(path, "/api/") {
				u, p, ok := extractCredentials(r)
				if ok {
					userMatch := subtle.ConstantTimeCompare([]byte(u), []byte(sessions.username)) == 1
					if !userMatch {
						// Continue to auth failed response
					} else {
						sessions.mu.RLock()
						storedHash := sessions.passwordHash
						sessions.mu.RUnlock()

						// Check password using bcrypt or legacy SHA-256
						var passMatch bool
						if IsLegacyHash(storedHash) {
							// Legacy SHA-256 hash
							incomingHash := legacyHashPassword(p)
							passMatch = subtle.ConstantTimeCompare([]byte(incomingHash), []byte(storedHash)) == 1
						} else {
							// Modern bcrypt hash
							passMatch = CheckPasswordHash(p, storedHash)
						}

						if passMatch {
							next.ServeHTTP(w, r)
							return
						}
					}
				}
				if log != nil {
					log.Debug("Auth rejected", "path", path, "method", r.Method, "remote", r.RemoteAddr)
				}
				// Return JSON 401 without WWW-Authenticate to prevent browser popup
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized","login":"/login"}`))
				return
			}

			// Browser requests: redirect to login page
			if log != nil {
				log.Debug("Unauthenticated request, redirecting to login", "path", path, "method", r.Method, "remote", r.RemoteAddr)
			}
			http.Redirect(w, r, "/login", http.StatusFound)
		})
	}
}

// AuthMiddleware returns an http.Handler that enforces Basic Auth.
// Kept for backwards compatibility with tests.
func AuthMiddleware(username, password string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isStaticAsset(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			u, p, ok := extractCredentials(r)
			if !ok {
				writeUnauthorized(w)
				return
			}

			userMatch := subtle.ConstantTimeCompare([]byte(u), []byte(username)) == 1
			passMatch := subtle.ConstantTimeCompare([]byte(p), []byte(password)) == 1

			if !userMatch || !passMatch {
				writeUnauthorized(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth is an alias for AuthMiddleware.
func RequireAuth(username, password string) func(http.Handler) http.Handler {
	return AuthMiddleware(username, password)
}

// extractCredentials extracts username and password from the Authorization header.
func extractCredentials(r *http.Request) (username, password string, ok bool) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", "", false
	}

	const prefix = "Basic "
	if !strings.HasPrefix(authHeader, prefix) {
		return "", "", false
	}

	encoded := authHeader[len(prefix):]
	if encoded == "" {
		return "", "", false
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", false
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}

	return parts[0], parts[1], true
}

// isStaticAsset checks if the request path is for a static asset.
func isStaticAsset(path string) bool {
	return strings.HasPrefix(path, "/static/")
}

// writeUnauthorized sends a 401 Unauthorized response.
func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="onWatch"`)
	w.WriteHeader(http.StatusUnauthorized)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}
