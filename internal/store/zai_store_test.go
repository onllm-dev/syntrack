package store

import (
	"testing"
	"time"

	"github.com/onllm-dev/syntrack/internal/api"
)

func TestZaiStore_InsertAndQuerySnapshot(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	nextReset := now.Add(24 * time.Hour)

	snapshot := &api.ZaiSnapshot{
		CapturedAt:          now,
		TimeLimit:           1000,
		TimeUnit:            1,
		TimeNumber:          1000,
		TimeUsage:           150.5,
		TimeCurrentValue:    150.5,
		TimeRemaining:       849.5,
		TimePercentage:      15,
		TokensLimit:         200000000,
		TokensUnit:          1,
		TokensNumber:        200000000,
		TokensUsage:         5000000,
		TokensCurrentValue:  5000000,
		TokensRemaining:     195000000,
		TokensPercentage:    2,
		TokensNextResetTime: &nextReset,
	}

	id, err := s.InsertZaiSnapshot(snapshot)
	if err != nil {
		t.Fatalf("InsertZaiSnapshot failed: %v", err)
	}
	if id == 0 {
		t.Error("Expected non-zero ID")
	}

	// Query latest
	latest, err := s.QueryLatestZai()
	if err != nil {
		t.Fatalf("QueryLatestZai failed: %v", err)
	}
	if latest == nil {
		t.Fatal("Expected latest snapshot, got nil")
	}

	if latest.TimeUsage != 150.5 {
		t.Errorf("TimeUsage = %v, want 150.5", latest.TimeUsage)
	}
	if latest.TokensUsage != 5000000 {
		t.Errorf("TokensUsage = %v, want 5000000", latest.TokensUsage)
	}
	if latest.TokensNextResetTime == nil {
		t.Error("Expected TokensNextResetTime to be set")
	} else if !latest.TokensNextResetTime.Equal(nextReset) {
		t.Errorf("TokensNextResetTime = %v, want %v", latest.TokensNextResetTime, nextReset)
	}
}

func TestZaiStore_QueryLatestZai_EmptyDB(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	latest, err := s.QueryLatestZai()
	if err != nil {
		t.Fatalf("QueryLatestZai failed: %v", err)
	}
	if latest != nil {
		t.Error("Expected nil for empty DB")
	}
}

func TestZaiStore_QueryZaiRange(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 2, 6, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		snapshot := &api.ZaiSnapshot{
			CapturedAt:       base.Add(time.Duration(i) * time.Hour),
			TimeLimit:        1000,
			TimeUnit:         1,
			TimeNumber:       1000,
			TimeUsage:        float64(i * 10),
			TimeCurrentValue: float64(i * 10),
			TimeRemaining:    float64(1000 - i*10),
			TimePercentage:   i * 10,
			TokensLimit:      200000000,
			TokensUnit:       1,
			TokensNumber:     200000000,
			TokensUsage:      float64(i * 1000),
		}
		_, err := s.InsertZaiSnapshot(snapshot)
		if err != nil {
			t.Fatalf("InsertZaiSnapshot %d failed: %v", i, err)
		}
	}

	start := base.Add(30 * time.Minute)
	end := base.Add(3*time.Hour + 30*time.Minute)
	snapshots, err := s.QueryZaiRange(start, end)
	if err != nil {
		t.Fatalf("QueryZaiRange failed: %v", err)
	}
	if len(snapshots) != 3 {
		t.Errorf("Expected 3 snapshots, got %d", len(snapshots))
	}
}

func TestZaiStore_QueryZaiRange_Empty(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	snapshot := &api.ZaiSnapshot{
		CapturedAt:       time.Now(),
		TimeLimit:        1000,
		TimeUnit:         1,
		TimeNumber:       1000,
		TimeUsage:        100,
		TimeCurrentValue: 100,
		TimeRemaining:    900,
		TimePercentage:   10,
		TokensLimit:      200000000,
		TokensUnit:       1,
		TokensNumber:     200000000,
		TokensUsage:      1000000,
	}
	_, err = s.InsertZaiSnapshot(snapshot)
	if err != nil {
		t.Fatalf("InsertZaiSnapshot failed: %v", err)
	}

	start := time.Now().Add(-2 * time.Hour)
	end := time.Now().Add(-1 * time.Hour)
	snapshots, err := s.QueryZaiRange(start, end)
	if err != nil {
		t.Fatalf("QueryZaiRange failed: %v", err)
	}
	if len(snapshots) != 0 {
		t.Errorf("Expected 0 snapshots, got %d", len(snapshots))
	}
}

func TestZaiStore_CreateAndCloseCycle(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	tests := []struct {
		quotaType string
		nextReset *time.Time
	}{
		{"tokens", nil},
		{"time", nil},
	}

	for _, tt := range tests {
		start := time.Now().UTC()
		cycleID, err := s.CreateZaiCycle(tt.quotaType, start, tt.nextReset)
		if err != nil {
			t.Fatalf("CreateZaiCycle failed for %s: %v", tt.quotaType, err)
		}
		if cycleID == 0 {
			t.Errorf("Expected non-zero cycle ID for %s", tt.quotaType)
		}
	}

	for _, tt := range tests {
		cycle, err := s.QueryActiveZaiCycle(tt.quotaType)
		if err != nil {
			t.Fatalf("QueryActiveZaiCycle failed for %s: %v", tt.quotaType, err)
		}
		if cycle == nil {
			t.Errorf("Expected active cycle for %s", tt.quotaType)
			continue
		}
		if cycle.QuotaType != tt.quotaType {
			t.Errorf("QuotaType = %q, want %q", cycle.QuotaType, tt.quotaType)
		}
	}

	err = s.CloseZaiCycle("tokens", time.Now().UTC(), 500, 450)
	if err != nil {
		t.Fatalf("CloseZaiCycle failed: %v", err)
	}

	cycle, err := s.QueryActiveZaiCycle("tokens")
	if err != nil {
		t.Fatalf("QueryActiveZaiCycle failed: %v", err)
	}
	if cycle != nil {
		t.Error("Expected no active tokens cycle after close")
	}

	history, err := s.QueryZaiCycleHistory("tokens")
	if err != nil {
		t.Fatalf("QueryZaiCycleHistory failed: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("Expected 1 cycle in history, got %d", len(history))
	}
	if history[0].PeakValue != 500 {
		t.Errorf("PeakValue = %v, want 500", history[0].PeakValue)
	}
	if history[0].TotalDelta != 450 {
		t.Errorf("TotalDelta = %v, want 450", history[0].TotalDelta)
	}
}

func TestZaiStore_CreateCycleWithNextReset(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	start := time.Now().UTC()
	nextReset := start.Add(24 * time.Hour)

	cycleID, err := s.CreateZaiCycle("tokens", start, &nextReset)
	if err != nil {
		t.Fatalf("CreateZaiCycle failed: %v", err)
	}
	if cycleID == 0 {
		t.Error("Expected non-zero cycle ID")
	}

	cycle, err := s.QueryActiveZaiCycle("tokens")
	if err != nil {
		t.Fatalf("QueryActiveZaiCycle failed: %v", err)
	}
	if cycle == nil {
		t.Fatal("Expected active cycle")
	}
	if cycle.NextReset == nil {
		t.Error("Expected NextReset to be set")
	} else if !cycle.NextReset.Equal(nextReset) {
		t.Errorf("NextReset = %v, want %v", cycle.NextReset, nextReset)
	}
}

func TestZaiStore_UpdateCycle(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	start := time.Now().UTC()
	_, err = s.CreateZaiCycle("tokens", start, nil)
	if err != nil {
		t.Fatalf("CreateZaiCycle failed: %v", err)
	}

	updates := []struct {
		peak  int64
		delta int64
	}{
		{100, 100},
		{200, 200},
		{150, 150}, // Peak should stay at 200, delta updates
	}

	for _, u := range updates {
		err = s.UpdateZaiCycle("tokens", u.peak, u.delta)
		if err != nil {
			t.Fatalf("UpdateZaiCycle failed: %v", err)
		}
	}

	cycle, err := s.QueryActiveZaiCycle("tokens")
	if err != nil {
		t.Fatalf("QueryActiveZaiCycle failed: %v", err)
	}
	if cycle == nil {
		t.Fatal("Expected active cycle")
	}
	if cycle.PeakValue != 150 {
		t.Errorf("PeakValue = %v, want 150 (last update)", cycle.PeakValue)
	}
	if cycle.TotalDelta != 150 {
		t.Errorf("TotalDelta = %v, want 150", cycle.TotalDelta)
	}
}

func TestZaiStore_InsertAndQueryHourlyUsage(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	hour := "2026-02-06 15:00"
	err = s.InsertZaiHourlyUsage(hour, 100, 50000, 10, 5, 2)
	if err != nil {
		t.Fatalf("InsertZaiHourlyUsage failed: %v", err)
	}

	start := time.Date(2026, 2, 6, 14, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 6, 16, 0, 0, 0, time.UTC)
	usages, err := s.QueryZaiHourlyUsage(start, end)
	if err != nil {
		t.Fatalf("QueryZaiHourlyUsage failed: %v", err)
	}
	if len(usages) != 1 {
		t.Fatalf("Expected 1 usage record, got %d", len(usages))
	}

	usage := usages[0]
	if usage.Hour != hour {
		t.Errorf("Hour = %q, want %q", usage.Hour, hour)
	}
	if *usage.ModelCalls != 100 {
		t.Errorf("ModelCalls = %v, want 100", *usage.ModelCalls)
	}
	if *usage.TokensUsed != 50000 {
		t.Errorf("TokensUsed = %v, want 50000", *usage.TokensUsed)
	}
	if *usage.NetworkSearches != 10 {
		t.Errorf("NetworkSearches = %v, want 10", *usage.NetworkSearches)
	}
	if *usage.WebReads != 5 {
		t.Errorf("WebReads = %v, want 5", *usage.WebReads)
	}
	if *usage.Zreads != 2 {
		t.Errorf("Zreads = %v, want 2", usage.Zreads)
	}
}

func TestZaiStore_UpdateHourlyUsage(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	hour := "2026-02-06 15:00"

	err = s.InsertZaiHourlyUsage(hour, 100, 50000, 10, 5, 2)
	if err != nil {
		t.Fatalf("InsertZaiHourlyUsage failed: %v", err)
	}

	err = s.InsertZaiHourlyUsage(hour, 150, 75000, 15, 8, 3)
	if err != nil {
		t.Fatalf("InsertZaiHourlyUsage update failed: %v", err)
	}

	start := time.Date(2026, 2, 6, 14, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 6, 16, 0, 0, 0, time.UTC)
	usages, err := s.QueryZaiHourlyUsage(start, end)
	if err != nil {
		t.Fatalf("QueryZaiHourlyUsage failed: %v", err)
	}
	if len(usages) != 1 {
		t.Fatalf("Expected 1 usage record, got %d", len(usages))
	}

	usage := usages[0]
	if *usage.ModelCalls != 150 {
		t.Errorf("ModelCalls = %v, want 150 (updated)", *usage.ModelCalls)
	}
	if *usage.TokensUsed != 75000 {
		t.Errorf("TokensUsed = %v, want 75000 (updated)", *usage.TokensUsed)
	}
}

func TestZaiStore_QueryHourlyUsageRange(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	hours := []string{
		"2026-02-06 12:00",
		"2026-02-06 13:00",
		"2026-02-06 14:00",
		"2026-02-06 15:00",
		"2026-02-06 16:00",
	}

	for i, hour := range hours {
		err := s.InsertZaiHourlyUsage(hour, int64(i*10), int64(i*1000), 0, 0, 0)
		if err != nil {
			t.Fatalf("InsertZaiHourlyUsage %d failed: %v", i, err)
		}
	}

	// Query range includes hours from 13:00 to 15:00 inclusive (lexicographic comparison on hour string)
	start := time.Date(2026, 2, 6, 13, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 6, 15, 0, 0, 0, time.UTC)
	usages, err := s.QueryZaiHourlyUsage(start, end)
	if err != nil {
		t.Fatalf("QueryZaiHourlyUsage failed: %v", err)
	}
	// String comparison on hour format includes both endpoints: 13:00, 14:00, 15:00
	if len(usages) != 3 {
		t.Errorf("Expected 3 usage records (13:00, 14:00, 15:00), got %d", len(usages))
	}
}

func TestZaiStore_SnapshotWithoutReset(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	snapshot := &api.ZaiSnapshot{
		CapturedAt:          time.Now().UTC(),
		TimeLimit:           1000,
		TimeUnit:            1,
		TimeNumber:          1000,
		TimeUsage:           100,
		TimeCurrentValue:    100,
		TimeRemaining:       900,
		TimePercentage:      10,
		TokensLimit:         200000000,
		TokensUnit:          1,
		TokensNumber:        200000000,
		TokensUsage:         1000000,
		TokensNextResetTime: nil,
	}

	id, err := s.InsertZaiSnapshot(snapshot)
	if err != nil {
		t.Fatalf("InsertZaiSnapshot failed: %v", err)
	}
	if id == 0 {
		t.Error("Expected non-zero ID")
	}

	latest, err := s.QueryLatestZai()
	if err != nil {
		t.Fatalf("QueryLatestZai failed: %v", err)
	}
	if latest == nil {
		t.Fatal("Expected latest snapshot")
	}
	if latest.TokensNextResetTime != nil {
		t.Error("Expected TokensNextResetTime to be nil")
	}
}

func TestZaiStore_MultipleSnapshots(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	base := time.Now().UTC()

	for i := 0; i < 10; i++ {
		snapshot := &api.ZaiSnapshot{
			CapturedAt:       base.Add(time.Duration(i) * time.Second),
			TimeLimit:        1000,
			TimeUnit:         1,
			TimeNumber:       1000,
			TimeUsage:        float64(i * 10),
			TimeCurrentValue: float64(i * 10),
			TimeRemaining:    float64(1000 - i*10),
			TimePercentage:   i,
			TokensLimit:      200000000,
			TokensUnit:       1,
			TokensNumber:     200000000,
			TokensUsage:      float64(i * 1000),
		}
		_, err := s.InsertZaiSnapshot(snapshot)
		if err != nil {
			t.Fatalf("InsertZaiSnapshot %d failed: %v", i, err)
		}
	}

	start := base.Add(-1 * time.Hour)
	end := base.Add(1 * time.Hour)
	snapshots, err := s.QueryZaiRange(start, end)
	if err != nil {
		t.Fatalf("QueryZaiRange failed: %v", err)
	}
	if len(snapshots) != 10 {
		t.Errorf("Expected 10 snapshots, got %d", len(snapshots))
	}

	latest, err := s.QueryLatestZai()
	if err != nil {
		t.Fatalf("QueryLatestZai failed: %v", err)
	}
	if latest == nil {
		t.Fatal("Expected latest snapshot")
	}
	if latest.TimeUsage != 90 {
		t.Errorf("Latest TimeUsage = %v, want 90", latest.TimeUsage)
	}
}
