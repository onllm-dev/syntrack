package store

import (
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
)

func TestQueryAntigravityModelIDsForGroup(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	snapshot := &api.AntigravitySnapshot{
		CapturedAt: now,
		Models: []api.AntigravityModelQuota{
			{ModelID: "claude-4-5-sonnet", Label: "Claude 4.5 Sonnet", RemainingFraction: 0.75, RemainingPercent: 75},
			{ModelID: "gpt-4o", Label: "GPT 4o", RemainingFraction: 0.72, RemainingPercent: 72},
			{ModelID: "gemini-3-pro", Label: "Gemini 3 Pro", RemainingFraction: 0.60, RemainingPercent: 60},
			{ModelID: "gemini-3-flash", Label: "Gemini 3 Flash", RemainingFraction: 0.50, RemainingPercent: 50},
		},
	}
	if _, err := s.InsertAntigravitySnapshot(snapshot); err != nil {
		t.Fatalf("failed to insert snapshot: %v", err)
	}

	claudeGPT, err := s.QueryAntigravityModelIDsForGroup(api.AntigravityQuotaGroupClaudeGPT)
	if err != nil {
		t.Fatalf("failed querying claude+gpt IDs: %v", err)
	}
	if len(claudeGPT) != 2 {
		t.Fatalf("expected 2 claude+gpt IDs, got %d (%v)", len(claudeGPT), claudeGPT)
	}

	geminiPro, err := s.QueryAntigravityModelIDsForGroup(api.AntigravityQuotaGroupGeminiPro)
	if err != nil {
		t.Fatalf("failed querying gemini pro IDs: %v", err)
	}
	if len(geminiPro) != 1 || geminiPro[0] != "gemini-3-pro" {
		t.Fatalf("expected [gemini-3-pro], got %v", geminiPro)
	}

	geminiFlash, err := s.QueryAntigravityModelIDsForGroup(api.AntigravityQuotaGroupGeminiFlash)
	if err != nil {
		t.Fatalf("failed querying gemini flash IDs: %v", err)
	}
	if len(geminiFlash) != 1 || geminiFlash[0] != "gemini-3-flash" {
		t.Fatalf("expected [gemini-3-flash], got %v", geminiFlash)
	}
}

func TestQueryAntigravityCycleOverview_GroupedCrossQuotas(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(2 * time.Hour)

	startSnap := &api.AntigravitySnapshot{
		CapturedAt: now,
		Models: []api.AntigravityModelQuota{
			{ModelID: "claude-4-5-sonnet", Label: "Claude 4.5 Sonnet", RemainingFraction: 0.80, RemainingPercent: 80, ResetTime: &reset},
			{ModelID: "gemini-3-pro", Label: "Gemini 3 Pro", RemainingFraction: 0.90, RemainingPercent: 90, ResetTime: &reset},
			{ModelID: "gemini-3-flash", Label: "Gemini 3 Flash", RemainingFraction: 0.95, RemainingPercent: 95, ResetTime: &reset},
		},
	}
	if _, err := s.InsertAntigravitySnapshot(startSnap); err != nil {
		t.Fatalf("failed to insert start snapshot: %v", err)
	}

	if _, err := s.CreateAntigravityCycle("claude-4-5-sonnet", now, &reset); err != nil {
		t.Fatalf("failed to create cycle: %v", err)
	}
	if err := s.UpdateAntigravityCycle("claude-4-5-sonnet", 0.50, 0.30); err != nil {
		t.Fatalf("failed to update cycle: %v", err)
	}
	if err := s.CloseAntigravityCycle("claude-4-5-sonnet", now.Add(30*time.Minute), 0.50, 0.30); err != nil {
		t.Fatalf("failed to close cycle: %v", err)
	}

	endSnap := &api.AntigravitySnapshot{
		CapturedAt: now.Add(30 * time.Minute),
		Models: []api.AntigravityModelQuota{
			{ModelID: "claude-4-5-sonnet", Label: "Claude 4.5 Sonnet", RemainingFraction: 0.60, RemainingPercent: 60, ResetTime: &reset},
			{ModelID: "gemini-3-pro", Label: "Gemini 3 Pro", RemainingFraction: 0.85, RemainingPercent: 85, ResetTime: &reset},
			{ModelID: "gemini-3-flash", Label: "Gemini 3 Flash", RemainingFraction: 0.90, RemainingPercent: 90, ResetTime: &reset},
		},
	}
	if _, err := s.InsertAntigravitySnapshot(endSnap); err != nil {
		t.Fatalf("failed to insert end snapshot: %v", err)
	}

	rows, err := s.QueryAntigravityCycleOverview(api.AntigravityQuotaGroupClaudeGPT, 10)
	if err != nil {
		t.Fatalf("failed querying cycle overview: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 cycle overview row, got %d", len(rows))
	}

	row := rows[0]
	if row.QuotaType != api.AntigravityQuotaGroupClaudeGPT {
		t.Fatalf("expected quota type %s, got %s", api.AntigravityQuotaGroupClaudeGPT, row.QuotaType)
	}
	if row.CycleEnd == nil {
		t.Fatal("expected cycle end to be set")
	}

	if len(row.CrossQuotas) != len(api.AntigravityQuotaGroupOrder()) {
		t.Fatalf("expected %d cross quotas, got %d", len(api.AntigravityQuotaGroupOrder()), len(row.CrossQuotas))
	}

	crossByName := make(map[string]CrossQuotaEntry, len(row.CrossQuotas))
	for _, cq := range row.CrossQuotas {
		crossByName[cq.Name] = cq
	}

	claudeGroup, ok := crossByName[api.AntigravityQuotaGroupClaudeGPT]
	if !ok {
		t.Fatalf("missing cross quota for %s", api.AntigravityQuotaGroupClaudeGPT)
	}
	if claudeGroup.Percent < 39.9 || claudeGroup.Percent > 40.1 {
		t.Fatalf("expected claude group usage ~40%%, got %.2f", claudeGroup.Percent)
	}
	if claudeGroup.StartPercent < 19.9 || claudeGroup.StartPercent > 20.1 {
		t.Fatalf("expected claude group start ~20%%, got %.2f", claudeGroup.StartPercent)
	}
	if claudeGroup.Delta < 19.9 || claudeGroup.Delta > 20.1 {
		t.Fatalf("expected claude group delta ~20%%, got %.2f", claudeGroup.Delta)
	}

	geminiProGroup, ok := crossByName[api.AntigravityQuotaGroupGeminiPro]
	if !ok {
		t.Fatalf("missing cross quota for %s", api.AntigravityQuotaGroupGeminiPro)
	}
	if geminiProGroup.Percent < 14.9 || geminiProGroup.Percent > 15.1 {
		t.Fatalf("expected gemini pro usage ~15%%, got %.2f", geminiProGroup.Percent)
	}
	if geminiProGroup.Delta < 4.9 || geminiProGroup.Delta > 5.1 {
		t.Fatalf("expected gemini pro delta ~5%%, got %.2f", geminiProGroup.Delta)
	}

	geminiFlashGroup, ok := crossByName[api.AntigravityQuotaGroupGeminiFlash]
	if !ok {
		t.Fatalf("missing cross quota for %s", api.AntigravityQuotaGroupGeminiFlash)
	}
	if geminiFlashGroup.Percent < 9.9 || geminiFlashGroup.Percent > 10.1 {
		t.Fatalf("expected gemini flash usage ~10%%, got %.2f", geminiFlashGroup.Percent)
	}
	if geminiFlashGroup.Delta < 4.9 || geminiFlashGroup.Delta > 5.1 {
		t.Fatalf("expected gemini flash delta ~5%%, got %.2f", geminiFlashGroup.Delta)
	}
}

func TestQueryAntigravityCycleOverview_RejectsInvalidGroup(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	if _, err := s.QueryAntigravityCycleOverview("invalid-group", 10); err == nil {
		t.Fatal("expected error for invalid antigravity group")
	}
}

func TestQueryAntigravityCycleOverview_CollapsesMultipleActiveCyclesToSingleRow(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(6 * time.Hour)

	snap := &api.AntigravitySnapshot{
		CapturedAt: now,
		Models: []api.AntigravityModelQuota{
			{ModelID: "claude-4-5-sonnet", Label: "Claude 4.5 Sonnet", RemainingFraction: 0.70, RemainingPercent: 70, ResetTime: &reset},
			{ModelID: "gpt-4o", Label: "GPT 4o", RemainingFraction: 0.60, RemainingPercent: 60, ResetTime: &reset},
		},
	}
	if _, err := s.InsertAntigravitySnapshot(snap); err != nil {
		t.Fatalf("failed to insert snapshot: %v", err)
	}

	if _, err := s.CreateAntigravityCycle("claude-4-5-sonnet", now.Add(-2*time.Hour), &reset); err != nil {
		t.Fatalf("failed to create claude cycle: %v", err)
	}
	if err := s.UpdateAntigravityCycle("claude-4-5-sonnet", 0.40, 0.15); err != nil {
		t.Fatalf("failed to update claude cycle: %v", err)
	}

	if _, err := s.CreateAntigravityCycle("gpt-4o", now.Add(-90*time.Minute), &reset); err != nil {
		t.Fatalf("failed to create gpt cycle: %v", err)
	}
	if err := s.UpdateAntigravityCycle("gpt-4o", 0.55, 0.20); err != nil {
		t.Fatalf("failed to update gpt cycle: %v", err)
	}

	rows, err := s.QueryAntigravityCycleOverview(api.AntigravityQuotaGroupClaudeGPT, 20)
	if err != nil {
		t.Fatalf("failed querying cycle overview: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 grouped active row, got %d", len(rows))
	}

	row := rows[0]
	if row.CycleEnd != nil {
		t.Fatal("expected grouped active row to have nil cycle end")
	}
	if row.CycleID >= 0 {
		t.Fatalf("expected grouped active cycle ID to be negative sentinel, got %d", row.CycleID)
	}
	if row.TotalDelta <= 0 {
		t.Fatalf("expected grouped active row total delta > 0, got %.2f", row.TotalDelta)
	}
}

func TestCreateAntigravityCycle_RejectsSecondActiveCycleForSameModel(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC().Truncate(time.Second)
	if _, err := s.CreateAntigravityCycle("claude-4-5-sonnet", now, nil); err != nil {
		t.Fatalf("failed to create initial active cycle: %v", err)
	}
	if _, err := s.CreateAntigravityCycle("claude-4-5-sonnet", now.Add(time.Minute), nil); err == nil {
		t.Fatal("expected unique active cycle constraint error for duplicate active cycle")
	}
}
