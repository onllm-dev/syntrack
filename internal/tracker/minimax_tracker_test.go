package tracker

import (
	"log/slog"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
)

func newTestMiniMaxStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMiniMaxTracker_Process_FirstSnapshot(t *testing.T) {
	s := newTestMiniMaxStore(t)
	tr := NewMiniMaxTracker(s, slog.Default())

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)
	snap := &api.MiniMaxSnapshot{
		CapturedAt: now,
		Models: []api.MiniMaxModelQuota{
			{ModelName: "MiniMax-M1", Total: 200, Remain: 158, Used: 42, ResetAt: &resetAt},
		},
	}

	if err := tr.Process(snap); err != nil {
		t.Fatalf("Process: %v", err)
	}

	cycle, err := s.QueryActiveMiniMaxCycle("MiniMax-M1")
	if err != nil {
		t.Fatalf("QueryActiveMiniMaxCycle: %v", err)
	}
	if cycle == nil {
		t.Fatal("Expected active cycle after first snapshot")
	}
	if cycle.PeakUsed != 42 {
		t.Errorf("PeakUsed = %d, want 42", cycle.PeakUsed)
	}
}

func TestMiniMaxTracker_Process_UsageIncrease(t *testing.T) {
	s := newTestMiniMaxStore(t)
	tr := NewMiniMaxTracker(s, slog.Default())

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)

	snap1 := &api.MiniMaxSnapshot{CapturedAt: now, Models: []api.MiniMaxModelQuota{{ModelName: "MiniMax-M1", Total: 200, Remain: 158, Used: 42, ResetAt: &resetAt}}}
	if err := tr.Process(snap1); err != nil {
		t.Fatalf("Process snap1: %v", err)
	}

	snap2 := &api.MiniMaxSnapshot{CapturedAt: now.Add(time.Minute), Models: []api.MiniMaxModelQuota{{ModelName: "MiniMax-M1", Total: 200, Remain: 140, Used: 60, ResetAt: &resetAt}}}
	if err := tr.Process(snap2); err != nil {
		t.Fatalf("Process snap2: %v", err)
	}

	cycle, err := s.QueryActiveMiniMaxCycle("MiniMax-M1")
	if err != nil {
		t.Fatalf("QueryActiveMiniMaxCycle: %v", err)
	}
	if cycle.PeakUsed != 60 {
		t.Errorf("PeakUsed = %d, want 60", cycle.PeakUsed)
	}
	if cycle.TotalDelta != 18 {
		t.Errorf("TotalDelta = %d, want 18", cycle.TotalDelta)
	}
}

func TestMiniMaxTracker_Process_ResetDetection_ByWindowChange(t *testing.T) {
	s := newTestMiniMaxStore(t)
	tr := NewMiniMaxTracker(s, slog.Default())

	resetDetected := false
	tr.SetOnReset(func(modelName string) {
		if modelName == "MiniMax-M1" {
			resetDetected = true
		}
	})

	now := time.Now().UTC()
	resetAt1 := now.Add(2 * time.Hour)
	resetAt2 := now.Add(7 * time.Hour)

	snap1 := &api.MiniMaxSnapshot{CapturedAt: now, Models: []api.MiniMaxModelQuota{{ModelName: "MiniMax-M1", Total: 200, Remain: 20, Used: 180, ResetAt: &resetAt1}}}
	if err := tr.Process(snap1); err != nil {
		t.Fatalf("Process snap1: %v", err)
	}

	snap2 := &api.MiniMaxSnapshot{CapturedAt: now.Add(2 * time.Minute), Models: []api.MiniMaxModelQuota{{ModelName: "MiniMax-M1", Total: 200, Remain: 200, Used: 0, ResetAt: &resetAt2}}}
	if err := tr.Process(snap2); err != nil {
		t.Fatalf("Process snap2: %v", err)
	}

	if !resetDetected {
		t.Error("Expected reset callback to fire")
	}

	history, err := s.QueryMiniMaxCycleHistory("MiniMax-M1")
	if err != nil {
		t.Fatalf("QueryMiniMaxCycleHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("Expected 1 completed cycle, got %d", len(history))
	}
}

func TestMiniMaxTracker_Process_ResetDetection_ByUsageDrop(t *testing.T) {
	s := newTestMiniMaxStore(t)
	tr := NewMiniMaxTracker(s, slog.Default())

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)

	snap1 := &api.MiniMaxSnapshot{CapturedAt: now, Models: []api.MiniMaxModelQuota{{ModelName: "MiniMax-M1", Total: 200, Remain: 20, Used: 180, ResetAt: &resetAt}}}
	if err := tr.Process(snap1); err != nil {
		t.Fatalf("Process snap1: %v", err)
	}

	snap2 := &api.MiniMaxSnapshot{CapturedAt: now.Add(2 * time.Minute), Models: []api.MiniMaxModelQuota{{ModelName: "MiniMax-M1", Total: 200, Remain: 170, Used: 30, ResetAt: &resetAt}}}
	if err := tr.Process(snap2); err != nil {
		t.Fatalf("Process snap2: %v", err)
	}

	history, err := s.QueryMiniMaxCycleHistory("MiniMax-M1")
	if err != nil {
		t.Fatalf("QueryMiniMaxCycleHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("Expected 1 completed cycle after drop reset, got %d", len(history))
	}
}

func TestMiniMaxTracker_UsageSummary(t *testing.T) {
	s := newTestMiniMaxStore(t)
	tr := NewMiniMaxTracker(s, slog.Default())

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)
	snap := &api.MiniMaxSnapshot{
		CapturedAt: now,
		Models: []api.MiniMaxModelQuota{
			{ModelName: "MiniMax-M1", Total: 200, Remain: 158, Used: 42, UsedPercent: 21, ResetAt: &resetAt},
		},
	}
	if _, err := s.InsertMiniMaxSnapshot(snap); err != nil {
		t.Fatalf("InsertMiniMaxSnapshot: %v", err)
	}
	if err := tr.Process(snap); err != nil {
		t.Fatalf("Process: %v", err)
	}

	summary, err := tr.UsageSummary("MiniMax-M1")
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if summary == nil {
		t.Fatal("Expected summary")
	}
	if summary.Total != 200 {
		t.Errorf("Total = %d, want 200", summary.Total)
	}
	if summary.CurrentUsed != 42 {
		t.Errorf("CurrentUsed = %d, want 42", summary.CurrentUsed)
	}
}

func TestMiniMaxTracker_Process_ResetDetection_ByWindowBoundary(t *testing.T) {
	s := newTestMiniMaxStore(t)
	tr := NewMiniMaxTracker(s, slog.Default())

	now := time.Now().UTC()
	windowStart1 := now.Add(-30 * time.Minute)
	windowEnd1 := now.Add(30 * time.Minute)
	windowStart2 := now.Add(31 * time.Minute)
	windowEnd2 := now.Add(2 * time.Hour)

	snap1 := &api.MiniMaxSnapshot{CapturedAt: now, Models: []api.MiniMaxModelQuota{{
		ModelName:   "MiniMax-M2",
		Total:       200,
		Remain:      20,
		Used:        180,
		WindowStart: &windowStart1,
		WindowEnd:   &windowEnd1,
	}}}
	if err := tr.Process(snap1); err != nil {
		t.Fatalf("Process snap1: %v", err)
	}

	snap2 := &api.MiniMaxSnapshot{CapturedAt: now.Add(2 * time.Minute), Models: []api.MiniMaxModelQuota{{
		ModelName:   "MiniMax-M2",
		Total:       200,
		Remain:      18,
		Used:        182,
		WindowStart: &windowStart2,
		WindowEnd:   &windowEnd2,
	}}}
	if err := tr.Process(snap2); err != nil {
		t.Fatalf("Process snap2: %v", err)
	}

	history, err := s.QueryMiniMaxCycleHistory("MiniMax-M2")
	if err != nil {
		t.Fatalf("QueryMiniMaxCycleHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("Expected 1 completed cycle after window boundary reset, got %d", len(history))
	}
}
