package tracker

import (
	"testing"
	"time"

	"github.com/onllm-dev/syntrack/internal/api"
	"github.com/onllm-dev/syntrack/internal/store"
)

func makeZaiSnapshot(capturedAt time.Time, tokensValue float64, timeValue float64, nextReset *time.Time) *api.ZaiSnapshot {
	return &api.ZaiSnapshot{
		CapturedAt:          capturedAt,
		TokensLimit:         200000000,
		TokensUnit:          1000000,
		TokensNumber:        200,
		TokensUsage:         200000000, // budget
		TokensCurrentValue:  tokensValue,
		TokensRemaining:     200000000 - tokensValue,
		TokensPercentage:    int((tokensValue / 200000000) * 100),
		TokensNextResetTime: nextReset,
		TimeLimit:           1000,
		TimeUnit:            100,
		TimeNumber:          10,
		TimeUsage:           1000, // budget
		TimeCurrentValue:    timeValue,
		TimeRemaining:       1000 - timeValue,
		TimePercentage:      int((timeValue / 1000) * 100),
	}
}

func TestZaiTracker_FirstSnapshot_CreatesTwoCycles(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tr := NewZaiTracker(s, nil)
	resetTime := time.Now().Add(24 * time.Hour)
	snapshot := makeZaiSnapshot(time.Now(), 50000, 100, &resetTime)

	err := tr.Process(snapshot)
	if err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	// Verify both cycles were created
	for _, quotaType := range []string{"tokens", "time"} {
		cycle, err := s.QueryActiveZaiCycle(quotaType)
		if err != nil {
			t.Fatalf("QueryActiveZaiCycle failed for %s: %v", quotaType, err)
		}
		if cycle == nil {
			t.Errorf("Expected active cycle for %s", quotaType)
		}
	}
}

func TestZaiTracker_TokensIncrement_UpdatesDelta(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tr := NewZaiTracker(s, nil)
	baseTime := time.Now()
	resetTime := baseTime.Add(24 * time.Hour)

	// First snapshot
	s1 := makeZaiSnapshot(baseTime, 50000, 100, &resetTime)
	tr.Process(s1)

	// Second snapshot — tokens increased
	s2 := makeZaiSnapshot(baseTime.Add(time.Minute), 80000, 150, &resetTime)
	err := tr.Process(s2)
	if err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	cycle, _ := s.QueryActiveZaiCycle("tokens")
	if cycle.TotalDelta != 30000 {
		t.Errorf("TotalDelta = %d, want 30000", cycle.TotalDelta)
	}
	if cycle.PeakValue != 80000 {
		t.Errorf("PeakValue = %d, want 80000", cycle.PeakValue)
	}
}

func TestZaiTracker_DetectsTokensReset(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tr := NewZaiTracker(s, nil)
	baseTime := time.Now()
	resetTime1 := baseTime.Add(24 * time.Hour)

	// First snapshot
	s1 := makeZaiSnapshot(baseTime, 50000, 100, &resetTime1)
	tr.Process(s1)

	// Second snapshot — different nextResetTime = reset
	resetTime2 := baseTime.Add(48 * time.Hour)
	s2 := makeZaiSnapshot(baseTime.Add(time.Minute), 1000, 110, &resetTime2)
	err := tr.Process(s2)
	if err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	// Check old cycle was closed
	history, _ := s.QueryZaiCycleHistory("tokens")
	if len(history) != 1 {
		t.Errorf("Expected 1 closed cycle, got %d", len(history))
	}

	// Check new cycle was created
	cycle, _ := s.QueryActiveZaiCycle("tokens")
	if cycle == nil {
		t.Fatal("Expected new active cycle")
	}
}

func TestZaiTracker_DetectsTimeReset_ValueDrop(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tr := NewZaiTracker(s, nil)
	baseTime := time.Now()
	resetTime := baseTime.Add(24 * time.Hour)

	// First snapshot with high time value
	s1 := makeZaiSnapshot(baseTime, 50000, 800, &resetTime)
	tr.Process(s1)

	// Second snapshot — time value drops >50% = reset
	s2 := makeZaiSnapshot(baseTime.Add(time.Minute), 55000, 100, &resetTime)
	err := tr.Process(s2)
	if err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	// Check old cycle was closed
	history, _ := s.QueryZaiCycleHistory("time")
	if len(history) != 1 {
		t.Errorf("Expected 1 closed time cycle, got %d", len(history))
	}

	// Tokens cycle should still be active (no tokens reset)
	tokensCycle, _ := s.QueryActiveZaiCycle("tokens")
	if tokensCycle == nil {
		t.Error("Tokens cycle should still be active")
	}
}

func TestZaiTracker_NegativeDelta_Ignored(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tr := NewZaiTracker(s, nil)
	baseTime := time.Now()
	resetTime := baseTime.Add(24 * time.Hour)

	// First snapshot with high value
	s1 := makeZaiSnapshot(baseTime, 80000, 500, &resetTime)
	tr.Process(s1)

	// Second snapshot — slight drop (not enough for reset, within same cycle)
	s2 := makeZaiSnapshot(baseTime.Add(time.Minute), 75000, 490, &resetTime)
	tr.Process(s2)

	// Delta should be 0 (not negative)
	cycle, _ := s.QueryActiveZaiCycle("tokens")
	if cycle.TotalDelta != 0 {
		t.Errorf("TotalDelta = %d, want 0", cycle.TotalDelta)
	}
	// Peak should remain at 80000
	if cycle.PeakValue != 80000 {
		t.Errorf("PeakValue = %d, want 80000", cycle.PeakValue)
	}
}

func TestZaiTracker_PeakTracking(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tr := NewZaiTracker(s, nil)
	baseTime := time.Now()
	resetTime := baseTime.Add(24 * time.Hour)

	values := []float64{10000, 20000, 15000, 30000, 25000, 40000}

	for i, v := range values {
		snap := makeZaiSnapshot(baseTime.Add(time.Duration(i)*time.Minute), v, float64(i*10), &resetTime)
		tr.Process(snap)
	}

	cycle, _ := s.QueryActiveZaiCycle("tokens")
	if cycle.PeakValue != 40000 {
		t.Errorf("PeakValue = %d, want 40000", cycle.PeakValue)
	}
}

func TestZaiTracker_UsageSummary_NoCycles(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tr := NewZaiTracker(s, nil)

	summary, err := tr.UsageSummary("tokens")
	if err != nil {
		t.Fatalf("UsageSummary failed: %v", err)
	}

	if summary.QuotaType != "tokens" {
		t.Errorf("QuotaType = %q, want 'tokens'", summary.QuotaType)
	}
	if summary.CompletedCycles != 0 {
		t.Errorf("CompletedCycles = %d, want 0", summary.CompletedCycles)
	}
}

func TestZaiTracker_UsageSummary_WithCompletedCycle(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tr := NewZaiTracker(s, nil)
	baseTime := time.Now()
	resetTime1 := baseTime.Add(24 * time.Hour)

	// Create a cycle
	s1 := makeZaiSnapshot(baseTime, 50000, 100, &resetTime1)
	tr.Process(s1)

	s2 := makeZaiSnapshot(baseTime.Add(time.Minute), 100000, 200, &resetTime1)
	tr.Process(s2)

	// Trigger reset
	resetTime2 := baseTime.Add(48 * time.Hour)
	s3 := makeZaiSnapshot(baseTime.Add(2*time.Minute), 5000, 210, &resetTime2)
	// Also insert the snapshot so QueryLatestZai works
	s.InsertZaiSnapshot(s3)
	tr.Process(s3)

	summary, err := tr.UsageSummary("tokens")
	if err != nil {
		t.Fatalf("UsageSummary failed: %v", err)
	}

	if summary.CompletedCycles != 1 {
		t.Errorf("CompletedCycles = %d, want 1", summary.CompletedCycles)
	}
	if summary.CurrentUsage != 5000 {
		t.Errorf("CurrentUsage = %v, want 5000", summary.CurrentUsage)
	}
}
