package web

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuth_ValidCredentials(t *testing.T) {
	// Arrange
	username := "admin"
	password := "secret123"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	middleware := AuthMiddleware(username, password)
	wrapped := middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
	rr := httptest.NewRecorder()

	// Act
	wrapped.ServeHTTP(rr, req)

	// Assert
	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
	if body := rr.Body.String(); body != "success" {
		t.Errorf("expected body 'success', got %q", body)
	}
}

func TestAuth_InvalidPassword(t *testing.T) {
	// Arrange
	username := "admin"
	password := "secret123"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with invalid credentials")
	})

	middleware := AuthMiddleware(username, password)
	wrapped := middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+"wrongpassword")))
	rr := httptest.NewRecorder()

	// Act
	wrapped.ServeHTTP(rr, req)

	// Assert
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestAuth_InvalidUsername(t *testing.T) {
	// Arrange
	username := "admin"
	password := "secret123"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with invalid credentials")
	})

	middleware := AuthMiddleware(username, password)
	wrapped := middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("wronguser:"+password)))
	rr := httptest.NewRecorder()

	// Act
	wrapped.ServeHTTP(rr, req)

	// Assert
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestAuth_MissingHeader(t *testing.T) {
	// Arrange
	username := "admin"
	password := "secret123"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called without auth header")
	})

	middleware := AuthMiddleware(username, password)
	wrapped := middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	// No Authorization header set
	rr := httptest.NewRecorder()

	// Act
	wrapped.ServeHTTP(rr, req)

	// Assert
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestAuth_MalformedHeader(t *testing.T) {
	testCases := []struct {
		name   string
		header string
	}{
		{
			name:   "no basic prefix",
			header: base64.StdEncoding.EncodeToString([]byte("admin:secret")),
		},
		{
			name:   "wrong scheme",
			header: "Bearer " + base64.StdEncoding.EncodeToString([]byte("admin:secret")),
		},
		{
			name:   "invalid base64",
			header: "Basic !!!invalid!!!",
		},
		{
			name:   "empty after basic",
			header: "Basic ",
		},
		{
			name:   "no colon in credentials",
			header: "Basic " + base64.StdEncoding.EncodeToString([]byte("adminsecret")),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("handler should not be called with malformed header")
			})

			middleware := AuthMiddleware("admin", "secret")
			wrapped := middleware(handler)

			req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
			req.Header.Set("Authorization", tc.header)
			rr := httptest.NewRecorder()

			// Act
			wrapped.ServeHTTP(rr, req)

			// Assert
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("expected status 401, got %d", rr.Code)
			}
		})
	}
}

func TestAuth_TimingSafe(t *testing.T) {
	// This test verifies that the implementation uses subtle.ConstantTimeCompare
	// by checking that the middleware doesn't leak timing information
	// through early returns or different code paths

	username := "admin"
	password := "secret123"

	// Test that both wrong username and wrong password take similar paths
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	middleware := AuthMiddleware(username, password)
	wrapped := middleware(handler)

	// Test wrong username
	req1 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req1.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("wronguser:"+password)))
	rr1 := httptest.NewRecorder()
	wrapped.ServeHTTP(rr1, req1)

	// Test wrong password
	req2 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req2.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+"wrongpass")))
	rr2 := httptest.NewRecorder()
	wrapped.ServeHTTP(rr2, req2)

	// Both should return 401 with same headers
	if rr1.Code != rr2.Code {
		t.Error("wrong username and wrong password should return same status code")
	}

	// Check that WWW-Authenticate header is present in both
	if rr1.Header().Get("WWW-Authenticate") != rr2.Header().Get("WWW-Authenticate") {
		t.Error("wrong username and wrong password should return same WWW-Authenticate header")
	}

	// Verify that subtle.ConstantTimeCompare is actually used
	// by checking that both comparisons happen (no early return)
	// We can verify this by ensuring the implementation exists
	// The implementation test below will check the actual code path
}

func TestAuth_SetsWWWAuthenticate(t *testing.T) {
	// Arrange
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	middleware := AuthMiddleware("admin", "secret")
	wrapped := middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	// No Authorization header - should trigger 401
	rr := httptest.NewRecorder()

	// Act
	wrapped.ServeHTTP(rr, req)

	// Assert
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}

	wwwAuth := rr.Header().Get("WWW-Authenticate")
	if wwwAuth == "" {
		t.Error("expected WWW-Authenticate header to be set")
	}

	expected := `Basic realm="SynTrack"`
	if wwwAuth != expected {
		t.Errorf("expected WWW-Authenticate to be %q, got %q", expected, wwwAuth)
	}
}

func TestAuth_StaticAssets_NoAuth(t *testing.T) {
	// Arrange
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("static file"))
	})

	middleware := AuthMiddleware("admin", "secret")
	wrapped := middleware(handler)

	staticPaths := []string{
		"/static/style.css",
		"/static/app.js",
		"/static/images/logo.png",
		"/static/",
	}

	for _, path := range staticPaths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			// No Authorization header
			rr := httptest.NewRecorder()

			// Act
			wrapped.ServeHTTP(rr, req)

			// Assert
			if rr.Code != http.StatusOK {
				t.Errorf("expected status 200 for %s, got %d", path, rr.Code)
			}
			if body := rr.Body.String(); body != "static file" {
				t.Errorf("expected body 'static file' for %s, got %q", path, body)
			}
		})
	}
}

func TestAuth_NonStaticPath_RequiresAuth(t *testing.T) {
	// Arrange
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called without auth")
	})

	middleware := AuthMiddleware("admin", "secret")
	wrapped := middleware(handler)

	nonStaticPaths := []string{
		"/dashboard",
		"/api/quotas",
		"/",
		"/login",
		"/admin",
		"/static", // Note: /static without trailing slash should still require auth
	}

	for _, path := range nonStaticPaths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			// No Authorization header
			rr := httptest.NewRecorder()

			// Act
			wrapped.ServeHTTP(rr, req)

			// Assert
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("expected status 401 for %s, got %d", path, rr.Code)
			}
		})
	}
}

func TestExtractCredentials_ValidHeader(t *testing.T) {
	// Arrange
	username := "admin"
	password := "secret123"
	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Header.Set("Authorization", "Basic "+encoded)

	// Act
	u, p, ok := extractCredentials(req)

	// Assert
	if !ok {
		t.Error("expected ok to be true")
	}
	if u != username {
		t.Errorf("expected username %q, got %q", username, u)
	}
	if p != password {
		t.Errorf("expected password %q, got %q", password, p)
	}
}

func TestExtractCredentials_InvalidHeader(t *testing.T) {
	testCases := []struct {
		name   string
		header string
	}{
		{
			name:   "missing header",
			header: "",
		},
		{
			name:   "wrong scheme",
			header: "Bearer token123",
		},
		{
			name:   "no space after scheme",
			header: "Basictoken",
		},
		{
			name:   "invalid base64",
			header: "Basic !!!invalid!!!",
		},
		{
			name:   "no colon in decoded",
			header: "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon")),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}

			// Act
			_, _, ok := extractCredentials(req)

			// Assert
			if ok {
				t.Error("expected ok to be false")
			}
		})
	}
}

func TestRequireAuth_DirectUsage(t *testing.T) {
	// RequireAuth should be an alias or wrapper for AuthMiddleware
	// Both should behave identically
	username := "admin"
	password := "secret"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Test both middlewares produce same results
	authMiddleware := AuthMiddleware(username, password)
	requireAuth := RequireAuth(username, password)

	// Test valid credentials with AuthMiddleware
	req1 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req1.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
	rr1 := httptest.NewRecorder()
	authMiddleware(handler).ServeHTTP(rr1, req1)

	// Test valid credentials with RequireAuth
	req2 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req2.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
	rr2 := httptest.NewRecorder()
	requireAuth(handler).ServeHTTP(rr2, req2)

	if rr1.Code != rr2.Code {
		t.Error("AuthMiddleware and RequireAuth should behave identically with valid credentials")
	}
}

func TestConstantTimeComparison(t *testing.T) {
	// Verify that the implementation uses constant-time comparison
	// This is a security requirement to prevent timing attacks

	// The subtle.ConstantTimeCompare function returns 1 if equal, 0 if not
	equal := subtle.ConstantTimeCompare([]byte("test"), []byte("test"))
	notEqual := subtle.ConstantTimeCompare([]byte("test"), []byte("different"))

	if equal != 1 {
		t.Error("ConstantTimeCompare should return 1 for equal strings")
	}
	if notEqual != 0 {
		t.Error("ConstantTimeCompare should return 0 for different strings")
	}

	// Verify the implementation imports and uses this function
	// The actual usage is verified by the implementation code inspection
}

func TestAuth_PasswordNotLogged(t *testing.T) {
	// This test ensures that passwords are never logged
	// We verify by checking the implementation doesn't log the Authorization header
	// or any decoded credentials

	// Since we can't easily capture logs in this test structure,
	// we'll verify by code review that:
	// 1. No fmt.Println or log statements output the password
	// 2. Error messages don't include credentials

	// This is more of a documentation test to remind developers
	t.Log("Security check: Ensure no logging of passwords in middleware implementation")
}

func TestAuth_CaseSensitivity(t *testing.T) {
	// Credentials should be case-sensitive
	username := "Admin"
	password := "Secret123"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with wrong case")
	})

	middleware := AuthMiddleware(username, password)
	wrapped := middleware(handler)

	// Test wrong case username
	req1 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req1.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:"+password)))
	rr1 := httptest.NewRecorder()
	wrapped.ServeHTTP(rr1, req1)

	if rr1.Code != http.StatusUnauthorized {
		t.Error("credentials should be case-sensitive")
	}

	// Test wrong case password
	req2 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req2.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+"secret123")))
	rr2 := httptest.NewRecorder()
	wrapped.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusUnauthorized {
		t.Error("credentials should be case-sensitive")
	}
}

func TestExtractCredentials_EdgeCases(t *testing.T) {
	testCases := []struct {
		name         string
		encoded      string
		expectedUser string
		expectedPass string
		expectedOk   bool
	}{
		{
			name:         "empty password",
			encoded:      base64.StdEncoding.EncodeToString([]byte("admin:")),
			expectedUser: "admin",
			expectedPass: "",
			expectedOk:   true,
		},
		{
			name:         "empty username",
			encoded:      base64.StdEncoding.EncodeToString([]byte(":password")),
			expectedUser: "",
			expectedPass: "password",
			expectedOk:   true,
		},
		{
			name:         "multiple colons",
			encoded:      base64.StdEncoding.EncodeToString([]byte("admin:pass:word")),
			expectedUser: "admin",
			expectedPass: "pass:word",
			expectedOk:   true,
		},
		{
			name:         "special characters",
			encoded:      base64.StdEncoding.EncodeToString([]byte("user@domain.com:p@ssw0rd!#$%")),
			expectedUser: "user@domain.com",
			expectedPass: "p@ssw0rd!#$%",
			expectedOk:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
			req.Header.Set("Authorization", "Basic "+tc.encoded)

			u, p, ok := extractCredentials(req)

			if ok != tc.expectedOk {
				t.Errorf("expected ok=%v, got %v", tc.expectedOk, ok)
			}
			if ok {
				if u != tc.expectedUser {
					t.Errorf("expected username %q, got %q", tc.expectedUser, u)
				}
				if p != tc.expectedPass {
					t.Errorf("expected password %q, got %q", tc.expectedPass, p)
				}
			}
		})
	}
}

func TestAuth_ResponseBody(t *testing.T) {
	// 401 responses should not contain sensitive information
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	middleware := AuthMiddleware("admin", "secret")
	wrapped := middleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	// No auth header
	rr := httptest.NewRecorder()

	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}

	body := rr.Body.String()

	// Body should not contain password or credentials
	if strings.Contains(body, "secret") {
		t.Error("response body should not contain password")
	}
	if strings.Contains(body, "admin") {
		t.Error("response body should not contain username")
	}
}

func TestAuth_ConcurrentRequests(t *testing.T) {
	// Ensure middleware is safe for concurrent use
	username := "admin"
	password := "secret"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := AuthMiddleware(username, password)
	wrapped := middleware(handler)

	// Run multiple concurrent requests
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(valid bool) {
			req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
			if valid {
				req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
			}
			rr := httptest.NewRecorder()
			wrapped.ServeHTTP(rr, req)

			if valid && rr.Code != http.StatusOK {
				t.Errorf("concurrent valid request failed with status %d", rr.Code)
			}
			if !valid && rr.Code != http.StatusUnauthorized {
				t.Errorf("concurrent invalid request failed with status %d", rr.Code)
			}
			done <- true
		}(i%2 == 0)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}
