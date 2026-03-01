package store

import (
	"database/sql"
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
	// cache_size is negative for KB, -500 = 512KB
	if cacheSize != -500 {
		t.Errorf("Expected cache_size -500, got %d", cacheSize)
	}
}

func TestStore_AntigravityActiveCycleUniqueIndexExists(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	rows, err := s.db.Query("PRAGMA index_list('antigravity_reset_cycles')")
	if err != nil {
		t.Fatalf("failed to query index list: %v", err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var seq int
		var name string
		var unique int
		var origin string
		var partial int
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("failed to scan index row: %v", err)
		}
		if name == "idx_antigravity_cycles_model_active_unique" {
			found = true
			if unique != 1 {
				t.Fatalf("expected index %s to be unique", name)
			}
			if partial != 1 {
				t.Fatalf("expected index %s to be partial", name)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("index list iteration failed: %v", err)
	}
	if !found {
		t.Fatal("expected idx_antigravity_cycles_model_active_unique index to exist")
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

func TestStore_QueryRange_WithLimitReturnsLatestChronological(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 2, 27, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		snapshot := &api.Snapshot{
			CapturedAt: base.Add(time.Duration(i) * time.Minute),
			Sub:        api.QuotaInfo{Limit: 100, Requests: float64(i), RenewsAt: base},
			Search:     api.QuotaInfo{Limit: 50, Requests: float64(i), RenewsAt: base},
			ToolCall:   api.QuotaInfo{Limit: 200, Requests: float64(i), RenewsAt: base},
		}
		if _, err := s.InsertSnapshot(snapshot); err != nil {
			t.Fatalf("InsertSnapshot[%d] failed: %v", i, err)
		}
	}

	snapshots, err := s.QueryRange(base.Add(-time.Minute), base.Add(10*time.Minute), 2)
	if err != nil {
		t.Fatalf("QueryRange with limit failed: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snapshots))
	}

	if !snapshots[0].CapturedAt.Equal(base.Add(3 * time.Minute)) {
		t.Fatalf("expected first limited snapshot at t+3m, got %s", snapshots[0].CapturedAt)
	}
	if !snapshots[1].CapturedAt.Equal(base.Add(4 * time.Minute)) {
		t.Fatalf("expected second limited snapshot at t+4m, got %s", snapshots[1].CapturedAt)
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

func TestStore_NotificationLog_TableExists(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	var count int
	err = s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='notification_log'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query tables: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected notification_log table to exist, got count=%d", count)
	}
}

func TestStore_UpsertNotificationLog_Insert(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	err = s.UpsertNotificationLog("anthropic", "five_hour", "threshold_80", 82.5)
	if err != nil {
		t.Fatalf("UpsertNotificationLog failed: %v", err)
	}

	sentAt, util, err := s.GetLastNotification("anthropic", "five_hour", "threshold_80")
	if err != nil {
		t.Fatalf("GetLastNotification failed: %v", err)
	}
	if sentAt.IsZero() {
		t.Error("Expected non-zero sentAt")
	}
	if util != 82.5 {
		t.Errorf("utilization = %v, want 82.5", util)
	}
}

func TestStore_UpsertNotificationLog_Upsert(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Insert first
	err = s.UpsertNotificationLog("anthropic", "five_hour", "threshold_80", 80.0)
	if err != nil {
		t.Fatalf("First upsert failed: %v", err)
	}

	sentAt1, _, err := s.GetLastNotification("anthropic", "five_hour", "threshold_80")
	if err != nil {
		t.Fatalf("GetLastNotification failed: %v", err)
	}

	// Upsert (replace)
	// Small delay to ensure different timestamp
	time.Sleep(10 * time.Millisecond)
	err = s.UpsertNotificationLog("anthropic", "five_hour", "threshold_80", 85.0)
	if err != nil {
		t.Fatalf("Second upsert failed: %v", err)
	}

	sentAt2, util, err := s.GetLastNotification("anthropic", "five_hour", "threshold_80")
	if err != nil {
		t.Fatalf("GetLastNotification after upsert failed: %v", err)
	}

	if !sentAt2.After(sentAt1) {
		t.Errorf("Expected sentAt2 (%v) > sentAt1 (%v)", sentAt2, sentAt1)
	}
	if util != 85.0 {
		t.Errorf("utilization = %v, want 85.0", util)
	}
}

func TestStore_GetLastNotification_NotFound(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	sentAt, util, err := s.GetLastNotification("anthropic", "nonexistent", "threshold_80")
	if err != nil {
		t.Fatalf("GetLastNotification failed: %v", err)
	}
	if !sentAt.IsZero() {
		t.Errorf("Expected zero time for missing entry, got %v", sentAt)
	}
	if util != 0 {
		t.Errorf("Expected 0 utilization for missing entry, got %v", util)
	}
}

func TestStore_UpsertNotificationLog_DifferentKeys(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Insert for different quota keys
	err = s.UpsertNotificationLog("anthropic", "five_hour", "threshold_80", 80.0)
	if err != nil {
		t.Fatalf("Upsert five_hour failed: %v", err)
	}
	err = s.UpsertNotificationLog("anthropic", "seven_day", "threshold_80", 81.0)
	if err != nil {
		t.Fatalf("Upsert seven_day failed: %v", err)
	}
	err = s.UpsertNotificationLog("anthropic", "five_hour", "threshold_95", 95.0)
	if err != nil {
		t.Fatalf("Upsert five_hour threshold_95 failed: %v", err)
	}

	// Each should be independent
	_, util1, _ := s.GetLastNotification("anthropic", "five_hour", "threshold_80")
	_, util2, _ := s.GetLastNotification("anthropic", "seven_day", "threshold_80")
	_, util3, _ := s.GetLastNotification("anthropic", "five_hour", "threshold_95")

	if util1 != 80.0 {
		t.Errorf("five_hour/threshold_80 util = %v, want 80.0", util1)
	}
	if util2 != 81.0 {
		t.Errorf("seven_day/threshold_80 util = %v, want 81.0", util2)
	}
	if util3 != 95.0 {
		t.Errorf("five_hour/threshold_95 util = %v, want 95.0", util3)
	}
}

func TestStore_NotificationLog_ProviderScoped(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertNotificationLog("anthropic", "five_hour", "threshold_80", 80.0); err != nil {
		t.Fatalf("Upsert anthropic failed: %v", err)
	}
	if err := s.UpsertNotificationLog("codex", "five_hour", "threshold_80", 90.0); err != nil {
		t.Fatalf("Upsert codex failed: %v", err)
	}

	_, anthropicUtil, err := s.GetLastNotification("anthropic", "five_hour", "threshold_80")
	if err != nil {
		t.Fatalf("GetLastNotification anthropic failed: %v", err)
	}
	_, codexUtil, err := s.GetLastNotification("codex", "five_hour", "threshold_80")
	if err != nil {
		t.Fatalf("GetLastNotification codex failed: %v", err)
	}

	if anthropicUtil != 80.0 {
		t.Errorf("anthropic util = %v, want 80.0", anthropicUtil)
	}
	if codexUtil != 90.0 {
		t.Errorf("codex util = %v, want 90.0", codexUtil)
	}
}

func TestStore_ClearNotificationLog_ProviderScoped(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertNotificationLog("anthropic", "five_hour", "warning", 80.0); err != nil {
		t.Fatalf("Upsert anthropic failed: %v", err)
	}
	if err := s.UpsertNotificationLog("codex", "five_hour", "warning", 90.0); err != nil {
		t.Fatalf("Upsert codex failed: %v", err)
	}

	if err := s.ClearNotificationLog("anthropic", "five_hour"); err != nil {
		t.Fatalf("ClearNotificationLog anthropic failed: %v", err)
	}

	anthropicSentAt, _, err := s.GetLastNotification("anthropic", "five_hour", "warning")
	if err != nil {
		t.Fatalf("GetLastNotification anthropic failed: %v", err)
	}
	codexSentAt, _, err := s.GetLastNotification("codex", "five_hour", "warning")
	if err != nil {
		t.Fatalf("GetLastNotification codex failed: %v", err)
	}

	if !anthropicSentAt.IsZero() {
		t.Errorf("expected anthropic entry to be cleared, got sentAt=%v", anthropicSentAt)
	}
	if codexSentAt.IsZero() {
		t.Error("expected codex entry to remain after anthropic clear")
	}
}

func TestStore_NotificationLog_MigratesLegacySchema(t *testing.T) {
	dbPath := t.TempDir() + "/legacy-notification.db"

	legacyDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open legacy DB: %v", err)
	}

	if _, err := legacyDB.Exec(`
		CREATE TABLE notification_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			quota_key TEXT NOT NULL,
			notification_type TEXT NOT NULL,
			sent_at TEXT NOT NULL,
			utilization REAL,
			UNIQUE(quota_key, notification_type)
		)
	`); err != nil {
		t.Fatalf("Failed to create legacy notification_log: %v", err)
	}

	if _, err := legacyDB.Exec(`
		INSERT INTO notification_log (quota_key, notification_type, sent_at, utilization)
		VALUES ('five_hour', 'warning', ?, 81.5)
	`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("Failed to insert legacy row: %v", err)
	}

	if err := legacyDB.Close(); err != nil {
		t.Fatalf("Failed to close legacy DB: %v", err)
	}

	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to open migrated store: %v", err)
	}
	defer s.Close()

	var providerColCount int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('notification_log') WHERE name = 'provider'
	`).Scan(&providerColCount); err != nil {
		t.Fatalf("Failed to check provider column: %v", err)
	}
	if providerColCount != 1 {
		t.Fatalf("Expected provider column after migration, got count=%d", providerColCount)
	}

	legacySentAt, legacyUtil, err := s.GetLastNotification("legacy", "five_hour", "warning")
	if err != nil {
		t.Fatalf("GetLastNotification legacy failed: %v", err)
	}
	if legacySentAt.IsZero() {
		t.Fatal("Expected migrated legacy notification row")
	}
	if legacyUtil != 81.5 {
		t.Fatalf("legacy util = %v, want 81.5", legacyUtil)
	}

	if err := s.UpsertNotificationLog("anthropic", "five_hour", "warning", 88.0); err != nil {
		t.Fatalf("UpsertNotificationLog anthropic failed: %v", err)
	}
	_, anthropicUtil, err := s.GetLastNotification("anthropic", "five_hour", "warning")
	if err != nil {
		t.Fatalf("GetLastNotification anthropic failed: %v", err)
	}
	if anthropicUtil != 88.0 {
		t.Fatalf("anthropic util = %v, want 88.0", anthropicUtil)
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

// --- Auth Token Lifecycle Tests ---

func TestStore_SaveAuthToken_RoundTrip(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	token := "test-token-abc123"
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Millisecond)

	err = s.SaveAuthToken(token, expiresAt)
	if err != nil {
		t.Fatalf("SaveAuthToken failed: %v", err)
	}

	gotExpiry, found, err := s.GetAuthTokenExpiry(token)
	if err != nil {
		t.Fatalf("GetAuthTokenExpiry failed: %v", err)
	}
	if !found {
		t.Fatal("Expected token to be found")
	}
	// Compare to second precision (RFC3339Nano round-trip may lose sub-nanosecond)
	if gotExpiry.Unix() != expiresAt.Unix() {
		t.Errorf("Expiry = %v, want %v", gotExpiry, expiresAt)
	}
}

func TestStore_GetAuthTokenExpiry_NotFound(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	expiry, found, err := s.GetAuthTokenExpiry("nonexistent-token")
	if err != nil {
		t.Fatalf("GetAuthTokenExpiry failed: %v", err)
	}
	if found {
		t.Error("Expected token not to be found")
	}
	if !expiry.IsZero() {
		t.Errorf("Expected zero time, got %v", expiry)
	}
}

func TestStore_DeleteAllAuthTokens(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Insert multiple tokens
	tokens := []string{"token-1", "token-2", "token-3"}
	for _, tok := range tokens {
		err := s.SaveAuthToken(tok, time.Now().UTC().Add(24*time.Hour))
		if err != nil {
			t.Fatalf("SaveAuthToken failed for %s: %v", tok, err)
		}
	}

	// Verify they exist
	for _, tok := range tokens {
		_, found, err := s.GetAuthTokenExpiry(tok)
		if err != nil {
			t.Fatalf("GetAuthTokenExpiry failed: %v", err)
		}
		if !found {
			t.Fatalf("Token %s should exist before delete", tok)
		}
	}

	// Delete all
	err = s.DeleteAllAuthTokens()
	if err != nil {
		t.Fatalf("DeleteAllAuthTokens failed: %v", err)
	}

	// Verify all are gone
	for _, tok := range tokens {
		_, found, err := s.GetAuthTokenExpiry(tok)
		if err != nil {
			t.Fatalf("GetAuthTokenExpiry failed after delete: %v", err)
		}
		if found {
			t.Errorf("Token %s should not exist after DeleteAllAuthTokens", tok)
		}
	}
}

func TestStore_CleanExpiredAuthTokens(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()

	// Insert expired token (1 hour ago)
	err = s.SaveAuthToken("expired-token", now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("SaveAuthToken (expired) failed: %v", err)
	}

	// Insert valid token (24 hours from now)
	err = s.SaveAuthToken("valid-token", now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("SaveAuthToken (valid) failed: %v", err)
	}

	// Clean expired
	err = s.CleanExpiredAuthTokens()
	if err != nil {
		t.Fatalf("CleanExpiredAuthTokens failed: %v", err)
	}

	// Expired token should be gone
	_, found, err := s.GetAuthTokenExpiry("expired-token")
	if err != nil {
		t.Fatalf("GetAuthTokenExpiry failed: %v", err)
	}
	if found {
		t.Error("Expired token should have been cleaned")
	}

	// Valid token should still exist
	_, found, err = s.GetAuthTokenExpiry("valid-token")
	if err != nil {
		t.Fatalf("GetAuthTokenExpiry failed: %v", err)
	}
	if !found {
		t.Error("Valid token should still exist after cleaning")
	}
}

// --- User CRUD Tests ---

func TestStore_UpsertUser_Create(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	err = s.UpsertUser("admin", "hash-abc123")
	if err != nil {
		t.Fatalf("UpsertUser failed: %v", err)
	}

	hash, err := s.GetUser("admin")
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	if hash != "hash-abc123" {
		t.Errorf("password_hash = %q, want %q", hash, "hash-abc123")
	}
}

func TestStore_UpsertUser_Update(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Create user
	err = s.UpsertUser("admin", "old-hash")
	if err != nil {
		t.Fatalf("UpsertUser (create) failed: %v", err)
	}

	// Update password
	err = s.UpsertUser("admin", "new-hash")
	if err != nil {
		t.Fatalf("UpsertUser (update) failed: %v", err)
	}

	hash, err := s.GetUser("admin")
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	if hash != "new-hash" {
		t.Errorf("password_hash = %q, want %q", hash, "new-hash")
	}
}

func TestStore_GetUser_NotFound(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	hash, err := s.GetUser("nonexistent")
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	if hash != "" {
		t.Errorf("Expected empty string for non-existent user, got %q", hash)
	}
}

// --- Session Tests ---

func TestStore_CreateSession_WithStartValues(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	sessionID := "session-start-vals"
	startedAt := time.Now().UTC()

	err = s.CreateSession(sessionID, startedAt, 60, "synthetic", 100.0, 50.0, 200.0)
	if err != nil {
		t.Fatalf("CreateSession with start values failed: %v", err)
	}

	sessions, err := s.QuerySessionHistory("synthetic")
	if err != nil {
		t.Fatalf("QuerySessionHistory failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}
	if sessions[0].StartSubRequests != 100.0 {
		t.Errorf("StartSubRequests = %v, want 100.0", sessions[0].StartSubRequests)
	}
	if sessions[0].StartSearchRequests != 50.0 {
		t.Errorf("StartSearchRequests = %v, want 50.0", sessions[0].StartSearchRequests)
	}
	if sessions[0].StartToolRequests != 200.0 {
		t.Errorf("StartToolRequests = %v, want 200.0", sessions[0].StartToolRequests)
	}
}

func TestStore_CloseOrphanedSessions(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()

	// Create two open sessions
	err = s.CreateSession("orphan-1", now.Add(-2*time.Hour), 60, "synthetic")
	if err != nil {
		t.Fatalf("CreateSession 1 failed: %v", err)
	}
	err = s.CreateSession("orphan-2", now.Add(-1*time.Hour), 60, "zai")
	if err != nil {
		t.Fatalf("CreateSession 2 failed: %v", err)
	}

	// Close one normally
	err = s.CloseSession("orphan-1", now)
	if err != nil {
		t.Fatalf("CloseSession failed: %v", err)
	}

	// Close orphaned (should only affect orphan-2)
	closed, err := s.CloseOrphanedSessions()
	if err != nil {
		t.Fatalf("CloseOrphanedSessions failed: %v", err)
	}
	if closed != 1 {
		t.Errorf("Expected 1 orphaned session closed, got %d", closed)
	}

	// Verify no active sessions remain
	active, err := s.QueryActiveSession()
	if err != nil {
		t.Fatalf("QueryActiveSession failed: %v", err)
	}
	if active != nil {
		t.Error("Expected no active sessions after closing orphans")
	}
}

// --- QueryCyclesSince Test ---

func TestStore_QueryCyclesSince(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Create 3 cycles at different times
	for i := 0; i < 3; i++ {
		start := base.Add(time.Duration(i) * 24 * time.Hour)
		renewsAt := start.Add(12 * time.Hour)
		_, err := s.CreateCycle("subscription", start, renewsAt)
		if err != nil {
			t.Fatalf("CreateCycle %d failed: %v", i, err)
		}
		// Close immediately (for history)
		err = s.CloseCycle("subscription", start.Add(6*time.Hour), float64(i*100), float64(i*50))
		if err != nil {
			t.Fatalf("CloseCycle %d failed: %v", i, err)
		}
	}

	// Query since day 1 (should get cycles at day 1 and day 2, not day 0)
	since := base.Add(24 * time.Hour)
	cycles, err := s.QueryCyclesSince("subscription", since)
	if err != nil {
		t.Fatalf("QueryCyclesSince failed: %v", err)
	}
	if len(cycles) != 2 {
		t.Errorf("Expected 2 cycles since day 1, got %d", len(cycles))
	}

	// Verify descending order
	if len(cycles) >= 2 && cycles[0].CycleStart.Before(cycles[1].CycleStart) {
		t.Error("Expected cycles in descending order by cycle_start")
	}
}
