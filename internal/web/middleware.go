// Package web provides HTTP server components for the SynTrack dashboard.
package web

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

// AuthMiddleware returns an http.Handler that enforces Basic Auth.
// It protects all routes except those under /static/ which are publicly accessible.
func AuthMiddleware(username, password string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Static assets bypass authentication
			if isStaticAsset(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// Extract credentials from request
			u, p, ok := extractCredentials(r)
			if !ok {
				writeUnauthorized(w)
				return
			}

			// Perform constant-time comparison to prevent timing attacks
			userMatch := subtle.ConstantTimeCompare([]byte(u), []byte(username)) == 1
			passMatch := subtle.ConstantTimeCompare([]byte(p), []byte(password)) == 1

			if !userMatch || !passMatch {
				writeUnauthorized(w)
				return
			}

			// Authentication successful, proceed to next handler
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth is an alias for AuthMiddleware.
// It provides the same functionality with a more explicit name.
func RequireAuth(username, password string) func(http.Handler) http.Handler {
	return AuthMiddleware(username, password)
}

// extractCredentials extracts username and password from the Authorization header.
// Returns ok=false if the header is missing, malformed, or invalid.
func extractCredentials(r *http.Request) (username, password string, ok bool) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", "", false
	}

	// Must start with "Basic "
	const prefix = "Basic "
	if !strings.HasPrefix(authHeader, prefix) {
		return "", "", false
	}

	// Extract base64 encoded credentials
	encoded := authHeader[len(prefix):]
	if encoded == "" {
		return "", "", false
	}

	// Decode base64
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", false
	}

	// Split on first colon
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}

	return parts[0], parts[1], true
}

// isStaticAsset checks if the request path is for a static asset.
// Static assets under /static/ bypass authentication.
func isStaticAsset(path string) bool {
	return strings.HasPrefix(path, "/static/")
}

// writeUnauthorized sends a 401 Unauthorized response with the WWW-Authenticate header.
func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="SynTrack"`)
	w.WriteHeader(http.StatusUnauthorized)
	// Minimal response body - no sensitive information
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}
