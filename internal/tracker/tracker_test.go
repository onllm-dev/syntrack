package tracker

import (
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
)

func TestTracker_FirstSnapshot_CreatesThreeCycles(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tracker := New(s, nil)
	snapshot := &api.Snapshot{
		CapturedAt: time.Now(),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 100, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 500, RenewsAt: time.Now().Add(3 * time.Hour)},
	}

	err := tracker.Process(snapshot)
	if err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	// Verify all three cycles were created
	for _, quotaType := range []string{"subscription", "search", "toolcall"} {
		cycle, err := s.QueryActiveCycle(quotaType)
		if err != nil {
			t.Fatalf("QueryActiveCycle failed for %s: %v", quotaType, err)
		}
		if cycle == nil {
			t.Errorf("Expected active cycle for %s", quotaType)
		}
	}
}

func TestTracker_NormalIncrement_UpdatesDelta(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tracker := New(s, nil)
	baseTime := time.Now()

	// First snapshot
	snapshot1 := &api.Snapshot{
		CapturedAt: baseTime,
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 100, RenewsAt: baseTime.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: baseTime.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 500, RenewsAt: baseTime.Add(3 * time.Hour)},
	}
	tracker.Process(snapshot1)

	// Second snapshot - requests increased
	snapshot2 := &api.Snapshot{
		CapturedAt: baseTime.Add(1 * time.Minute),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 150, RenewsAt: baseTime.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 15, RenewsAt: baseTime.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 600, RenewsAt: baseTime.Add(3 * time.Hour)},
	}
	err := tracker.Process(snapshot2)
	if err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	// Check cycle was updated with delta
	cycle, err := s.QueryActiveCycle("subscription")
	if err != nil {
		t.Fatalf("QueryActiveCycle failed: %v", err)
	}
	if cycle.TotalDelta != 50 {
		t.Errorf("TotalDelta = %v, want 50", cycle.TotalDelta)
	}
	if cycle.PeakRequests != 150 {
		t.Errorf("PeakRequests = %v, want 150", cycle.PeakRequests)
	}
}

func TestTracker_DetectsSubscriptionReset(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tracker := New(s, nil)
	baseTime := time.Now()

	// First snapshot
	snapshot1 := &api.Snapshot{
		CapturedAt: baseTime,
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 100, RenewsAt: baseTime.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: baseTime.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 500, RenewsAt: baseTime.Add(3 * time.Hour)},
	}
	tracker.Process(snapshot1)

	// Second snapshot - subscription reset (renewsAt changed)
	snapshot2 := &api.Snapshot{
		CapturedAt: baseTime.Add(1 * time.Minute),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 10, RenewsAt: baseTime.Add(10 * time.Hour)}, // New reset time
		Search:     api.QuotaInfo{Limit: 250, Requests: 15, RenewsAt: baseTime.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 600, RenewsAt: baseTime.Add(3 * time.Hour)},
	}
	err := tracker.Process(snapshot2)
	if err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	// Check old cycle was closed
	history, err := s.QueryCycleHistory("subscription")
	if err != nil {
		t.Fatalf("QueryCycleHistory failed: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("Expected 1 closed cycle, got %d", len(history))
	}

	// Check new cycle was created
	cycle, err := s.QueryActiveCycle("subscription")
	if err != nil {
		t.Fatalf("QueryActiveCycle failed: %v", err)
	}
	if cycle == nil {
		t.Fatal("Expected new active cycle")
	}
	if cycle.TotalDelta != 0 {
		t.Errorf("New cycle TotalDelta = %v, want 0", cycle.TotalDelta)
	}
}

func TestTracker_DetectsSearchReset(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tracker := New(s, nil)
	baseTime := time.Now()

	// First snapshot
	snapshot1 := &api.Snapshot{
		CapturedAt: baseTime,
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 100, RenewsAt: baseTime.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: baseTime.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 500, RenewsAt: baseTime.Add(3 * time.Hour)},
	}
	tracker.Process(snapshot1)

	// Second snapshot - search reset
	snapshot2 := &api.Snapshot{
		CapturedAt: baseTime.Add(1 * time.Minute),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 110, RenewsAt: baseTime.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: baseTime.Add(2 * time.Hour)}, // Reset
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 550, RenewsAt: baseTime.Add(3 * time.Hour)},
	}
	tracker.Process(snapshot2)

	// Verify only search cycle was closed, others still active
	history, _ := s.QueryCycleHistory("search")
	if len(history) != 1 {
		t.Errorf("Expected 1 closed search cycle, got %d", len(history))
	}

	// Verify subscription and toolcall still have active cycles
	_, err := s.QueryActiveCycle("subscription")
	if err != nil {
		t.Errorf("Subscription cycle should still be active: %v", err)
	}
	_, err = s.QueryActiveCycle("toolcall")
	if err != nil {
		t.Errorf("Toolcall cycle should still be active: %v", err)
	}
}

func TestTracker_RequestsDropToZero(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tracker := New(s, nil)
	baseTime := time.Now()

	// First snapshot with high requests
	snapshot1 := &api.Snapshot{
		CapturedAt: baseTime,
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 500, RenewsAt: baseTime.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: baseTime.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 500, RenewsAt: baseTime.Add(3 * time.Hour)},
	}
	tracker.Process(snapshot1)

	// Second snapshot - requests dropped but renewsAt same (anomaly, not reset)
	snapshot2 := &api.Snapshot{
		CapturedAt: baseTime.Add(1 * time.Minute),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 0, RenewsAt: baseTime.Add(5 * time.Hour)}, // Same renewsAt
		Search:     api.QuotaInfo{Limit: 250, Requests: 15, RenewsAt: baseTime.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 550, RenewsAt: baseTime.Add(3 * time.Hour)},
	}
	tracker.Process(snapshot2)

	// Delta should be 0 (not negative)
	cycle, _ := s.QueryActiveCycle("subscription")
	if cycle.TotalDelta != 0 {
		t.Errorf("TotalDelta = %v, want 0 (delta should never be negative)", cycle.TotalDelta)
	}
	// Peak should remain at 500
	if cycle.PeakRequests != 500 {
		t.Errorf("PeakRequests = %v, want 500", cycle.PeakRequests)
	}
}

func TestTracker_PeakTracking(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tracker := New(s, nil)
	baseTime := time.Now()

	// Requests go up and down
	requests := []float64{100, 200, 150, 250, 200, 300}

	for i, req := range requests {
		snapshot := &api.Snapshot{
			CapturedAt: baseTime.Add(time.Duration(i) * time.Minute),
			Sub:        api.QuotaInfo{Limit: 1000, Requests: req, RenewsAt: baseTime.Add(5 * time.Hour)},
			Search:     api.QuotaInfo{Limit: 250, Requests: float64(i * 5), RenewsAt: baseTime.Add(1 * time.Hour)},
			ToolCall:   api.QuotaInfo{Limit: 5000, Requests: float64(i * 10), RenewsAt: baseTime.Add(3 * time.Hour)},
		}
		tracker.Process(snapshot)
	}

	cycle, _ := s.QueryActiveCycle("subscription")
	if cycle.PeakRequests != 300 {
		t.Errorf("PeakRequests = %v, want 300", cycle.PeakRequests)
	}
	if cycle.TotalDelta != 300 {
		t.Errorf("TotalDelta = %v, want 300", cycle.TotalDelta)
	}
}

func TestTracker_UsageSummary_NoCycles(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tracker := New(s, nil)

	summary, err := tracker.UsageSummary("subscription")
	if err != nil {
		t.Fatalf("UsageSummary failed: %v", err)
	}

	if summary.QuotaType != "subscription" {
		t.Errorf("QuotaType = %q, want 'subscription'", summary.QuotaType)
	}
	if summary.CompletedCycles != 0 {
		t.Errorf("CompletedCycles = %d, want 0", summary.CompletedCycles)
	}
}

func TestTracker_UsageSummary_SingleCycle(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tracker := New(s, nil)
	baseTime := time.Now()

	// Create a completed cycle
	snapshot1 := &api.Snapshot{
		CapturedAt: baseTime,
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 100, RenewsAt: baseTime.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: baseTime.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 100, RenewsAt: baseTime.Add(3 * time.Hour)},
	}
	tracker.Process(snapshot1)

	// Close the cycle by triggering a reset
	snapshot2 := &api.Snapshot{
		CapturedAt: baseTime.Add(1 * time.Minute),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 200, RenewsAt: baseTime.Add(10 * time.Hour)}, // Reset
		Search:     api.QuotaInfo{Limit: 250, Requests: 15, RenewsAt: baseTime.Add(2 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 150, RenewsAt: baseTime.Add(6 * time.Hour)},
	}
	tracker.Process(snapshot2)

	summary, err := tracker.UsageSummary("subscription")
	if err != nil {
		t.Fatalf("UsageSummary failed: %v", err)
	}

	if summary.CompletedCycles != 1 {
		t.Errorf("CompletedCycles = %d, want 1", summary.CompletedCycles)
	}
	if summary.AvgPerCycle != 100 {
		t.Errorf("AvgPerCycle = %v, want 100", summary.AvgPerCycle)
	}
	if summary.PeakCycle != 100 { // Peak in completed cycle
		t.Errorf("PeakCycle = %v, want 100", summary.PeakCycle)
	}
	if summary.TrackingSince.IsZero() {
		t.Fatal("expected TrackingSince to be set")
	}
}

func TestTracker_UsageSummary_MultipleCycles(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tracker := New(s, nil)
	baseTime := time.Now()
	fixedSearchRenew := baseTime.Add(100 * time.Hour)
	fixedToolRenew := baseTime.Add(100 * time.Hour)

	// Create first cycle with delta 100
	tracker.Process(&api.Snapshot{
		CapturedAt: baseTime,
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 100, RenewsAt: baseTime.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: fixedSearchRenew},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 0, RenewsAt: fixedToolRenew},
	})
	tracker.Process(&api.Snapshot{
		CapturedAt: baseTime.Add(1 * time.Hour),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 200, RenewsAt: baseTime.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: fixedSearchRenew},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 0, RenewsAt: fixedToolRenew},
	})

	// Trigger reset and create second cycle with delta 200
	tracker.Process(&api.Snapshot{
		CapturedAt: baseTime.Add(2 * time.Hour),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 0, RenewsAt: baseTime.Add(10 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: fixedSearchRenew},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 0, RenewsAt: fixedToolRenew},
	})
	tracker.Process(&api.Snapshot{
		CapturedAt: baseTime.Add(3 * time.Hour),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 200, RenewsAt: baseTime.Add(10 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: fixedSearchRenew},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 0, RenewsAt: fixedToolRenew},
	})

	// Trigger reset and create third cycle with delta 150
	tracker.Process(&api.Snapshot{
		CapturedAt: baseTime.Add(4 * time.Hour),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 0, RenewsAt: baseTime.Add(15 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: fixedSearchRenew},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 0, RenewsAt: fixedToolRenew},
	})
	tracker.Process(&api.Snapshot{
		CapturedAt: baseTime.Add(5 * time.Hour),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 150, RenewsAt: baseTime.Add(15 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: fixedSearchRenew},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 0, RenewsAt: fixedToolRenew},
	})

	summary, _ := tracker.UsageSummary("subscription")

	// 2 completed cycles (first and second), 1 active (third)
	if summary.CompletedCycles != 2 {
		t.Errorf("CompletedCycles = %d, want 2", summary.CompletedCycles)
	}
	expectedAvg := (100.0 + 200.0) / 2.0
	if summary.AvgPerCycle != expectedAvg {
		t.Errorf("AvgPerCycle = %v, want %v", summary.AvgPerCycle, expectedAvg)
	}
	if summary.PeakCycle != 200 {
		t.Errorf("PeakCycle = %v, want 200", summary.PeakCycle)
	}
}

func TestTracker_MinuteLevelJitter_IgnoredAsNonReset(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tracker := New(s, nil)
	baseTime := time.Now().Truncate(time.Hour) // Start at hour boundary

	// First snapshot with renewsAt at +5:00
	snapshot1 := &api.Snapshot{
		CapturedAt: baseTime,
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 100, RenewsAt: baseTime.Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: baseTime.Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 500, RenewsAt: baseTime.Add(3 * time.Hour)},
	}
	tracker.Process(snapshot1)

	// Simulate rolling window: renewsAt shifts forward by poll interval (1 minute)
	// This is how Synthetic API's search quota works - it returns "now + 1 hour"
	// on each poll, so renewsAt increments by 1 minute each time.
	// This should NOT trigger a new cycle (compare at hour precision).
	for i := 1; i <= 30; i++ { // Simulate 30 polls over 30 minutes
		snapshot := &api.Snapshot{
			CapturedAt: baseTime.Add(time.Duration(i) * time.Minute),
			Sub:        api.QuotaInfo{Limit: 1000, Requests: 100 + float64(i)*2, RenewsAt: baseTime.Add(5*time.Hour + time.Duration(i)*time.Minute)},
			Search:     api.QuotaInfo{Limit: 250, Requests: 10 + float64(i), RenewsAt: baseTime.Add(1*time.Hour + time.Duration(i)*time.Minute)},
			ToolCall:   api.QuotaInfo{Limit: 5000, Requests: 500 + float64(i)*5, RenewsAt: baseTime.Add(3*time.Hour + time.Duration(i)*time.Minute)},
		}
		tracker.Process(snapshot)
	}

	// Verify cycles were NOT closed (still active, all within same hour)
	for _, quotaType := range []string{"subscription", "search", "toolcall"} {
		cycle, _ := s.QueryActiveCycle(quotaType)
		if cycle == nil {
			t.Errorf("Expected active cycle for %s to still be open", quotaType)
			continue
		}
		// Verify no completed cycles (history should be empty)
		history, _ := s.QueryCycleHistory(quotaType)
		if len(history) != 0 {
			t.Errorf("Expected 0 completed cycles for %s, got %d (minute-level jitter caused false reset)", quotaType, len(history))
		}
	}

	// Verify delta was calculated correctly (should accumulate across all 30 polls)
	subCycle, _ := s.QueryActiveCycle("subscription")
	expectedDelta := float64(30 * 2) // 2 per poll for 30 polls
	if subCycle.TotalDelta != expectedDelta {
		t.Errorf("TotalDelta = %v, want %v", subCycle.TotalDelta, expectedDelta)
	}
}
