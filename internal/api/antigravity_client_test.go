package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAntigravityClient_FetchQuotas_Success(t *testing.T) {
	resetTime := time.Now().Add(24 * time.Hour).Format(time.RFC3339)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/GetUserStatus") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Connect-Protocol-Version") != "1" {
			t.Error("missing Connect-Protocol-Version header")
		}
		if r.Header.Get("X-Codeium-Csrf-Token") != "test-csrf-token" {
			t.Errorf("unexpected CSRF token: %s", r.Header.Get("X-Codeium-Csrf-Token"))
		}

		resp := map[string]interface{}{
			"userStatus": map[string]interface{}{
				"email": "test@example.com",
				"planStatus": map[string]interface{}{
					"availablePromptCredits": 500,
					"planInfo": map[string]interface{}{
						"planName":            "Pro",
						"monthlyPromptCredits": 1000,
					},
				},
				"cascadeModelConfigData": map[string]interface{}{
					"clientModelConfigs": []map[string]interface{}{
						{
							"label":        "Claude Sonnet",
							"modelOrAlias": map[string]string{"model": "claude-4-5-sonnet"},
							"quotaInfo": map[string]interface{}{
								"remainingFraction": 0.75,
								"resetTime":         resetTime,
							},
						},
					},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	conn := &AntigravityConnection{
		BaseURL:   server.URL,
		CSRFToken: "test-csrf-token",
		Port:      0,
		Protocol:  "http",
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	client := NewAntigravityClient(logger, WithAntigravityConnection(conn))

	ctx := context.Background()
	resp, err := client.FetchQuotas(ctx)
	if err != nil {
		t.Fatalf("FetchQuotas failed: %v", err)
	}

	if resp.UserStatus == nil {
		t.Fatal("UserStatus is nil")
	}
	if resp.UserStatus.Email != "test@example.com" {
		t.Errorf("Email = %q, want %q", resp.UserStatus.Email, "test@example.com")
	}

	ids := resp.ActiveModelIDs()
	if len(ids) != 1 {
		t.Fatalf("expected 1 active model, got %d", len(ids))
	}
	if ids[0] != "claude-4-5-sonnet" {
		t.Errorf("ModelID = %q, want %q", ids[0], "claude-4-5-sonnet")
	}
}

func TestAntigravityClient_FetchQuotas_NotAuthenticated(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"message": "User not authenticated",
			"code":    "UNAUTHENTICATED",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	conn := &AntigravityConnection{
		BaseURL:  server.URL,
		Protocol: "http",
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	client := NewAntigravityClient(logger, WithAntigravityConnection(conn))

	ctx := context.Background()
	_, err := client.FetchQuotas(ctx)
	if err == nil {
		t.Fatal("expected error for unauthenticated response")
	}
	if !strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAntigravityClient_FetchQuotas_ServerError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	conn := &AntigravityConnection{
		BaseURL:  server.URL,
		Protocol: "http",
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	client := NewAntigravityClient(logger, WithAntigravityConnection(conn))

	ctx := context.Background()
	_, err := client.FetchQuotas(ctx)
	if err == nil {
		t.Fatal("expected error for server error")
	}
}

func TestAntigravityClient_IsConnected(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// No connection initially
	client := NewAntigravityClient(logger)
	if client.IsConnected() {
		t.Error("expected IsConnected=false initially")
	}

	// With pre-configured connection
	conn := &AntigravityConnection{
		BaseURL:  "https://127.0.0.1:42100",
		Protocol: "https",
	}
	client2 := NewAntigravityClient(logger, WithAntigravityConnection(conn))
	if !client2.IsConnected() {
		t.Error("expected IsConnected=true with pre-configured connection")
	}
}

func TestAntigravityClient_Reset(t *testing.T) {
	conn := &AntigravityConnection{
		BaseURL:  "https://127.0.0.1:42100",
		Protocol: "https",
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	client := NewAntigravityClient(logger, WithAntigravityConnection(conn))

	if !client.IsConnected() {
		t.Error("expected IsConnected=true before reset")
	}

	client.Reset()

	if client.IsConnected() {
		t.Error("expected IsConnected=false after reset")
	}
}

func TestExtractArgument(t *testing.T) {
	tests := []struct {
		name        string
		commandLine string
		argName     string
		expected    string
	}{
		{
			name:        "equals format",
			commandLine: "/path/to/binary --csrf_token=abc123 --other=value",
			argName:     "--csrf_token",
			expected:    "abc123",
		},
		{
			name:        "space format",
			commandLine: "/path/to/binary --csrf_token abc123 --other value",
			argName:     "--csrf_token",
			expected:    "abc123",
		},
		{
			name:        "quoted value",
			commandLine: `/path/to/binary --csrf_token="abc 123" --other=value`,
			argName:     "--csrf_token",
			expected:    "abc 123",
		},
		{
			name:        "not found",
			commandLine: "/path/to/binary --other=value",
			argName:     "--csrf_token",
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractArgument(tt.commandLine, tt.argName)
			if got != tt.expected {
				t.Errorf("extractArgument(%q, %q) = %q, want %q", tt.commandLine, tt.argName, got, tt.expected)
			}
		})
	}
}

func TestExtractPortArgument(t *testing.T) {
	tests := []struct {
		name        string
		commandLine string
		expected    int
	}{
		{
			name:        "valid port",
			commandLine: "--extension_server_port=42100",
			expected:    42100,
		},
		{
			name:        "no port",
			commandLine: "--other=value",
			expected:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPortArgument(tt.commandLine, "--extension_server_port")
			if got != tt.expected {
				t.Errorf("extractPortArgument = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestParsePortsFromLsof(t *testing.T) {
	output := `COMMAND   PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME
language_ 123 user   10u  IPv4  12345      0t0  TCP *:42100 (LISTEN)
language_ 123 user   11u  IPv4  12346      0t0  TCP *:42101 (LISTEN)
`

	ports := parsePortsFromLsof(output)
	if len(ports) != 2 {
		t.Errorf("expected 2 ports, got %d", len(ports))
	}
	if ports[0] != 42100 || ports[1] != 42101 {
		t.Errorf("unexpected ports: %v", ports)
	}
}

func TestParsePortsFromWindowsNetstat(t *testing.T) {
	output := `  TCP    0.0.0.0:42100         0.0.0.0:0              LISTENING       1234
  TCP    0.0.0.0:42101         0.0.0.0:0              LISTENING       1234
  TCP    0.0.0.0:80            0.0.0.0:0              LISTENING       5678
`

	ports := parsePortsFromWindowsNetstat(output, 1234)
	if len(ports) != 2 {
		t.Errorf("expected 2 ports for PID 1234, got %d", len(ports))
	}
}

func TestScoreWindowsCandidate(t *testing.T) {
	tests := []struct {
		name     string
		info     *AntigravityProcessInfo
		minScore int
	}{
		{
			name: "full match",
			info: &AntigravityProcessInfo{
				CommandLine:         "antigravity language_server --lsp",
				CSRFToken:           "token",
				ExtensionServerPort: 42100,
			},
			minScore: 50,
		},
		{
			name: "minimal match",
			info: &AntigravityProcessInfo{
				CommandLine: "antigravity",
			},
			minScore: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := scoreWindowsCandidate(tt.info)
			if score < tt.minScore {
				t.Errorf("score = %d, want >= %d", score, tt.minScore)
			}
		})
	}
}
