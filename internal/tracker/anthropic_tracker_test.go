package tracker

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
)

// helper to create an AnthropicSnapshot with a single quota.
func makeAnthropicSnapshot(capturedAt time.Time, quotaName string, utilization float64, resetsAt *time.Time) *api.AnthropicSnapshot {
	return &api.AnthropicSnapshot{
		CapturedAt: capturedAt,
		Quotas: []api.AnthropicQuota{
			{Name: quotaName, Utilization: utilization, ResetsAt: resetsAt},
		},
	}
}

// helper to create an AnthropicSnapshot with multiple quotas.
func makeMultiQuotaSnapshot(capturedAt time.Time, quotas []api.AnthropicQuota) *api.AnthropicSnapshot {
	return &api.AnthropicSnapshot{
		CapturedAt: capturedAt,
		Quotas:     quotas,
	}
}

// timePtr returns a pointer to the given time.
func timePtr(t time.Time) *time.Time {
	return &t
}

func TestAnthropicTracker_Process_FirstSnapshot_CreatesCycle(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	tracker := NewAnthropicTracker(s, nil)
	baseTime := time.Now().Truncate(time.Second)
	resetsAt := baseTime.Add(5 * time.Hour)

	snapshot := makeAnthropicSnapshot(baseTime, "five_hour", 25.0, &resetsAt)

	if err := tracker.Process(snapshot); err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	// Verify cycle was created in DB
	cycle, err := s.QueryActiveAnthropicCycle("five_hour")
	if err != nil {
		t.Fatalf("QueryActiveAnthropicCycle failed: %v", err)
	}
	if cycle == nil {
		t.Fatal("Expected active cycle for five_hour, got nil")
	}
	if cycle.QuotaName != "five_hour" {
		t.Errorf("QuotaName = %q, want %q", cycle.QuotaName, "five_hour")
	}
	if cycle.PeakUtilization != 25.0 {
		t.Errorf("PeakUtilization = %v, want 25.0", cycle.PeakUtilization)
	}
	if cycle.TotalDelta != 0 {
		t.Errorf("TotalDelta = %v, want 0 (first snapshot has no delta)", cycle.TotalDelta)
	}
	if cycle.CycleEnd != nil {
		t.Error("Expected cycle_end to be nil (active cycle)")
	}
}

func TestAnthropicTracker_Process_SameCycle_UpdatesPeakAndDelta(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	tracker := NewAnthropicTracker(s, nil)
	baseTime := time.Now().Truncate(time.Second)
	resetsAt := baseTime.Add(5 * time.Hour)

	// First snapshot: utilization 20%
	snap1 := makeAnthropicSnapshot(baseTime, "five_hour", 20.0, &resetsAt)
	if err := tracker.Process(snap1); err != nil {
		t.Fatalf("Process snap1 failed: %v", err)
	}

	// Second snapshot: utilization increased to 35%
	snap2 := makeAnthropicSnapshot(baseTime.Add(1*time.Minute), "five_hour", 35.0, &resetsAt)
	if err := tracker.Process(snap2); err != nil {
		t.Fatalf("Process snap2 failed: %v", err)
	}

	cycle, err := s.QueryActiveAnthropicCycle("five_hour")
	if err != nil {
		t.Fatalf("QueryActiveAnthropicCycle failed: %v", err)
	}
	if cycle == nil {
		t.Fatal("Expected active cycle, got nil")
	}

	// Peak should be 35.0
	if cycle.PeakUtilization != 35.0 {
		t.Errorf("PeakUtilization = %v, want 35.0", cycle.PeakUtilization)
	}
	// Delta should be 15.0 (35 - 20)
	if cycle.TotalDelta != 15.0 {
		t.Errorf("TotalDelta = %v, want 15.0", cycle.TotalDelta)
	}

	// Third snapshot: utilization increases to 50%
	snap3 := makeAnthropicSnapshot(baseTime.Add(2*time.Minute), "five_hour", 50.0, &resetsAt)
	if err := tracker.Process(snap3); err != nil {
		t.Fatalf("Process snap3 failed: %v", err)
	}

	cycle, _ = s.QueryActiveAnthropicCycle("five_hour")
	// Peak should be 50.0
	if cycle.PeakUtilization != 50.0 {
		t.Errorf("PeakUtilization = %v, want 50.0", cycle.PeakUtilization)
	}
	// Delta should be 30.0 (15 + 15)
	if cycle.TotalDelta != 30.0 {
		t.Errorf("TotalDelta = %v, want 30.0", cycle.TotalDelta)
	}
}

func TestAnthropicTracker_Process_ResetDetected_ClosesCycleCreatesNew(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	tracker := NewAnthropicTracker(s, nil)
	baseTime := time.Now().Truncate(time.Second)
	resetsAt1 := baseTime.Add(5 * time.Hour)

	// First snapshot
	snap1 := makeAnthropicSnapshot(baseTime, "five_hour", 40.0, &resetsAt1)
	if err := tracker.Process(snap1); err != nil {
		t.Fatalf("Process snap1 failed: %v", err)
	}

	// Second snapshot: same cycle, utilization increased
	snap2 := makeAnthropicSnapshot(baseTime.Add(1*time.Minute), "five_hour", 60.0, &resetsAt1)
	if err := tracker.Process(snap2); err != nil {
		t.Fatalf("Process snap2 failed: %v", err)
	}

	// Third snapshot: reset detected (resetsAt changed by >10 minutes)
	resetsAt2 := baseTime.Add(10 * time.Hour)
	snap3 := makeAnthropicSnapshot(baseTime.Add(2*time.Minute), "five_hour", 5.0, &resetsAt2)
	if err := tracker.Process(snap3); err != nil {
		t.Fatalf("Process snap3 failed: %v", err)
	}

	// Old cycle should be closed
	history, err := s.QueryAnthropicCycleHistory("five_hour")
	if err != nil {
		t.Fatalf("QueryAnthropicCycleHistory failed: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("Expected 1 closed cycle, got %d", len(history))
	}
	closedCycle := history[0]
	if closedCycle.CycleEnd == nil {
		t.Error("Expected closed cycle to have a cycle_end timestamp")
	}
	if closedCycle.PeakUtilization != 60.0 {
		t.Errorf("Closed cycle PeakUtilization = %v, want 60.0", closedCycle.PeakUtilization)
	}

	// New active cycle should exist
	activeCycle, err := s.QueryActiveAnthropicCycle("five_hour")
	if err != nil {
		t.Fatalf("QueryActiveAnthropicCycle failed: %v", err)
	}
	if activeCycle == nil {
		t.Fatal("Expected new active cycle after reset")
	}
	if activeCycle.PeakUtilization != 5.0 {
		t.Errorf("New cycle PeakUtilization = %v, want 5.0", activeCycle.PeakUtilization)
	}
	if activeCycle.TotalDelta != 0 {
		t.Errorf("New cycle TotalDelta = %v, want 0", activeCycle.TotalDelta)
	}
}

func TestAnthropicTracker_Process_JitterIgnored(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	tracker := NewAnthropicTracker(s, nil)
	baseTime := time.Now().Truncate(time.Second)
	resetsAt1 := baseTime.Add(5 * time.Hour)

	// First snapshot
	snap1 := makeAnthropicSnapshot(baseTime, "five_hour", 30.0, &resetsAt1)
	if err := tracker.Process(snap1); err != nil {
		t.Fatalf("Process snap1 failed: %v", err)
	}

	// Second snapshot: resetsAt shifts by 2 seconds (jitter < 10min threshold)
	resetsAtJitter := resetsAt1.Add(2 * time.Second)
	snap2 := makeAnthropicSnapshot(baseTime.Add(1*time.Minute), "five_hour", 35.0, &resetsAtJitter)
	if err := tracker.Process(snap2); err != nil {
		t.Fatalf("Process snap2 failed: %v", err)
	}

	// Should NOT have created a new cycle -- still same active cycle, no closed cycles
	history, err := s.QueryAnthropicCycleHistory("five_hour")
	if err != nil {
		t.Fatalf("QueryAnthropicCycleHistory failed: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("Expected 0 closed cycles (jitter should be ignored), got %d", len(history))
	}

	// Active cycle should have updated delta
	cycle, _ := s.QueryActiveAnthropicCycle("five_hour")
	if cycle == nil {
		t.Fatal("Expected active cycle")
	}
	if cycle.TotalDelta != 5.0 {
		t.Errorf("TotalDelta = %v, want 5.0 (delta should accumulate despite jitter)", cycle.TotalDelta)
	}

	// Third snapshot: resetsAt shifts by 9 minutes (still within 10min threshold)
	resetsAtNearThreshold := resetsAt1.Add(9 * time.Minute)
	snap3 := makeAnthropicSnapshot(baseTime.Add(2*time.Minute), "five_hour", 40.0, &resetsAtNearThreshold)
	if err := tracker.Process(snap3); err != nil {
		t.Fatalf("Process snap3 failed: %v", err)
	}

	// Still no reset
	history, _ = s.QueryAnthropicCycleHistory("five_hour")
	if len(history) != 0 {
		t.Errorf("Expected 0 closed cycles (9min jitter within threshold), got %d", len(history))
	}
}

func TestAnthropicTracker_Process_MultipleQuotas(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	tracker := NewAnthropicTracker(s, nil)
	baseTime := time.Now().Truncate(time.Second)
	fiveHourReset := baseTime.Add(5 * time.Hour)
	sevenDayReset := baseTime.Add(7 * 24 * time.Hour)

	// First snapshot with both quotas
	snap1 := makeMultiQuotaSnapshot(baseTime, []api.AnthropicQuota{
		{Name: "five_hour", Utilization: 10.0, ResetsAt: &fiveHourReset},
		{Name: "seven_day", Utilization: 5.0, ResetsAt: &sevenDayReset},
	})
	if err := tracker.Process(snap1); err != nil {
		t.Fatalf("Process snap1 failed: %v", err)
	}

	// Second snapshot: both increase
	snap2 := makeMultiQuotaSnapshot(baseTime.Add(1*time.Minute), []api.AnthropicQuota{
		{Name: "five_hour", Utilization: 30.0, ResetsAt: &fiveHourReset},
		{Name: "seven_day", Utilization: 8.0, ResetsAt: &sevenDayReset},
	})
	if err := tracker.Process(snap2); err != nil {
		t.Fatalf("Process snap2 failed: %v", err)
	}

	// Third snapshot: five_hour resets, seven_day continues
	newFiveHourReset := baseTime.Add(10 * time.Hour)
	snap3 := makeMultiQuotaSnapshot(baseTime.Add(2*time.Minute), []api.AnthropicQuota{
		{Name: "five_hour", Utilization: 2.0, ResetsAt: &newFiveHourReset},
		{Name: "seven_day", Utilization: 12.0, ResetsAt: &sevenDayReset},
	})
	if err := tracker.Process(snap3); err != nil {
		t.Fatalf("Process snap3 failed: %v", err)
	}

	// five_hour should have 1 completed cycle and 1 active cycle
	fiveHourHistory, _ := s.QueryAnthropicCycleHistory("five_hour")
	if len(fiveHourHistory) != 1 {
		t.Errorf("five_hour: expected 1 closed cycle, got %d", len(fiveHourHistory))
	}
	fiveHourActive, _ := s.QueryActiveAnthropicCycle("five_hour")
	if fiveHourActive == nil {
		t.Error("five_hour: expected active cycle after reset")
	}

	// seven_day should have 0 completed cycles and 1 active cycle (no reset)
	sevenDayHistory, _ := s.QueryAnthropicCycleHistory("seven_day")
	if len(sevenDayHistory) != 0 {
		t.Errorf("seven_day: expected 0 closed cycles, got %d", len(sevenDayHistory))
	}
	sevenDayActive, _ := s.QueryActiveAnthropicCycle("seven_day")
	if sevenDayActive == nil {
		t.Error("seven_day: expected active cycle")
	}
	// seven_day delta should be 7.0 (8-5 + 12-8)
	if sevenDayActive.TotalDelta != 7.0 {
		t.Errorf("seven_day TotalDelta = %v, want 7.0", sevenDayActive.TotalDelta)
	}
	if sevenDayActive.PeakUtilization != 12.0 {
		t.Errorf("seven_day PeakUtilization = %v, want 12.0", sevenDayActive.PeakUtilization)
	}
}

func TestAnthropicTracker_Process_UtilizationDecrease_NoDelta(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	tracker := NewAnthropicTracker(s, nil)
	baseTime := time.Now().Truncate(time.Second)
	resetsAt := baseTime.Add(5 * time.Hour)

	// First snapshot: utilization 50%
	snap1 := makeAnthropicSnapshot(baseTime, "five_hour", 50.0, &resetsAt)
	if err := tracker.Process(snap1); err != nil {
		t.Fatalf("Process snap1 failed: %v", err)
	}

	// Second snapshot: utilization drops to 30% (same resetsAt, so not a reset)
	snap2 := makeAnthropicSnapshot(baseTime.Add(1*time.Minute), "five_hour", 30.0, &resetsAt)
	if err := tracker.Process(snap2); err != nil {
		t.Fatalf("Process snap2 failed: %v", err)
	}

	cycle, _ := s.QueryActiveAnthropicCycle("five_hour")
	if cycle == nil {
		t.Fatal("Expected active cycle")
	}
	// Delta should be 0 (negative delta ignored)
	if cycle.TotalDelta != 0 {
		t.Errorf("TotalDelta = %v, want 0 (negative delta should be ignored)", cycle.TotalDelta)
	}
	// Peak should remain at 50 (highest seen)
	if cycle.PeakUtilization != 50.0 {
		t.Errorf("PeakUtilization = %v, want 50.0 (should retain previous peak)", cycle.PeakUtilization)
	}

	// Third snapshot: utilization goes back up to 55%
	snap3 := makeAnthropicSnapshot(baseTime.Add(2*time.Minute), "five_hour", 55.0, &resetsAt)
	if err := tracker.Process(snap3); err != nil {
		t.Fatalf("Process snap3 failed: %v", err)
	}

	cycle, _ = s.QueryActiveAnthropicCycle("five_hour")
	// Delta should be 25.0 (55 - 30, since last was 30)
	if cycle.TotalDelta != 25.0 {
		t.Errorf("TotalDelta = %v, want 25.0", cycle.TotalDelta)
	}
	// Peak should update to 55
	if cycle.PeakUtilization != 55.0 {
		t.Errorf("PeakUtilization = %v, want 55.0", cycle.PeakUtilization)
	}
}

func TestAnthropicTracker_OnResetCallback_Called(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	tracker := NewAnthropicTracker(s, nil)

	// Use atomic for thread safety (callback could theoretically be called from a goroutine)
	var callCount atomic.Int32
	var calledWith atomic.Value

	tracker.SetOnReset(func(quotaName string) {
		callCount.Add(1)
		calledWith.Store(quotaName)
	})

	baseTime := time.Now().Truncate(time.Second)
	resetsAt1 := baseTime.Add(5 * time.Hour)

	// First snapshot
	snap1 := makeAnthropicSnapshot(baseTime, "five_hour", 40.0, &resetsAt1)
	if err := tracker.Process(snap1); err != nil {
		t.Fatalf("Process snap1 failed: %v", err)
	}

	// No reset yet -- callback should not have been called
	if callCount.Load() != 0 {
		t.Errorf("onReset called %d times before reset, want 0", callCount.Load())
	}

	// Trigger reset
	resetsAt2 := baseTime.Add(10 * time.Hour)
	snap2 := makeAnthropicSnapshot(baseTime.Add(1*time.Minute), "five_hour", 5.0, &resetsAt2)
	if err := tracker.Process(snap2); err != nil {
		t.Fatalf("Process snap2 failed: %v", err)
	}

	if callCount.Load() != 1 {
		t.Errorf("onReset called %d times after reset, want 1", callCount.Load())
	}
	if got, ok := calledWith.Load().(string); !ok || got != "five_hour" {
		t.Errorf("onReset called with %q, want %q", got, "five_hour")
	}
}

func TestAnthropicTracker_UsageSummary_WithHistory(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	tracker := NewAnthropicTracker(s, nil)
	baseTime := time.Now().Add(-4 * time.Hour).Truncate(time.Second)
	resetsAt1 := baseTime.Add(2 * time.Hour)

	// -- Cycle 1: utilization goes from 10 to 40 (delta = 30) --
	snap1 := makeAnthropicSnapshot(baseTime, "five_hour", 10.0, &resetsAt1)
	tracker.Process(snap1)

	snap2 := makeAnthropicSnapshot(baseTime.Add(30*time.Minute), "five_hour", 40.0, &resetsAt1)
	tracker.Process(snap2)

	// Reset: closes cycle 1 (peak=40, delta=30)
	resetsAt2 := baseTime.Add(7 * time.Hour)
	snap3 := makeAnthropicSnapshot(baseTime.Add(1*time.Hour), "five_hour", 5.0, &resetsAt2)
	tracker.Process(snap3)

	// -- Cycle 2: utilization goes from 5 to 25 (delta = 20) --
	snap4 := makeAnthropicSnapshot(baseTime.Add(1*time.Hour+30*time.Minute), "five_hour", 25.0, &resetsAt2)
	tracker.Process(snap4)

	// Reset: closes cycle 2 (peak=25, delta=20)
	resetsAt3 := baseTime.Add(12 * time.Hour)
	snap5 := makeAnthropicSnapshot(baseTime.Add(2*time.Hour), "five_hour", 3.0, &resetsAt3)
	tracker.Process(snap5)

	// -- Cycle 3 (active): utilization goes from 3 to 15 --
	// Insert a snapshot into the store so QueryLatestAnthropic finds it
	snap6 := makeAnthropicSnapshot(baseTime.Add(2*time.Hour+30*time.Minute), "five_hour", 15.0, &resetsAt3)
	s.InsertAnthropicSnapshot(snap6)
	tracker.Process(snap6)

	summary, err := tracker.UsageSummary("five_hour")
	if err != nil {
		t.Fatalf("UsageSummary failed: %v", err)
	}

	// 2 completed cycles
	if summary.CompletedCycles != 2 {
		t.Errorf("CompletedCycles = %d, want 2", summary.CompletedCycles)
	}

	// AvgPerCycle should be (30 + 20) / 2 = 25.0
	if summary.AvgPerCycle != 25.0 {
		t.Errorf("AvgPerCycle = %v, want 25.0", summary.AvgPerCycle)
	}

	// PeakCycle should be 40.0 (from completed cycle 1)
	if summary.PeakCycle != 40.0 {
		t.Errorf("PeakCycle = %v, want 40.0", summary.PeakCycle)
	}

	// TotalTracked = completed (30 + 20) + active (12) = 62
	if summary.TotalTracked != 62.0 {
		t.Errorf("TotalTracked = %v, want 62.0", summary.TotalTracked)
	}

	// CurrentUtil should be 15.0 (latest snapshot)
	if summary.CurrentUtil != 15.0 {
		t.Errorf("CurrentUtil = %v, want 15.0", summary.CurrentUtil)
	}

	// ResetsAt should match resetsAt3
	if summary.ResetsAt == nil {
		t.Error("Expected ResetsAt to be set")
	}

	// QuotaName should match
	if summary.QuotaName != "five_hour" {
		t.Errorf("QuotaName = %q, want %q", summary.QuotaName, "five_hour")
	}
}

func TestAnthropicTracker_UsageSummary_NoCycles(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	tracker := NewAnthropicTracker(s, nil)

	summary, err := tracker.UsageSummary("five_hour")
	if err != nil {
		t.Fatalf("UsageSummary failed: %v", err)
	}

	if summary == nil {
		t.Fatal("Expected non-nil summary even with no data")
	}
	if summary.QuotaName != "five_hour" {
		t.Errorf("QuotaName = %q, want %q", summary.QuotaName, "five_hour")
	}
	if summary.CompletedCycles != 0 {
		t.Errorf("CompletedCycles = %d, want 0", summary.CompletedCycles)
	}
	if summary.CurrentUtil != 0 {
		t.Errorf("CurrentUtil = %v, want 0", summary.CurrentUtil)
	}
	if summary.CurrentRate != 0 {
		t.Errorf("CurrentRate = %v, want 0", summary.CurrentRate)
	}
	if summary.ProjectedUtil != 0 {
		t.Errorf("ProjectedUtil = %v, want 0", summary.ProjectedUtil)
	}
	if summary.AvgPerCycle != 0 {
		t.Errorf("AvgPerCycle = %v, want 0", summary.AvgPerCycle)
	}
	if summary.PeakCycle != 0 {
		t.Errorf("PeakCycle = %v, want 0", summary.PeakCycle)
	}
	if summary.TotalTracked != 0 {
		t.Errorf("TotalTracked = %v, want 0", summary.TotalTracked)
	}
	if summary.ResetsAt != nil {
		t.Errorf("ResetsAt = %v, want nil", summary.ResetsAt)
	}
}
