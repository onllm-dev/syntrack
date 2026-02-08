package testutil

import (
	"log/slog"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/config"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
	"github.com/onllm-dev/onwatch/internal/web"
)

// DiscardLogger returns a logger that discards all output.
func DiscardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// InMemoryStore creates an in-memory SQLite store for testing.
// The store is automatically closed when the test completes.
func InMemoryStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("InMemoryStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// InMemoryStoreWithSnapshots creates an in-memory store pre-populated with
// the specified number of Synthetic, Z.ai, and Anthropic snapshots.
func InMemoryStoreWithSnapshots(t *testing.T, synCount, zaiCount, anthCount int) *store.Store {
	t.Helper()
	s := InMemoryStore(t)
	now := time.Now().UTC()

	// Insert Synthetic snapshots
	for i := range synCount {
		capturedAt := now.Add(-time.Duration(synCount-i) * time.Minute)
		renewsAt := now.Add(4 * time.Hour)
		snap := &api.Snapshot{
			CapturedAt: capturedAt,
			Sub:        api.QuotaInfo{Limit: 1350, Requests: 100 + float64(i)*10, RenewsAt: renewsAt},
			Search:     api.QuotaInfo{Limit: 250, Requests: float64(i) * 5, RenewsAt: renewsAt},
			ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 5000 + float64(i)*100, RenewsAt: renewsAt},
		}
		if _, err := s.InsertSnapshot(snap); err != nil {
			t.Fatalf("InMemoryStoreWithSnapshots: insert synthetic: %v", err)
		}
	}

	// Insert Z.ai snapshots
	for i := range zaiCount {
		capturedAt := now.Add(-time.Duration(zaiCount-i) * time.Minute)
		resetTime := now.Add(7 * 24 * time.Hour)
		snap := &api.ZaiSnapshot{
			CapturedAt:         capturedAt,
			TokensUsage:        200000000,
			TokensCurrentValue: 10000000 + float64(i)*5000000,
			TokensNextResetTime: &resetTime,
			TimeUsage:          1000,
			TimeCurrentValue:   10 + float64(i)*3,
		}
		if _, err := s.InsertZaiSnapshot(snap); err != nil {
			t.Fatalf("InMemoryStoreWithSnapshots: insert zai: %v", err)
		}
	}

	// Insert Anthropic snapshots
	for i := range anthCount {
		capturedAt := now.Add(-time.Duration(anthCount-i) * time.Minute)
		fiveHourReset := now.Add(3 * time.Hour)
		sevenDayReset := now.Add(5 * 24 * time.Hour)
		snap := &api.AnthropicSnapshot{
			CapturedAt: capturedAt,
			Quotas: []api.AnthropicQuota{
				{Name: "five_hour", Utilization: 10 + float64(i)*5, ResetsAt: &fiveHourReset},
				{Name: "seven_day", Utilization: 5 + float64(i)*2, ResetsAt: &sevenDayReset},
			},
		}
		if _, err := s.InsertAnthropicSnapshot(snap); err != nil {
			t.Fatalf("InMemoryStoreWithSnapshots: insert anthropic: %v", err)
		}
	}

	return s
}

// TestConfig creates a Config suitable for testing.
// The baseURL is used to set the Synthetic API base URL, Z.ai base URL, etc.
func TestConfig(baseURL string) *config.Config {
	return &config.Config{
		SyntheticAPIKey:    "syn_test_key_12345",
		ZaiAPIKey:          "zai_test_key",
		ZaiBaseURL:         baseURL,
		AnthropicToken:     "anth_test_token",
		PollInterval:       10 * time.Second,
		Port:               9211,
		Host:               "127.0.0.1",
		AdminUser:          "admin",
		AdminPass:          "testpass",
		DBPath:             ":memory:",
		LogLevel:           "debug",
		SessionIdleTimeout: 600 * time.Second,
		DebugMode:          true,
		TestMode:           true,
	}
}

// TestHandler creates a Handler wired up with an in-memory store, discard logger,
// and a test config. Returns the handler and the store for further assertions.
func TestHandler(t *testing.T) (*web.Handler, *store.Store) {
	t.Helper()
	s := InMemoryStore(t)
	logger := DiscardLogger()
	cfg := TestConfig("http://localhost:19212")

	tr := tracker.New(s, logger)
	zaiTr := tracker.NewZaiTracker(s, logger)
	sessions := web.NewSessionStore(cfg.AdminUser, "testhash", s)

	h := web.NewHandler(s, tr, logger, sessions, cfg, zaiTr)
	anthTr := tracker.NewAnthropicTracker(s, logger)
	h.SetAnthropicTracker(anthTr)
	h.SetVersion("test-dev")

	return h, s
}
