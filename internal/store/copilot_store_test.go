package store

import (
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
)

func newTestCopilotSnapshot(capturedAt time.Time, resetDate *time.Time) *api.CopilotSnapshot {
	return &api.CopilotSnapshot{
		CapturedAt:  capturedAt,
		CopilotPlan: "individual_pro",
		ResetDate:   resetDate,
		RawJSON:     `{"test": true}`,
		Quotas: []api.CopilotQuota{
			{Name: "chat", Entitlement: 0, Remaining: 0, PercentRemaining: 100.0, Unlimited: true},
			{Name: "premium_interactions", Entitlement: 1500, Remaining: 473, PercentRemaining: 31.578, Unlimited: false},
		},
	}
}

func TestCopilotStore_InsertAndQueryLatest(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetDate := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	snap := newTestCopilotSnapshot(now, &resetDate)

	id, err := s.InsertCopilotSnapshot(snap)
	if err != nil {
		t.Fatalf("InsertCopilotSnapshot: %v", err)
	}
	if id <= 0 {
		t.Errorf("Expected positive ID, got %d", id)
	}

	latest, err := s.QueryLatestCopilot()
	if err != nil {
		t.Fatalf("QueryLatestCopilot: %v", err)
	}
	if latest == nil {
		t.Fatal("QueryLatestCopilot returned nil")
	}
	if latest.CopilotPlan != "individual_pro" {
		t.Errorf("CopilotPlan = %q, want individual_pro", latest.CopilotPlan)
	}
	if latest.ResetDate == nil {
		t.Fatal("ResetDate should not be nil")
	}
	if len(latest.Quotas) != 2 {
		t.Fatalf("Quotas len = %d, want 2", len(latest.Quotas))
	}
	// Quotas are ordered by name: chat, premium_interactions
	if latest.Quotas[0].Name != "chat" {
		t.Errorf("Quotas[0].Name = %q, want chat", latest.Quotas[0].Name)
	}
	if !latest.Quotas[0].Unlimited {
		t.Error("chat should be unlimited")
	}
	if latest.Quotas[1].Name != "premium_interactions" {
		t.Errorf("Quotas[1].Name = %q, want premium_interactions", latest.Quotas[1].Name)
	}
	if latest.Quotas[1].Remaining != 473 {
		t.Errorf("premium Remaining = %d, want 473", latest.Quotas[1].Remaining)
	}
}

func TestCopilotStore_QueryLatest_Empty(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	latest, err := s.QueryLatestCopilot()
	if err != nil {
		t.Fatalf("QueryLatestCopilot: %v", err)
	}
	if latest != nil {
		t.Error("Expected nil for empty store")
	}
}

func TestCopilotStore_QueryRange(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetDate := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// Insert 5 snapshots
	for i := range 5 {
		snap := newTestCopilotSnapshot(now.Add(time.Duration(i)*time.Minute), &resetDate)
		snap.Quotas[1].Remaining = 1500 - (i * 100) // Decrement premium remaining
		if _, err := s.InsertCopilotSnapshot(snap); err != nil {
			t.Fatalf("InsertCopilotSnapshot[%d]: %v", i, err)
		}
	}

	// Query range
	snapshots, err := s.QueryCopilotRange(now.Add(-time.Minute), now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("QueryCopilotRange: %v", err)
	}
	if len(snapshots) != 5 {
		t.Fatalf("QueryCopilotRange len = %d, want 5", len(snapshots))
	}

	// Verify quotas loaded for first snapshot
	if len(snapshots[0].Quotas) != 2 {
		t.Errorf("Snapshot[0] Quotas len = %d, want 2", len(snapshots[0].Quotas))
	}

	// Query with limit
	limited, err := s.QueryCopilotRange(now.Add(-time.Minute), now.Add(10*time.Minute), 2)
	if err != nil {
		t.Fatalf("QueryCopilotRange with limit: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("QueryCopilotRange limited len = %d, want 2", len(limited))
	}

	if !limited[0].CapturedAt.Equal(now.Add(3 * time.Minute)) {
		t.Fatalf("expected first limited snapshot at t+3m, got %s", limited[0].CapturedAt)
	}
	if !limited[1].CapturedAt.Equal(now.Add(4 * time.Minute)) {
		t.Fatalf("expected second limited snapshot at t+4m, got %s", limited[1].CapturedAt)
	}
}

func TestCopilotStore_Cycles(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetDate := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// Create a cycle
	id, err := s.CreateCopilotCycle("premium_interactions", now, &resetDate)
	if err != nil {
		t.Fatalf("CreateCopilotCycle: %v", err)
	}
	if id <= 0 {
		t.Error("Expected positive cycle ID")
	}

	// Query active cycle
	active, err := s.QueryActiveCopilotCycle("premium_interactions")
	if err != nil {
		t.Fatalf("QueryActiveCopilotCycle: %v", err)
	}
	if active == nil {
		t.Fatal("Expected active cycle")
	}
	if active.QuotaName != "premium_interactions" {
		t.Errorf("QuotaName = %q, want premium_interactions", active.QuotaName)
	}

	// Update cycle
	if err := s.UpdateCopilotCycle("premium_interactions", 500, 300); err != nil {
		t.Fatalf("UpdateCopilotCycle: %v", err)
	}

	// Verify update
	active, err = s.QueryActiveCopilotCycle("premium_interactions")
	if err != nil {
		t.Fatalf("QueryActiveCopilotCycle after update: %v", err)
	}
	if active.PeakUsed != 500 {
		t.Errorf("PeakUsed = %d, want 500", active.PeakUsed)
	}
	if active.TotalDelta != 300 {
		t.Errorf("TotalDelta = %d, want 300", active.TotalDelta)
	}

	// Close cycle
	endTime := now.Add(30 * 24 * time.Hour)
	if err := s.CloseCopilotCycle("premium_interactions", endTime, 800, 600); err != nil {
		t.Fatalf("CloseCopilotCycle: %v", err)
	}

	// Verify no active cycle
	active, err = s.QueryActiveCopilotCycle("premium_interactions")
	if err != nil {
		t.Fatalf("QueryActiveCopilotCycle after close: %v", err)
	}
	if active != nil {
		t.Error("Expected nil active cycle after close")
	}

	// Query history
	history, err := s.QueryCopilotCycleHistory("premium_interactions")
	if err != nil {
		t.Fatalf("QueryCopilotCycleHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("CycleHistory len = %d, want 1", len(history))
	}
	if history[0].PeakUsed != 800 {
		t.Errorf("History PeakUsed = %d, want 800", history[0].PeakUsed)
	}
}

func TestCopilotStore_CyclesSince(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetDate := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// Create and close 2 cycles
	for i := range 2 {
		start := now.Add(-time.Duration(2-i) * 30 * 24 * time.Hour)
		if _, err := s.CreateCopilotCycle("premium_interactions", start, &resetDate); err != nil {
			t.Fatalf("CreateCopilotCycle[%d]: %v", i, err)
		}
		end := start.Add(30 * 24 * time.Hour)
		if err := s.CloseCopilotCycle("premium_interactions", end, 500+i*100, 300+i*50); err != nil {
			t.Fatalf("CloseCopilotCycle[%d]: %v", i, err)
		}
	}

	// Query cycles since 90 days ago (should include both)
	cycles, err := s.QueryCopilotCyclesSince("premium_interactions", now.Add(-90*24*time.Hour))
	if err != nil {
		t.Fatalf("QueryCopilotCyclesSince: %v", err)
	}
	if len(cycles) != 2 {
		t.Errorf("CyclesSince len = %d, want 2", len(cycles))
	}
}

func TestCopilotStore_UsageSeries(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetDate := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// Insert snapshots
	for i := range 3 {
		snap := newTestCopilotSnapshot(now.Add(time.Duration(i)*time.Minute), &resetDate)
		snap.Quotas[1].Remaining = 1500 - (i * 100)
		if _, err := s.InsertCopilotSnapshot(snap); err != nil {
			t.Fatalf("InsertCopilotSnapshot[%d]: %v", i, err)
		}
	}

	points, err := s.QueryCopilotUsageSeries("premium_interactions", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("QueryCopilotUsageSeries: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("UsageSeries len = %d, want 3", len(points))
	}
	// First point should have remaining=1500, last=1300
	if points[0].Remaining != 1500 {
		t.Errorf("points[0].Remaining = %d, want 1500", points[0].Remaining)
	}
	if points[2].Remaining != 1300 {
		t.Errorf("points[2].Remaining = %d, want 1300", points[2].Remaining)
	}
}

func TestCopilotStore_QuotaNames(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	snap := newTestCopilotSnapshot(now, nil)
	if _, err := s.InsertCopilotSnapshot(snap); err != nil {
		t.Fatalf("InsertCopilotSnapshot: %v", err)
	}

	names, err := s.QueryAllCopilotQuotaNames()
	if err != nil {
		t.Fatalf("QueryAllCopilotQuotaNames: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("QuotaNames len = %d, want 2", len(names))
	}
	if names[0] != "chat" || names[1] != "premium_interactions" {
		t.Errorf("QuotaNames = %v, want [chat, premium_interactions]", names)
	}
}
