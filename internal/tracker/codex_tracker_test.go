package tracker

import (
	"log/slog"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
)

func newTestCodexStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCodexTracker_Process_FirstSnapshot(t *testing.T) {
	s := newTestCodexStore(t)
	tr := NewCodexTracker(s, slog.Default())

	now := time.Now().UTC()
	reset := now.Add(5 * time.Hour)
	snap := &api.CodexSnapshot{
		CapturedAt: now,
		Quotas: []api.CodexQuota{
			{Name: "five_hour", Utilization: 22.5, ResetsAt: &reset, Status: "healthy"},
		},
	}

	if err := tr.Process(snap); err != nil {
		t.Fatalf("Process: %v", err)
	}

	cycle, err := s.QueryActiveCodexCycle("five_hour")
	if err != nil {
		t.Fatalf("QueryActiveCodexCycle: %v", err)
	}
	if cycle == nil {
		t.Fatal("expected active cycle")
	}
	if cycle.PeakUtilization != 22.5 {
		t.Fatalf("PeakUtilization = %.1f, want 22.5", cycle.PeakUtilization)
	}
}

func TestCodexTracker_Process_UsageIncrease(t *testing.T) {
	s := newTestCodexStore(t)
	tr := NewCodexTracker(s, slog.Default())

	now := time.Now().UTC()
	reset := now.Add(5 * time.Hour)

	snap1 := &api.CodexSnapshot{
		CapturedAt: now,
		Quotas: []api.CodexQuota{
			{Name: "five_hour", Utilization: 20, ResetsAt: &reset, Status: "healthy"},
		},
	}
	if err := tr.Process(snap1); err != nil {
		t.Fatalf("Process snap1: %v", err)
	}

	snap2 := &api.CodexSnapshot{
		CapturedAt: now.Add(time.Minute),
		Quotas: []api.CodexQuota{
			{Name: "five_hour", Utilization: 35, ResetsAt: &reset, Status: "healthy"},
		},
	}
	if err := tr.Process(snap2); err != nil {
		t.Fatalf("Process snap2: %v", err)
	}

	cycle, err := s.QueryActiveCodexCycle("five_hour")
	if err != nil {
		t.Fatalf("QueryActiveCodexCycle: %v", err)
	}
	if cycle == nil {
		t.Fatal("expected active cycle")
	}
	if cycle.PeakUtilization != 35 {
		t.Fatalf("PeakUtilization = %.1f, want 35", cycle.PeakUtilization)
	}
	if cycle.TotalDelta != 15 {
		t.Fatalf("TotalDelta = %.1f, want 15", cycle.TotalDelta)
	}
}

func TestCodexTracker_Process_ResetDetection(t *testing.T) {
	s := newTestCodexStore(t)
	tr := NewCodexTracker(s, slog.Default())

	resetDetected := false
	tr.SetOnReset(func(string) {
		resetDetected = true
	})

	now := time.Now().UTC()
	reset1 := now.Add(5 * time.Hour)
	reset2 := now.Add(7 * 24 * time.Hour)

	snap1 := &api.CodexSnapshot{
		CapturedAt: now,
		Quotas: []api.CodexQuota{
			{Name: "five_hour", Utilization: 45, ResetsAt: &reset1, Status: "warning"},
		},
	}
	if err := tr.Process(snap1); err != nil {
		t.Fatalf("Process snap1: %v", err)
	}

	snap2 := &api.CodexSnapshot{
		CapturedAt: now.Add(time.Minute),
		Quotas: []api.CodexQuota{
			{Name: "five_hour", Utilization: 5, ResetsAt: &reset2, Status: "healthy"},
		},
	}
	if err := tr.Process(snap2); err != nil {
		t.Fatalf("Process snap2: %v", err)
	}

	if !resetDetected {
		t.Fatal("expected reset callback")
	}

	history, err := s.QueryCodexCycleHistory("five_hour")
	if err != nil {
		t.Fatalf("QueryCodexCycleHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("len(history) = %d, want 1", len(history))
	}

	active, err := s.QueryActiveCodexCycle("five_hour")
	if err != nil {
		t.Fatalf("QueryActiveCodexCycle: %v", err)
	}
	if active == nil {
		t.Fatal("expected new active cycle")
	}
	if active.PeakUtilization != 5 {
		t.Fatalf("active.PeakUtilization = %.1f, want 5", active.PeakUtilization)
	}
}

func TestCodexTracker_UsageSummary(t *testing.T) {
	s := newTestCodexStore(t)
	tr := NewCodexTracker(s, slog.Default())

	now := time.Now().UTC()
	reset := now.Add(5 * time.Hour)
	snap := &api.CodexSnapshot{
		CapturedAt: now,
		PlanType:   "pro",
		Quotas: []api.CodexQuota{
			{Name: "five_hour", Utilization: 28, ResetsAt: &reset, Status: "healthy"},
		},
	}

	if _, err := s.InsertCodexSnapshot(snap); err != nil {
		t.Fatalf("InsertCodexSnapshot: %v", err)
	}
	if err := tr.Process(snap); err != nil {
		t.Fatalf("Process: %v", err)
	}

	summary, err := tr.UsageSummary("five_hour")
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if summary == nil {
		t.Fatal("expected summary")
	}
	if summary.CurrentUtil != 28 {
		t.Fatalf("CurrentUtil = %.1f, want 28", summary.CurrentUtil)
	}
	if summary.ResetsAt == nil {
		t.Fatal("expected ResetsAt")
	}
}
