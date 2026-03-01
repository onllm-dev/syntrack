package store

import (
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
)

func newTestCodexSnapshot(capturedAt time.Time, resetsAt *time.Time) *api.CodexSnapshot {
	return &api.CodexSnapshot{
		CapturedAt: capturedAt,
		PlanType:   "pro",
		RawJSON:    `{"test":true}`,
		Quotas: []api.CodexQuota{
			{Name: "five_hour", Utilization: 22.5, ResetsAt: resetsAt, Status: "healthy"},
			{Name: "seven_day", Utilization: 41.0, ResetsAt: resetsAt, Status: "healthy"},
		},
	}
}

func TestCodexStore_InsertAndQueryLatest(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetsAt := now.Add(5 * time.Hour)
	snap := newTestCodexSnapshot(now, &resetsAt)

	id, err := s.InsertCodexSnapshot(snap)
	if err != nil {
		t.Fatalf("InsertCodexSnapshot: %v", err)
	}
	if id <= 0 {
		t.Fatalf("id = %d, want > 0", id)
	}

	latest, err := s.QueryLatestCodex()
	if err != nil {
		t.Fatalf("QueryLatestCodex: %v", err)
	}
	if latest == nil {
		t.Fatal("QueryLatestCodex returned nil")
	}
	if latest.PlanType != "pro" {
		t.Fatalf("PlanType = %q, want pro", latest.PlanType)
	}
	if len(latest.Quotas) != 2 {
		t.Fatalf("len(Quotas) = %d, want 2", len(latest.Quotas))
	}
	if latest.Quotas[0].Name != "five_hour" {
		t.Fatalf("Quotas[0].Name = %q, want five_hour", latest.Quotas[0].Name)
	}
}

func TestCodexStore_QueryRange(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	for i := range 4 {
		resetsAt := now.Add(5 * time.Hour)
		snap := newTestCodexSnapshot(now.Add(time.Duration(i)*time.Minute), &resetsAt)
		snap.Quotas[0].Utilization = float64(10 + i)
		if _, err := s.InsertCodexSnapshot(snap); err != nil {
			t.Fatalf("InsertCodexSnapshot[%d]: %v", i, err)
		}
	}

	rows, err := s.QueryCodexRange(now.Add(-time.Minute), now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("QueryCodexRange: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("len(rows) = %d, want 4", len(rows))
	}

	limited, err := s.QueryCodexRange(now.Add(-time.Minute), now.Add(10*time.Minute), 2)
	if err != nil {
		t.Fatalf("QueryCodexRange(limit): %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("len(limited) = %d, want 2", len(limited))
	}

	if !limited[0].CapturedAt.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("expected first limited snapshot at t+2m, got %s", limited[0].CapturedAt)
	}
	if !limited[1].CapturedAt.Equal(now.Add(3 * time.Minute)) {
		t.Fatalf("expected second limited snapshot at t+3m, got %s", limited[1].CapturedAt)
	}
}

func TestCodexStore_CyclesAndSeries(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetsAt := now.Add(5 * time.Hour)

	id, err := s.CreateCodexCycle("five_hour", now, &resetsAt)
	if err != nil {
		t.Fatalf("CreateCodexCycle: %v", err)
	}
	if id <= 0 {
		t.Fatalf("cycle id = %d, want > 0", id)
	}

	if err := s.UpdateCodexCycle("five_hour", 35.0, 12.5); err != nil {
		t.Fatalf("UpdateCodexCycle: %v", err)
	}

	active, err := s.QueryActiveCodexCycle("five_hour")
	if err != nil {
		t.Fatalf("QueryActiveCodexCycle: %v", err)
	}
	if active == nil {
		t.Fatal("active cycle is nil")
	}
	if active.PeakUtilization != 35.0 {
		t.Fatalf("PeakUtilization = %.1f, want 35.0", active.PeakUtilization)
	}

	end := now.Add(time.Hour)
	if err := s.CloseCodexCycle("five_hour", end, 44.0, 18.0); err != nil {
		t.Fatalf("CloseCodexCycle: %v", err)
	}

	history, err := s.QueryCodexCycleHistory("five_hour")
	if err != nil {
		t.Fatalf("QueryCodexCycleHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("len(history) = %d, want 1", len(history))
	}

	for i := range 3 {
		reset := now.Add(5 * time.Hour)
		snap := newTestCodexSnapshot(now.Add(time.Duration(i)*time.Minute), &reset)
		snap.Quotas[0].Utilization = float64(20 + i)
		if _, err := s.InsertCodexSnapshot(snap); err != nil {
			t.Fatalf("InsertCodexSnapshot series[%d]: %v", i, err)
		}
	}

	series, err := s.QueryCodexUtilizationSeries("five_hour", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("QueryCodexUtilizationSeries: %v", err)
	}
	if len(series) != 3 {
		t.Fatalf("len(series) = %d, want 3", len(series))
	}
}

func TestCodexStore_CycleOverviewAndQuotaNames(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	reset := now.Add(5 * time.Hour)
	snap := newTestCodexSnapshot(now, &reset)
	if _, err := s.InsertCodexSnapshot(snap); err != nil {
		t.Fatalf("InsertCodexSnapshot: %v", err)
	}
	if _, err := s.CreateCodexCycle("five_hour", now, &reset); err != nil {
		t.Fatalf("CreateCodexCycle: %v", err)
	}
	if err := s.UpdateCodexCycle("five_hour", 22.5, 4.0); err != nil {
		t.Fatalf("UpdateCodexCycle: %v", err)
	}

	overview, err := s.QueryCodexCycleOverview("five_hour", 50)
	if err != nil {
		t.Fatalf("QueryCodexCycleOverview: %v", err)
	}
	if len(overview) == 0 {
		t.Fatal("expected non-empty overview")
	}

	names, err := s.QueryAllCodexQuotaNames()
	if err != nil {
		t.Fatalf("QueryAllCodexQuotaNames: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("expected quota names")
	}
}

func TestCodexStore_QueryLatestCodex_ParseFailure(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	_, err = s.db.Exec(`INSERT INTO codex_snapshots (captured_at, plan_type, raw_json, quota_count) VALUES (?, ?, ?, ?)`, "invalid-ts", "pro", "{}", 0)
	if err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}

	_, err = s.QueryLatestCodex()
	if err == nil {
		t.Fatal("expected parse error")
	}
	if got, want := err.Error(), "codex snapshot captured_at"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want to contain %q", got, want)
	}
}

func TestCodexStore_QueryCodexRange_ParseFailure(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	capturedAt := now.Format(time.RFC3339Nano)
	result, err := s.db.Exec(`INSERT INTO codex_snapshots (captured_at, plan_type, raw_json, quota_count) VALUES (?, ?, ?, ?)`, capturedAt, "pro", "{}", 1)
	if err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	snapshotID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("snapshot id: %v", err)
	}
	_, err = s.db.Exec(`INSERT INTO codex_quota_values (snapshot_id, quota_name, utilization, resets_at, status) VALUES (?, ?, ?, ?, ?)`, snapshotID, "five_hour", 10.0, "invalid-reset", "healthy")
	if err != nil {
		t.Fatalf("insert quota value: %v", err)
	}

	_, err = s.QueryCodexRange(now.Add(-time.Minute), now.Add(time.Minute))
	if err == nil {
		t.Fatal("expected parse error")
	}
	if got, want := err.Error(), "codex quota resets_at"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want to contain %q", got, want)
	}
}
