package store

import (
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
)

func newTestMiniMaxSnapshot(capturedAt time.Time, resetAt *time.Time) *api.MiniMaxSnapshot {
	return &api.MiniMaxSnapshot{
		CapturedAt: capturedAt,
		RawJSON:    `{"test": true}`,
		Models: []api.MiniMaxModelQuota{
			{ModelName: "MiniMax-M1", Total: 200, Remain: 158, Used: 42, UsedPercent: 21.0, ResetAt: resetAt, TimeUntilReset: 2 * time.Hour},
			{ModelName: "MiniMax-Text-01", Total: 100, Remain: 80, Used: 20, UsedPercent: 20.0, ResetAt: resetAt, TimeUntilReset: 2 * time.Hour},
		},
	}
}

func TestMiniMaxStore_InsertAndQueryLatest(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour).UTC().Truncate(time.Second)
	snap := newTestMiniMaxSnapshot(now, &resetAt)

	id, err := s.InsertMiniMaxSnapshot(snap)
	if err != nil {
		t.Fatalf("InsertMiniMaxSnapshot: %v", err)
	}
	if id <= 0 {
		t.Errorf("Expected positive ID, got %d", id)
	}

	latest, err := s.QueryLatestMiniMax()
	if err != nil {
		t.Fatalf("QueryLatestMiniMax: %v", err)
	}
	if latest == nil {
		t.Fatal("QueryLatestMiniMax returned nil")
	}
	if len(latest.Models) != 2 {
		t.Fatalf("Models len = %d, want 2", len(latest.Models))
	}
	if latest.Models[0].ModelName != "MiniMax-M1" {
		t.Errorf("Models[0].ModelName = %q, want MiniMax-M1", latest.Models[0].ModelName)
	}
}

func TestMiniMaxStore_QueryRange(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour).UTC().Truncate(time.Second)

	for i := range 3 {
		snap := newTestMiniMaxSnapshot(now.Add(time.Duration(i)*time.Minute), &resetAt)
		snap.Models[0].Used = 40 + i
		snap.Models[0].Remain = 160 - i
		if _, err := s.InsertMiniMaxSnapshot(snap); err != nil {
			t.Fatalf("InsertMiniMaxSnapshot[%d]: %v", i, err)
		}
	}

	snapshots, err := s.QueryMiniMaxRange(now.Add(-time.Minute), now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("QueryMiniMaxRange: %v", err)
	}
	if len(snapshots) != 3 {
		t.Fatalf("QueryMiniMaxRange len = %d, want 3", len(snapshots))
	}
	if len(snapshots[0].Models) != 2 {
		t.Fatalf("Snapshot[0] Models len = %d, want 2", len(snapshots[0].Models))
	}
}

func TestMiniMaxStore_Cycles(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)

	id, err := s.CreateMiniMaxCycle("MiniMax-M1", now, &resetAt)
	if err != nil {
		t.Fatalf("CreateMiniMaxCycle: %v", err)
	}
	if id <= 0 {
		t.Error("Expected positive cycle ID")
	}

	active, err := s.QueryActiveMiniMaxCycle("MiniMax-M1")
	if err != nil {
		t.Fatalf("QueryActiveMiniMaxCycle: %v", err)
	}
	if active == nil {
		t.Fatal("Expected active cycle")
	}

	if err := s.UpdateMiniMaxCycle("MiniMax-M1", 80, 30); err != nil {
		t.Fatalf("UpdateMiniMaxCycle: %v", err)
	}

	active, err = s.QueryActiveMiniMaxCycle("MiniMax-M1")
	if err != nil {
		t.Fatalf("QueryActiveMiniMaxCycle after update: %v", err)
	}
	if active.PeakUsed != 80 {
		t.Errorf("PeakUsed = %d, want 80", active.PeakUsed)
	}
	if active.TotalDelta != 30 {
		t.Errorf("TotalDelta = %d, want 30", active.TotalDelta)
	}

	if err := s.CloseMiniMaxCycle("MiniMax-M1", now.Add(2*time.Hour), 80, 30); err != nil {
		t.Fatalf("CloseMiniMaxCycle: %v", err)
	}

	active, err = s.QueryActiveMiniMaxCycle("MiniMax-M1")
	if err != nil {
		t.Fatalf("QueryActiveMiniMaxCycle after close: %v", err)
	}
	if active != nil {
		t.Fatal("Expected no active cycle after close")
	}

	history, err := s.QueryMiniMaxCycleHistory("MiniMax-M1")
	if err != nil {
		t.Fatalf("QueryMiniMaxCycleHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("Cycle history len = %d, want 1", len(history))
	}
}

func TestMiniMaxStore_UsageSeriesAndModelNames(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour).UTC().Truncate(time.Second)

	for i := range 3 {
		snap := newTestMiniMaxSnapshot(now.Add(time.Duration(i)*time.Minute), &resetAt)
		snap.Models[0].Used = 40 + i
		snap.Models[0].Remain = 160 - i
		if _, err := s.InsertMiniMaxSnapshot(snap); err != nil {
			t.Fatalf("InsertMiniMaxSnapshot[%d]: %v", i, err)
		}
	}

	points, err := s.QueryMiniMaxUsageSeries("MiniMax-M1", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("QueryMiniMaxUsageSeries: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("UsageSeries len = %d, want 3", len(points))
	}

	names, err := s.QueryAllMiniMaxModelNames()
	if err != nil {
		t.Fatalf("QueryAllMiniMaxModelNames: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("Model names len = %d, want 2", len(names))
	}
}

func TestMiniMaxStore_QueryCycleOverview_LimitRespectedWithActiveCycle(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)

	if _, err := s.CreateMiniMaxCycle("MiniMax-M2", now.Add(-2*time.Hour), &resetAt); err != nil {
		t.Fatalf("CreateMiniMaxCycle completed: %v", err)
	}
	if err := s.UpdateMiniMaxCycle("MiniMax-M2", 80, 30); err != nil {
		t.Fatalf("UpdateMiniMaxCycle completed: %v", err)
	}
	if err := s.CloseMiniMaxCycle("MiniMax-M2", now.Add(-time.Hour), 80, 30); err != nil {
		t.Fatalf("CloseMiniMaxCycle completed: %v", err)
	}

	if _, err := s.CreateMiniMaxCycle("MiniMax-M2", now.Add(-30*time.Minute), &resetAt); err != nil {
		t.Fatalf("CreateMiniMaxCycle active: %v", err)
	}
	if err := s.UpdateMiniMaxCycle("MiniMax-M2", 25, 10); err != nil {
		t.Fatalf("UpdateMiniMaxCycle active: %v", err)
	}

	rows, err := s.QueryMiniMaxCycleOverview("MiniMax-M2", 1)
	if err != nil {
		t.Fatalf("QueryMiniMaxCycleOverview(limit=1): %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want 1", len(rows))
	}
}
