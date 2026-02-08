package store

import (
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
)

func TestStore_AnthropicTablesExist(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	var count int
	err = s.db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('anthropic_snapshots', 'anthropic_quota_values', 'anthropic_reset_cycles')",
	).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query tables: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 Anthropic tables, got %d", count)
	}
}

func TestStore_InsertAnthropicSnapshot(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetsAt := now.Add(5 * time.Hour)
	snapshot := &api.AnthropicSnapshot{
		CapturedAt: now,
		RawJSON:    `{"five_hour":{"utilization":0.42}}`,
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 0.42, ResetsAt: &resetsAt},
			{Name: "seven_day", Utilization: 0.15, ResetsAt: nil},
		},
	}

	id, err := s.InsertAnthropicSnapshot(snapshot)
	if err != nil {
		t.Fatalf("InsertAnthropicSnapshot failed: %v", err)
	}
	if id == 0 {
		t.Error("Expected non-zero ID")
	}
}

func TestStore_QueryLatestAnthropic_EmptyDB(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	latest, err := s.QueryLatestAnthropic()
	if err != nil {
		t.Fatalf("QueryLatestAnthropic failed: %v", err)
	}
	if latest != nil {
		t.Error("Expected nil for empty DB")
	}
}

func TestStore_QueryLatestAnthropic_WithData(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetsAt := now.Add(5 * time.Hour)
	snapshot := &api.AnthropicSnapshot{
		CapturedAt: now,
		RawJSON:    `{"five_hour":{"utilization":0.42}}`,
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 0.42, ResetsAt: &resetsAt},
			{Name: "seven_day", Utilization: 0.15, ResetsAt: nil},
		},
	}

	_, err = s.InsertAnthropicSnapshot(snapshot)
	if err != nil {
		t.Fatalf("InsertAnthropicSnapshot failed: %v", err)
	}

	latest, err := s.QueryLatestAnthropic()
	if err != nil {
		t.Fatalf("QueryLatestAnthropic failed: %v", err)
	}
	if latest == nil {
		t.Fatal("Expected latest snapshot, got nil")
	}
	if len(latest.Quotas) != 2 {
		t.Fatalf("Expected 2 quotas, got %d", len(latest.Quotas))
	}
	if latest.Quotas[0].Name != "five_hour" {
		t.Errorf("First quota name = %q, want 'five_hour'", latest.Quotas[0].Name)
	}
	if latest.Quotas[0].Utilization != 0.42 {
		t.Errorf("Utilization = %v, want 0.42", latest.Quotas[0].Utilization)
	}
	if latest.Quotas[0].ResetsAt == nil {
		t.Error("Expected ResetsAt to be set for five_hour")
	}
	if latest.Quotas[1].ResetsAt != nil {
		t.Error("Expected ResetsAt to be nil for seven_day")
	}
	if latest.RawJSON != `{"five_hour":{"utilization":0.42}}` {
		t.Errorf("RawJSON = %q, want original", latest.RawJSON)
	}
}

func TestStore_QueryAnthropicRange(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 2, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		snapshot := &api.AnthropicSnapshot{
			CapturedAt: base.Add(time.Duration(i) * time.Hour),
			RawJSON:    "{}",
			Quotas: []api.AnthropicQuota{
				{Name: "five_hour", Utilization: float64(i) * 0.1},
			},
		}
		_, err := s.InsertAnthropicSnapshot(snapshot)
		if err != nil {
			t.Fatalf("InsertAnthropicSnapshot failed: %v", err)
		}
	}

	// Query middle 3
	start := base.Add(30 * time.Minute)
	end := base.Add(3*time.Hour + 30*time.Minute)
	snapshots, err := s.QueryAnthropicRange(start, end)
	if err != nil {
		t.Fatalf("QueryAnthropicRange failed: %v", err)
	}
	if len(snapshots) != 3 {
		t.Errorf("Expected 3 snapshots, got %d", len(snapshots))
	}

	// Verify quotas are loaded
	for _, snap := range snapshots {
		if len(snap.Quotas) != 1 {
			t.Errorf("Expected 1 quota per snapshot, got %d", len(snap.Quotas))
		}
	}
}

func TestStore_QueryAnthropicRange_WithLimit(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 2, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		snapshot := &api.AnthropicSnapshot{
			CapturedAt: base.Add(time.Duration(i) * time.Hour),
			RawJSON:    "{}",
			Quotas: []api.AnthropicQuota{
				{Name: "five_hour", Utilization: float64(i) * 0.1},
			},
		}
		_, err := s.InsertAnthropicSnapshot(snapshot)
		if err != nil {
			t.Fatalf("InsertAnthropicSnapshot failed: %v", err)
		}
	}

	start := base.Add(-1 * time.Hour)
	end := base.Add(10 * time.Hour)
	snapshots, err := s.QueryAnthropicRange(start, end, 2)
	if err != nil {
		t.Fatalf("QueryAnthropicRange failed: %v", err)
	}
	if len(snapshots) != 2 {
		t.Errorf("Expected 2 snapshots with limit, got %d", len(snapshots))
	}
}

func TestStore_CreateAnthropicCycle(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetsAt := now.Add(5 * time.Hour)

	id, err := s.CreateAnthropicCycle("five_hour", now, &resetsAt)
	if err != nil {
		t.Fatalf("CreateAnthropicCycle failed: %v", err)
	}
	if id == 0 {
		t.Error("Expected non-zero cycle ID")
	}
}

func TestStore_CreateAnthropicCycle_NilResetsAt(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()

	id, err := s.CreateAnthropicCycle("seven_day", now, nil)
	if err != nil {
		t.Fatalf("CreateAnthropicCycle with nil resetsAt failed: %v", err)
	}
	if id == 0 {
		t.Error("Expected non-zero cycle ID")
	}
}

func TestStore_QueryActiveAnthropicCycle(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// No active cycle should return nil
	cycle, err := s.QueryActiveAnthropicCycle("five_hour")
	if err != nil {
		t.Fatalf("QueryActiveAnthropicCycle failed: %v", err)
	}
	if cycle != nil {
		t.Error("Expected nil for no active cycle")
	}

	// Create a cycle
	now := time.Now().UTC()
	resetsAt := now.Add(5 * time.Hour)
	_, err = s.CreateAnthropicCycle("five_hour", now, &resetsAt)
	if err != nil {
		t.Fatalf("CreateAnthropicCycle failed: %v", err)
	}

	// Query active cycle
	cycle, err = s.QueryActiveAnthropicCycle("five_hour")
	if err != nil {
		t.Fatalf("QueryActiveAnthropicCycle failed: %v", err)
	}
	if cycle == nil {
		t.Fatal("Expected active cycle")
	}
	if cycle.QuotaName != "five_hour" {
		t.Errorf("QuotaName = %q, want 'five_hour'", cycle.QuotaName)
	}
	if cycle.ResetsAt == nil {
		t.Error("Expected ResetsAt to be set")
	}
	if cycle.CycleEnd != nil {
		t.Error("Expected CycleEnd to be nil for active cycle")
	}
}

func TestStore_CloseAnthropicCycle(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetsAt := now.Add(5 * time.Hour)
	_, err = s.CreateAnthropicCycle("five_hour", now, &resetsAt)
	if err != nil {
		t.Fatalf("CreateAnthropicCycle failed: %v", err)
	}

	// Close the cycle
	endTime := now.Add(5 * time.Hour)
	err = s.CloseAnthropicCycle("five_hour", endTime, 0.85, 0.42)
	if err != nil {
		t.Fatalf("CloseAnthropicCycle failed: %v", err)
	}

	// Verify no active cycle
	cycle, err := s.QueryActiveAnthropicCycle("five_hour")
	if err != nil {
		t.Fatalf("QueryActiveAnthropicCycle failed: %v", err)
	}
	if cycle != nil {
		t.Error("Expected no active cycle after close")
	}
}

func TestStore_UpdateAnthropicCycle(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetsAt := now.Add(5 * time.Hour)
	_, err = s.CreateAnthropicCycle("five_hour", now, &resetsAt)
	if err != nil {
		t.Fatalf("CreateAnthropicCycle failed: %v", err)
	}

	// Update peak and delta
	err = s.UpdateAnthropicCycle("five_hour", 0.75, 0.30)
	if err != nil {
		t.Fatalf("UpdateAnthropicCycle failed: %v", err)
	}

	// Verify update
	cycle, err := s.QueryActiveAnthropicCycle("five_hour")
	if err != nil {
		t.Fatalf("QueryActiveAnthropicCycle failed: %v", err)
	}
	if cycle == nil {
		t.Fatal("Expected active cycle")
	}
	if cycle.PeakUtilization != 0.75 {
		t.Errorf("PeakUtilization = %v, want 0.75", cycle.PeakUtilization)
	}
	if cycle.TotalDelta != 0.30 {
		t.Errorf("TotalDelta = %v, want 0.30", cycle.TotalDelta)
	}
}

func TestStore_QueryAnthropicCycleHistory(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()

	// Create and close 3 cycles
	for i := 0; i < 3; i++ {
		start := now.Add(time.Duration(i) * 10 * time.Hour)
		resetsAt := start.Add(5 * time.Hour)
		_, err := s.CreateAnthropicCycle("five_hour", start, &resetsAt)
		if err != nil {
			t.Fatalf("CreateAnthropicCycle failed: %v", err)
		}
		endTime := start.Add(5 * time.Hour)
		err = s.CloseAnthropicCycle("five_hour", endTime, float64(i)*0.1+0.5, float64(i)*0.05+0.1)
		if err != nil {
			t.Fatalf("CloseAnthropicCycle failed: %v", err)
		}
	}

	// Query all history
	history, err := s.QueryAnthropicCycleHistory("five_hour")
	if err != nil {
		t.Fatalf("QueryAnthropicCycleHistory failed: %v", err)
	}
	if len(history) != 3 {
		t.Errorf("Expected 3 cycles in history, got %d", len(history))
	}

	// Verify order (DESC by cycle_start)
	if len(history) >= 2 {
		if history[0].CycleStart.Before(history[1].CycleStart) {
			t.Error("Expected history in descending order by cycle_start")
		}
	}

	// Query with limit
	limited, err := s.QueryAnthropicCycleHistory("five_hour", 2)
	if err != nil {
		t.Fatalf("QueryAnthropicCycleHistory with limit failed: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("Expected 2 cycles with limit, got %d", len(limited))
	}
}

func TestStore_QueryAnthropicCycleHistory_NoClosedCycles(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Create an active (unclosed) cycle
	now := time.Now().UTC()
	resetsAt := now.Add(5 * time.Hour)
	_, err = s.CreateAnthropicCycle("five_hour", now, &resetsAt)
	if err != nil {
		t.Fatalf("CreateAnthropicCycle failed: %v", err)
	}

	// History should be empty (only closed cycles)
	history, err := s.QueryAnthropicCycleHistory("five_hour")
	if err != nil {
		t.Fatalf("QueryAnthropicCycleHistory failed: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("Expected 0 cycles in history, got %d", len(history))
	}
}

func TestStore_QueryAnthropicUtilizationSeries(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 2, 6, 12, 0, 0, 0, time.UTC)
	// Insert 5 snapshots at 1-minute intervals with increasing utilization
	for i := 0; i < 5; i++ {
		snapshot := &api.AnthropicSnapshot{
			CapturedAt: base.Add(time.Duration(i) * time.Minute),
			RawJSON:    "{}",
			Quotas: []api.AnthropicQuota{
				{Name: "five_hour", Utilization: float64(i) * 10},
				{Name: "seven_day", Utilization: float64(i) * 2},
			},
		}
		_, err := s.InsertAnthropicSnapshot(snapshot)
		if err != nil {
			t.Fatalf("InsertAnthropicSnapshot failed: %v", err)
		}
	}

	// Query five_hour since base (should get all 5)
	points, err := s.QueryAnthropicUtilizationSeries("five_hour", base)
	if err != nil {
		t.Fatalf("QueryAnthropicUtilizationSeries failed: %v", err)
	}
	if len(points) != 5 {
		t.Errorf("Expected 5 points, got %d", len(points))
	}
	// Verify ordering (ASC) and values
	if len(points) >= 2 {
		if points[0].Utilization != 0 {
			t.Errorf("First point utilization = %v, want 0", points[0].Utilization)
		}
		if points[4].Utilization != 40 {
			t.Errorf("Last point utilization = %v, want 40", points[4].Utilization)
		}
	}

	// Query since base+2min (should get 3 snapshots: idx 2,3,4)
	points, err = s.QueryAnthropicUtilizationSeries("five_hour", base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("QueryAnthropicUtilizationSeries since failed: %v", err)
	}
	if len(points) != 3 {
		t.Errorf("Expected 3 points since +2min, got %d", len(points))
	}

	// Query different quota name
	points, err = s.QueryAnthropicUtilizationSeries("seven_day", base)
	if err != nil {
		t.Fatalf("QueryAnthropicUtilizationSeries seven_day failed: %v", err)
	}
	if len(points) != 5 {
		t.Errorf("Expected 5 seven_day points, got %d", len(points))
	}
	if len(points) >= 1 && points[4].Utilization != 8 {
		t.Errorf("Last seven_day utilization = %v, want 8", points[4].Utilization)
	}

	// Query non-existent quota
	points, err = s.QueryAnthropicUtilizationSeries("nonexistent", base)
	if err != nil {
		t.Fatalf("QueryAnthropicUtilizationSeries nonexistent failed: %v", err)
	}
	if len(points) != 0 {
		t.Errorf("Expected 0 points for nonexistent quota, got %d", len(points))
	}
}

func TestStore_QueryAnthropicCycleOverview_NoCycles(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	rows, err := s.QueryAnthropicCycleOverview("five_hour", 10)
	if err != nil {
		t.Fatalf("QueryAnthropicCycleOverview failed: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("Expected 0 rows, got %d", len(rows))
	}
}

func TestStore_QueryAnthropicCycleOverview_WithData(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 2, 6, 12, 0, 0, 0, time.UTC)
	cycleEnd := base.Add(5 * time.Hour)

	// Create and close a cycle
	_, err = s.CreateAnthropicCycle("five_hour", base, &cycleEnd)
	if err != nil {
		t.Fatalf("CreateAnthropicCycle failed: %v", err)
	}

	// Insert snapshots within the cycle with increasing utilization
	for i := 0; i < 5; i++ {
		snapshot := &api.AnthropicSnapshot{
			CapturedAt: base.Add(time.Duration(i) * time.Hour),
			RawJSON:    "{}",
			Quotas: []api.AnthropicQuota{
				{Name: "five_hour", Utilization: float64(i) * 20},
				{Name: "seven_day", Utilization: float64(i) * 5},
				{Name: "seven_day_sonnet", Utilization: float64(i) * 2},
			},
		}
		_, err := s.InsertAnthropicSnapshot(snapshot)
		if err != nil {
			t.Fatalf("InsertAnthropicSnapshot failed: %v", err)
		}
	}

	// Close the cycle
	err = s.CloseAnthropicCycle("five_hour", cycleEnd, 80, 72)
	if err != nil {
		t.Fatalf("CloseAnthropicCycle failed: %v", err)
	}

	rows, err := s.QueryAnthropicCycleOverview("five_hour", 10)
	if err != nil {
		t.Fatalf("QueryAnthropicCycleOverview failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Expected 1 row, got %d", len(rows))
	}

	row := rows[0]
	if row.QuotaType != "five_hour" {
		t.Errorf("QuotaType = %q, want 'five_hour'", row.QuotaType)
	}
	if row.PeakValue != 80 {
		t.Errorf("PeakValue = %v, want 80", row.PeakValue)
	}

	// Should have 3 cross-quotas from the peak snapshot (i=4, five_hour=80)
	if len(row.CrossQuotas) != 3 {
		t.Fatalf("Expected 3 cross-quotas, got %d", len(row.CrossQuotas))
	}

	// Cross-quotas should be ordered by quota_name (ASC)
	if row.CrossQuotas[0].Name != "five_hour" {
		t.Errorf("First cross-quota = %q, want 'five_hour'", row.CrossQuotas[0].Name)
	}
	if row.CrossQuotas[0].Percent != 80 {
		t.Errorf("five_hour percent = %v, want 80", row.CrossQuotas[0].Percent)
	}
	if row.CrossQuotas[1].Name != "seven_day" {
		t.Errorf("Second cross-quota = %q, want 'seven_day'", row.CrossQuotas[1].Name)
	}
	if row.CrossQuotas[1].Percent != 20 {
		t.Errorf("seven_day percent = %v, want 20", row.CrossQuotas[1].Percent)
	}
	if row.CrossQuotas[2].Name != "seven_day_sonnet" {
		t.Errorf("Third cross-quota = %q, want 'seven_day_sonnet'", row.CrossQuotas[2].Name)
	}
	if row.CrossQuotas[2].Percent != 8 {
		t.Errorf("seven_day_sonnet percent = %v, want 8", row.CrossQuotas[2].Percent)
	}
}

func TestStore_QueryAnthropicCycleOverview_NoSnapshots(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 2, 6, 12, 0, 0, 0, time.UTC)
	cycleEnd := base.Add(5 * time.Hour)

	_, err = s.CreateAnthropicCycle("five_hour", base, &cycleEnd)
	if err != nil {
		t.Fatalf("CreateAnthropicCycle failed: %v", err)
	}
	err = s.CloseAnthropicCycle("five_hour", cycleEnd, 0, 0)
	if err != nil {
		t.Fatalf("CloseAnthropicCycle failed: %v", err)
	}

	rows, err := s.QueryAnthropicCycleOverview("five_hour", 10)
	if err != nil {
		t.Fatalf("QueryAnthropicCycleOverview failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Expected 1 row, got %d", len(rows))
	}
	if len(rows[0].CrossQuotas) != 0 {
		t.Errorf("Expected 0 cross-quotas, got %d", len(rows[0].CrossQuotas))
	}
}

func TestStore_AnthropicForeignKeyConstraint(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Try to insert a quota value with a non-existent snapshot_id
	_, err = s.db.Exec(
		`INSERT INTO anthropic_quota_values (snapshot_id, quota_name, utilization) VALUES (?, ?, ?)`,
		9999, "five_hour", 0.42,
	)
	if err == nil {
		t.Error("Expected foreign key constraint error, got nil")
	}
}
