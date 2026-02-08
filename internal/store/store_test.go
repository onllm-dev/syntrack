package store

import (
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
)

func TestStore_CreateTables(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Verify tables exist by querying
	var count int
	err = s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('quota_snapshots', 'reset_cycles', 'sessions', 'settings')").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query tables: %v", err)
	}
	if count != 4 {
		t.Errorf("Expected 4 tables, got %d", count)
	}
}

func TestStore_WALMode(t *testing.T) {
	// WAL mode doesn't apply to :memory: databases
	// Test with a temp file instead
	tmpFile := t.TempDir() + "/test.db"
	s, err := New(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	var journalMode string
	err = s.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	if err != nil {
		t.Fatalf("Failed to query journal mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("Expected WAL mode, got %s", journalMode)
	}
}

func TestStore_BoundedCache(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	var cacheSize int
	err = s.db.QueryRow("PRAGMA cache_size").Scan(&cacheSize)
	if err != nil {
		t.Fatalf("Failed to query cache size: %v", err)
	}
	// cache_size is negative for KB, -2000 = 2MB
	if cacheSize != -2000 {
		t.Errorf("Expected cache_size -2000, got %d", cacheSize)
	}
}

func TestStore_InsertSnapshot(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub: api.QuotaInfo{
			Limit:    1350,
			Requests: 154.3,
			RenewsAt: time.Date(2026, 2, 6, 16, 16, 18, 0, time.UTC),
		},
		Search: api.QuotaInfo{
			Limit:    250,
			Requests: 0,
			RenewsAt: time.Date(2026, 2, 6, 13, 58, 14, 0, time.UTC),
		},
		ToolCall: api.QuotaInfo{
			Limit:    16200,
			Requests: 7635,
			RenewsAt: time.Date(2026, 2, 6, 15, 26, 41, 0, time.UTC),
		},
	}

	id, err := s.InsertSnapshot(snapshot)
	if err != nil {
		t.Fatalf("InsertSnapshot failed: %v", err)
	}
	if id == 0 {
		t.Error("Expected non-zero ID")
	}

	// Verify it was stored
	latest, err := s.QueryLatest()
	if err != nil {
		t.Fatalf("QueryLatest failed: %v", err)
	}
	if latest == nil {
		t.Fatal("Expected latest snapshot, got nil")
	}
	if latest.Sub.Requests != 154.3 {
		t.Errorf("Sub.Requests = %v, want 154.3", latest.Sub.Requests)
	}
}

func TestStore_QueryLatest_EmptyDB(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	latest, err := s.QueryLatest()
	if err != nil {
		t.Fatalf("QueryLatest failed: %v", err)
	}
	if latest != nil {
		t.Error("Expected nil for empty DB")
	}
}

func TestStore_QueryRange(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Insert multiple snapshots
	base := time.Date(2026, 2, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		snapshot := &api.Snapshot{
			CapturedAt: base.Add(time.Duration(i) * time.Hour),
			Sub:        api.QuotaInfo{Limit: 100, Requests: float64(i * 10), RenewsAt: base},
			Search:     api.QuotaInfo{Limit: 50, Requests: float64(i), RenewsAt: base},
			ToolCall:   api.QuotaInfo{Limit: 200, Requests: float64(i * 5), RenewsAt: base},
		}
		_, err := s.InsertSnapshot(snapshot)
		if err != nil {
			t.Fatalf("InsertSnapshot failed: %v", err)
		}
	}

	// Query middle 3
	start := base.Add(30 * time.Minute)
	end := base.Add(3*time.Hour + 30*time.Minute)
	snapshots, err := s.QueryRange(start, end)
	if err != nil {
		t.Fatalf("QueryRange failed: %v", err)
	}
	if len(snapshots) != 3 {
		t.Errorf("Expected 3 snapshots, got %d", len(snapshots))
	}
}

func TestStore_QueryRange_Empty(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Insert a snapshot
	snapshot := &api.Snapshot{
		CapturedAt: time.Now(),
		Sub:        api.QuotaInfo{Limit: 100, Requests: 50, RenewsAt: time.Now()},
		Search:     api.QuotaInfo{Limit: 50, Requests: 10, RenewsAt: time.Now()},
		ToolCall:   api.QuotaInfo{Limit: 200, Requests: 100, RenewsAt: time.Now()},
	}
	_, err = s.InsertSnapshot(snapshot)
	if err != nil {
		t.Fatalf("InsertSnapshot failed: %v", err)
	}

	// Query range with no data
	start := time.Now().Add(-2 * time.Hour)
	end := time.Now().Add(-1 * time.Hour)
	snapshots, err := s.QueryRange(start, end)
	if err != nil {
		t.Fatalf("QueryRange failed: %v", err)
	}
	if len(snapshots) != 0 {
		t.Errorf("Expected 0 snapshots, got %d", len(snapshots))
	}
}

func TestStore_CreateAndCloseSession(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	sessionID := "test-session-123"
	startedAt := time.Now().UTC()

	err = s.CreateSession(sessionID, startedAt, 60, "synthetic")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Verify active session exists
	active, err := s.QueryActiveSession()
	if err != nil {
		t.Fatalf("QueryActiveSession failed: %v", err)
	}
	if active == nil {
		t.Fatal("Expected active session")
	}
	if active.ID != sessionID {
		t.Errorf("Session ID = %q, want %q", active.ID, sessionID)
	}

	// Close session
	endedAt := startedAt.Add(2 * time.Hour)
	err = s.CloseSession(sessionID, endedAt)
	if err != nil {
		t.Fatalf("CloseSession failed: %v", err)
	}

	// Verify no active session
	active, err = s.QueryActiveSession()
	if err != nil {
		t.Fatalf("QueryActiveSession failed: %v", err)
	}
	if active != nil {
		t.Error("Expected no active session after close")
	}
}

func TestStore_UpdateSessionMaxRequests(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	sessionID := "test-session"
	err = s.CreateSession(sessionID, time.Now(), 60, "synthetic")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Update with increasing values
	updates := []struct {
		sub, search, tool float64
	}{
		{100, 10, 50},
		{200, 20, 100},
		{150, 15, 75}, // Should not decrease max
	}

	for _, u := range updates {
		err = s.UpdateSessionMaxRequests(sessionID, u.sub, u.search, u.tool)
		if err != nil {
			t.Fatalf("UpdateSessionMaxRequests failed: %v", err)
		}
	}

	// Query and verify max values
	sessions, err := s.QuerySessionHistory()
	if err != nil {
		t.Fatalf("QuerySessionHistory failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}

	// Max should be the highest value seen (200, 20, 100)
	if sessions[0].MaxSubRequests != 200 {
		t.Errorf("MaxSubRequests = %v, want 200", sessions[0].MaxSubRequests)
	}
	if sessions[0].MaxSearchRequests != 20 {
		t.Errorf("MaxSearchRequests = %v, want 20", sessions[0].MaxSearchRequests)
	}
	if sessions[0].MaxToolRequests != 100 {
		t.Errorf("MaxToolRequests = %v, want 100", sessions[0].MaxToolRequests)
	}
}

func TestStore_IncrementSnapshotCount(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	sessionID := "test-session"
	err = s.CreateSession(sessionID, time.Now(), 60, "synthetic")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Increment multiple times
	for i := 0; i < 5; i++ {
		err = s.IncrementSnapshotCount(sessionID)
		if err != nil {
			t.Fatalf("IncrementSnapshotCount failed: %v", err)
		}
	}

	sessions, err := s.QuerySessionHistory()
	if err != nil {
		t.Fatalf("QuerySessionHistory failed: %v", err)
	}
	if sessions[0].SnapshotCount != 5 {
		t.Errorf("SnapshotCount = %d, want 5", sessions[0].SnapshotCount)
	}
}

func TestStore_CreateAndCloseCycle(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	tests := []struct {
		quotaType string
		renewsAt  time.Time
	}{
		{"subscription", time.Date(2026, 2, 6, 16, 0, 0, 0, time.UTC)},
		{"search", time.Date(2026, 2, 6, 13, 0, 0, 0, time.UTC)},
		{"toolcall", time.Date(2026, 2, 6, 15, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		start := time.Now().UTC()
		cycleID, err := s.CreateCycle(tt.quotaType, start, tt.renewsAt)
		if err != nil {
			t.Fatalf("CreateCycle failed for %s: %v", tt.quotaType, err)
		}
		if cycleID == 0 {
			t.Errorf("Expected non-zero cycle ID for %s", tt.quotaType)
		}
	}

	// Query active cycles
	for _, tt := range tests {
		cycle, err := s.QueryActiveCycle(tt.quotaType)
		if err != nil {
			t.Fatalf("QueryActiveCycle failed for %s: %v", tt.quotaType, err)
		}
		if cycle == nil {
			t.Errorf("Expected active cycle for %s", tt.quotaType)
			continue
		}
		if cycle.QuotaType != tt.quotaType {
			t.Errorf("QuotaType = %q, want %q", cycle.QuotaType, tt.quotaType)
		}
	}

	// Close one cycle
	err = s.CloseCycle("subscription", time.Now().UTC(), 500, 450)
	if err != nil {
		t.Fatalf("CloseCycle failed: %v", err)
	}

	// Verify it's closed
	cycle, err := s.QueryActiveCycle("subscription")
	if err != nil {
		t.Fatalf("QueryActiveCycle failed: %v", err)
	}
	if cycle != nil {
		t.Error("Expected no active subscription cycle after close")
	}

	// Verify it appears in history
	history, err := s.QueryCycleHistory("subscription")
	if err != nil {
		t.Fatalf("QueryCycleHistory failed: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("Expected 1 cycle in history, got %d", len(history))
	}
	if history[0].PeakRequests != 500 {
		t.Errorf("PeakRequests = %v, want 500", history[0].PeakRequests)
	}
	if history[0].TotalDelta != 450 {
		t.Errorf("TotalDelta = %v, want 450", history[0].TotalDelta)
	}
}

func TestStore_MultipleInserts(t *testing.T) {
	// The real app uses serialized access from a single agent
	// This test verifies multiple sequential inserts work correctly
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Insert multiple snapshots sequentially
	for i := 0; i < 10; i++ {
		snapshot := &api.Snapshot{
			CapturedAt: time.Now().Add(time.Duration(i) * time.Second),
			Sub:        api.QuotaInfo{Limit: 100, Requests: float64(i * 10), RenewsAt: time.Now()},
			Search:     api.QuotaInfo{Limit: 50, Requests: float64(i), RenewsAt: time.Now()},
			ToolCall:   api.QuotaInfo{Limit: 200, Requests: float64(i * 5), RenewsAt: time.Now()},
		}
		_, err := s.InsertSnapshot(snapshot)
		if err != nil {
			t.Fatalf("Insert %d failed: %v", i, err)
		}
	}

	// Verify all 10 were inserted
	start := time.Now().Add(-1 * time.Hour)
	end := time.Now().Add(1 * time.Hour)
	snapshots, err := s.QueryRange(start, end)
	if err != nil {
		t.Fatalf("QueryRange failed: %v", err)
	}
	if len(snapshots) != 10 {
		t.Errorf("Expected 10 snapshots, got %d", len(snapshots))
	}
}

func TestStore_GetSetting_NotFound(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	val, err := s.GetSetting("nonexistent")
	if err != nil {
		t.Fatalf("GetSetting failed: %v", err)
	}
	if val != "" {
		t.Errorf("Expected empty string for missing key, got %q", val)
	}
}

func TestStore_SetAndGetSetting(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Set a value
	err = s.SetSetting("timezone", "America/New_York")
	if err != nil {
		t.Fatalf("SetSetting failed: %v", err)
	}

	// Get it back
	val, err := s.GetSetting("timezone")
	if err != nil {
		t.Fatalf("GetSetting failed: %v", err)
	}
	if val != "America/New_York" {
		t.Errorf("Expected 'America/New_York', got %q", val)
	}

	// Overwrite
	err = s.SetSetting("timezone", "Europe/London")
	if err != nil {
		t.Fatalf("SetSetting overwrite failed: %v", err)
	}

	val, err = s.GetSetting("timezone")
	if err != nil {
		t.Fatalf("GetSetting after overwrite failed: %v", err)
	}
	if val != "Europe/London" {
		t.Errorf("Expected 'Europe/London', got %q", val)
	}
}

func TestStore_QuerySyntheticCycleOverview_NoCycles(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	rows, err := s.QuerySyntheticCycleOverview("subscription", 10)
	if err != nil {
		t.Fatalf("QuerySyntheticCycleOverview failed: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("Expected 0 rows, got %d", len(rows))
	}
}

func TestStore_QuerySyntheticCycleOverview_WithData(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 2, 6, 12, 0, 0, 0, time.UTC)

	// Create a cycle
	_, err = s.CreateCycle("subscription", base, base.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("CreateCycle failed: %v", err)
	}

	// Insert snapshots within the cycle
	for i := 0; i < 5; i++ {
		snapshot := &api.Snapshot{
			CapturedAt: base.Add(time.Duration(i) * time.Hour),
			Sub:        api.QuotaInfo{Limit: 1350, Requests: float64(i * 100), RenewsAt: base.Add(24 * time.Hour)},
			Search:     api.QuotaInfo{Limit: 250, Requests: float64(i * 10), RenewsAt: base.Add(time.Hour)},
			ToolCall:   api.QuotaInfo{Limit: 16200, Requests: float64(i * 500), RenewsAt: base.Add(24 * time.Hour)},
		}
		_, err := s.InsertSnapshot(snapshot)
		if err != nil {
			t.Fatalf("InsertSnapshot failed: %v", err)
		}
	}

	// Close the cycle
	err = s.CloseCycle("subscription", base.Add(5*time.Hour), 400, 380)
	if err != nil {
		t.Fatalf("CloseCycle failed: %v", err)
	}

	rows, err := s.QuerySyntheticCycleOverview("subscription", 10)
	if err != nil {
		t.Fatalf("QuerySyntheticCycleOverview failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Expected 1 row, got %d", len(rows))
	}

	row := rows[0]
	if row.QuotaType != "subscription" {
		t.Errorf("QuotaType = %q, want 'subscription'", row.QuotaType)
	}
	if row.PeakValue != 400 {
		t.Errorf("PeakValue = %v, want 400", row.PeakValue)
	}
	if row.TotalDelta != 380 {
		t.Errorf("TotalDelta = %v, want 380", row.TotalDelta)
	}
	if len(row.CrossQuotas) != 3 {
		t.Fatalf("Expected 3 cross-quotas, got %d", len(row.CrossQuotas))
	}

	// The peak snapshot should be the one at i=4 (sub_requests=400)
	subEntry := row.CrossQuotas[0]
	if subEntry.Name != "subscription" {
		t.Errorf("First cross-quota name = %q, want 'subscription'", subEntry.Name)
	}
	if subEntry.Value != 400 {
		t.Errorf("Subscription value = %v, want 400", subEntry.Value)
	}
	if subEntry.Limit != 1350 {
		t.Errorf("Subscription limit = %v, want 1350", subEntry.Limit)
	}
	// Check percent is approximately correct
	expectedPct := 400.0 / 1350.0 * 100
	if subEntry.Percent < expectedPct-0.1 || subEntry.Percent > expectedPct+0.1 {
		t.Errorf("Subscription percent = %v, want ~%v", subEntry.Percent, expectedPct)
	}

	// Verify search and toolcall are also present
	if row.CrossQuotas[1].Name != "search" {
		t.Errorf("Second cross-quota name = %q, want 'search'", row.CrossQuotas[1].Name)
	}
	if row.CrossQuotas[2].Name != "toolcall" {
		t.Errorf("Third cross-quota name = %q, want 'toolcall'", row.CrossQuotas[2].Name)
	}
}

func TestStore_QuerySyntheticCycleOverview_NoSnapshots(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 2, 6, 12, 0, 0, 0, time.UTC)

	// Create and close a cycle without any snapshots
	_, err = s.CreateCycle("subscription", base, base.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("CreateCycle failed: %v", err)
	}
	err = s.CloseCycle("subscription", base.Add(5*time.Hour), 0, 0)
	if err != nil {
		t.Fatalf("CloseCycle failed: %v", err)
	}

	rows, err := s.QuerySyntheticCycleOverview("subscription", 10)
	if err != nil {
		t.Fatalf("QuerySyntheticCycleOverview failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Expected 1 row, got %d", len(rows))
	}
	// No snapshots means empty CrossQuotas
	if len(rows[0].CrossQuotas) != 0 {
		t.Errorf("Expected 0 cross-quotas, got %d", len(rows[0].CrossQuotas))
	}
}
