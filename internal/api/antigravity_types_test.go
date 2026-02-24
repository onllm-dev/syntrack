package api

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAntigravityDisplayName(t *testing.T) {
	tests := []struct {
		modelID  string
		expected string
	}{
		{"claude-4-5-sonnet", "Claude 4.5 Sonnet"},
		// Note: Thinking suffix is intentionally removed - it's redundant for Claude models
		{"claude-4-5-sonnet-thinking", "Claude 4.5 Sonnet"},
		{"gemini-3-pro", "Gemini 3 Pro"},
		{"unknown-model", "unknown-model"},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			got := AntigravityDisplayName(tt.modelID)
			if got != tt.expected {
				t.Errorf("AntigravityDisplayName(%q) = %q, want %q", tt.modelID, got, tt.expected)
			}
		})
	}
}

func TestAntigravityUserStatusResponse_ActiveModelIDs(t *testing.T) {
	resp := AntigravityUserStatusResponse{
		UserStatus: &AntigravityUserStatus{
			CascadeModelConfigData: &AntigravityCascadeModelConfigData{
				ClientModelConfigs: []AntigravityClientModelConfig{
					{
						Label:        "Claude Sonnet",
						ModelOrAlias: &AntigravityModelOrAlias{Model: "claude-4-5-sonnet"},
						QuotaInfo:    &AntigravityQuotaInfo{RemainingFraction: 0.8},
					},
					{
						Label:        "No Quota",
						ModelOrAlias: &AntigravityModelOrAlias{Model: "no-quota-model"},
						// No QuotaInfo - should be skipped
					},
					{
						Label:        "Gemini Pro",
						ModelOrAlias: &AntigravityModelOrAlias{Model: "gemini-3-pro"},
						QuotaInfo:    &AntigravityQuotaInfo{RemainingFraction: 0.5},
					},
				},
			},
		},
	}

	ids := resp.ActiveModelIDs()
	if len(ids) != 2 {
		t.Errorf("expected 2 active models, got %d", len(ids))
	}

	// Should be sorted
	if ids[0] != "claude-4-5-sonnet" || ids[1] != "gemini-3-pro" {
		t.Errorf("unexpected model IDs: %v", ids)
	}
}

func TestAntigravityUserStatusResponse_ActiveModelIDs_NilUserStatus(t *testing.T) {
	resp := AntigravityUserStatusResponse{}
	ids := resp.ActiveModelIDs()
	if ids != nil {
		t.Errorf("expected nil, got %v", ids)
	}
}

func TestAntigravityUserStatusResponse_ToSnapshot(t *testing.T) {
	now := time.Now().UTC()
	resetTime := now.Add(24 * time.Hour)

	resp := AntigravityUserStatusResponse{
		UserStatus: &AntigravityUserStatus{
			Email: "test@example.com",
			PlanStatus: &AntigravityPlanStatus{
				AvailablePromptCredits: 500,
				PlanInfo: &AntigravityPlanInfo{
					PlanName:            "Pro",
					MonthlyPromptCredits: 1000,
				},
			},
			CascadeModelConfigData: &AntigravityCascadeModelConfigData{
				ClientModelConfigs: []AntigravityClientModelConfig{
					{
						Label:        "Claude Sonnet",
						ModelOrAlias: &AntigravityModelOrAlias{Model: "claude-4-5-sonnet"},
						QuotaInfo: &AntigravityQuotaInfo{
							RemainingFraction: 0.75,
							ResetTime:         resetTime.Format(time.RFC3339),
						},
					},
				},
			},
		},
	}

	snapshot := resp.ToSnapshot(now)

	if snapshot.CapturedAt != now {
		t.Errorf("CapturedAt = %v, want %v", snapshot.CapturedAt, now)
	}
	if snapshot.Email != "test@example.com" {
		t.Errorf("Email = %q, want %q", snapshot.Email, "test@example.com")
	}
	if snapshot.PlanName != "Pro" {
		t.Errorf("PlanName = %q, want %q", snapshot.PlanName, "Pro")
	}
	if snapshot.PromptCredits != 500 {
		t.Errorf("PromptCredits = %f, want 500", snapshot.PromptCredits)
	}
	if snapshot.MonthlyCredits != 1000 {
		t.Errorf("MonthlyCredits = %d, want 1000", snapshot.MonthlyCredits)
	}
	if len(snapshot.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(snapshot.Models))
	}

	model := snapshot.Models[0]
	if model.ModelID != "claude-4-5-sonnet" {
		t.Errorf("ModelID = %q, want %q", model.ModelID, "claude-4-5-sonnet")
	}
	if model.RemainingFraction != 0.75 {
		t.Errorf("RemainingFraction = %f, want 0.75", model.RemainingFraction)
	}
	if model.RemainingPercent != 75 {
		t.Errorf("RemainingPercent = %f, want 75", model.RemainingPercent)
	}
	if model.IsExhausted {
		t.Error("IsExhausted = true, want false")
	}
	if model.ResetTime == nil {
		t.Error("ResetTime is nil")
	}
}

func TestParseAntigravityResponse(t *testing.T) {
	jsonData := `{
		"userStatus": {
			"email": "user@example.com",
			"planStatus": {
				"availablePromptCredits": 250,
				"planInfo": {
					"planName": "Free",
					"monthlyPromptCredits": 500
				}
			},
			"cascadeModelConfigData": {
				"clientModelConfigs": [
					{
						"label": "Test Model",
						"modelOrAlias": {"model": "test-model"},
						"quotaInfo": {
							"remainingFraction": 0.5,
							"resetTime": "2026-02-24T12:00:00Z"
						}
					}
				]
			}
		}
	}`

	resp, err := ParseAntigravityResponse([]byte(jsonData))
	if err != nil {
		t.Fatalf("ParseAntigravityResponse failed: %v", err)
	}

	if resp.UserStatus == nil {
		t.Fatal("UserStatus is nil")
	}
	if resp.UserStatus.Email != "user@example.com" {
		t.Errorf("Email = %q, want %q", resp.UserStatus.Email, "user@example.com")
	}
	if resp.UserStatus.PlanStatus.AvailablePromptCredits != 250 {
		t.Errorf("AvailablePromptCredits = %f, want 250", resp.UserStatus.PlanStatus.AvailablePromptCredits)
	}
}

func TestAntigravitySnapshot_RawJSON(t *testing.T) {
	resp := AntigravityUserStatusResponse{
		UserStatus: &AntigravityUserStatus{
			Email: "test@example.com",
		},
	}

	snapshot := resp.ToSnapshot(time.Now())

	// Raw JSON should be valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(snapshot.RawJSON), &parsed); err != nil {
		t.Errorf("RawJSON is not valid JSON: %v", err)
	}
}

func TestAntigravityModelQuota_IsExhausted(t *testing.T) {
	resp := AntigravityUserStatusResponse{
		UserStatus: &AntigravityUserStatus{
			CascadeModelConfigData: &AntigravityCascadeModelConfigData{
				ClientModelConfigs: []AntigravityClientModelConfig{
					{
						Label:        "Exhausted Model",
						ModelOrAlias: &AntigravityModelOrAlias{Model: "exhausted"},
						QuotaInfo:    &AntigravityQuotaInfo{RemainingFraction: 0},
					},
					{
						Label:        "Active Model",
						ModelOrAlias: &AntigravityModelOrAlias{Model: "active"},
						QuotaInfo:    &AntigravityQuotaInfo{RemainingFraction: 0.5},
					},
				},
			},
		},
	}

	snapshot := resp.ToSnapshot(time.Now())

	if len(snapshot.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(snapshot.Models))
	}

	for _, m := range snapshot.Models {
		if m.ModelID == "exhausted" && !m.IsExhausted {
			t.Error("exhausted model should have IsExhausted=true")
		}
		if m.ModelID == "active" && m.IsExhausted {
			t.Error("active model should have IsExhausted=false")
		}
	}
}

func TestCleanAntigravityLabel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Claude Sonnet 4.6 (Thinking)", "Claude Sonnet 4.6"},
		{"Claude Opus 4.6 (Thinking)", "Claude Opus 4.6"},
		{"Claude Sonnet 4.5(Thinking)", "Claude Sonnet 4.5"},
		{"Gemini 3.1 Pro (High)", "Gemini 3.1 Pro (High)"}, // Don't remove (High)
		{"GPT-OSS 120B (Medium)", "GPT-OSS 120B (Medium)"}, // Don't remove (Medium)
		{"Plain Model", "Plain Model"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := CleanAntigravityLabel(tt.input)
			if got != tt.expected {
				t.Errorf("CleanAntigravityLabel(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGroupModelsByQuota_SharedPool(t *testing.T) {
	// Models with same remainingFraction and resetTime should be grouped together
	resetTime := time.Now().Add(4 * time.Hour)
	models := []AntigravityModelQuota{
		{
			ModelID:           "model-a",
			Label:             "Gemini 3.1 Pro (High)",
			RemainingFraction: 0.6,
			RemainingPercent:  60,
			ResetTime:         &resetTime,
		},
		{
			ModelID:           "model-b",
			Label:             "Gemini 3.1 Pro (Low)",
			RemainingFraction: 0.6,
			RemainingPercent:  60,
			ResetTime:         &resetTime,
		},
		{
			ModelID:           "model-c",
			Label:             "Claude Sonnet 4.6",
			RemainingFraction: 1.0,
			RemainingPercent:  100,
			ResetTime:         &resetTime,
		},
	}

	pools := GroupModelsByQuota(models)

	if len(pools) != 2 {
		t.Errorf("expected 2 pools, got %d", len(pools))
	}

	// Find the Gemini pool
	var geminiPool *AntigravityQuotaPool
	for i := range pools {
		if len(pools[i].Models) == 2 {
			geminiPool = &pools[i]
			break
		}
	}

	if geminiPool == nil {
		t.Fatal("expected a pool with 2 models (Gemini)")
	}

	// The pool name should combine the models
	if geminiPool.RemainingPercent != 60 {
		t.Errorf("expected pool RemainingPercent = 60, got %f", geminiPool.RemainingPercent)
	}
}

func TestGroupModelsByQuota_Empty(t *testing.T) {
	pools := GroupModelsByQuota(nil)
	if pools != nil {
		t.Errorf("expected nil for empty input, got %v", pools)
	}
}

func TestExtractModelBase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Gemini 3.1 Pro (High)", "Gemini 3.1 Pro"},
		{"Gemini 3.1 Pro (Low)", "Gemini 3.1 Pro"},
		{"Claude Sonnet 4.6", "Claude Sonnet"},
		{"Claude Opus 4.5", "Claude Opus"},
		{"GPT-OSS 120B (Medium)", "GPT-OSS 120B"},
		{"Plain Model", "Plain Model"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractModelBase(tt.input)
			if got != tt.expected {
				t.Errorf("extractModelBase(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestAntigravityQuotaGroupForModel(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		label   string
		want    string
	}{
		{name: "Claude model", modelID: "claude-4-5-sonnet", want: AntigravityQuotaGroupClaudeGPT},
		{name: "GPT model", modelID: "gpt-5", want: AntigravityQuotaGroupClaudeGPT},
		{name: "Gemini Pro model", modelID: "gemini-3-pro", want: AntigravityQuotaGroupGeminiPro},
		{name: "Gemini Flash model", modelID: "gemini-3-flash", want: AntigravityQuotaGroupGeminiFlash},
		{name: "Label fallback Gemini Flash", modelID: "unknown", label: "Gemini Flash Lite", want: AntigravityQuotaGroupGeminiFlash},
		{name: "Unknown defaults to Claude+GPT", modelID: "other", label: "Other", want: AntigravityQuotaGroupClaudeGPT},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AntigravityQuotaGroupForModel(tt.modelID, tt.label)
			if got != tt.want {
				t.Fatalf("AntigravityQuotaGroupForModel(%q, %q) = %q, want %q", tt.modelID, tt.label, got, tt.want)
			}
		})
	}
}

func TestGroupAntigravityModelsByLogicalQuota(t *testing.T) {
	now := time.Now().UTC()
	soon := now.Add(2 * time.Hour)
	later := now.Add(6 * time.Hour)

	models := []AntigravityModelQuota{
		{
			ModelID:           "claude-4-5-sonnet",
			Label:             "Claude Sonnet 4.5",
			RemainingFraction: 0.70,
			IsExhausted:       false,
			ResetTime:         &later,
		},
		{
			ModelID:           "gpt-5",
			Label:             "GPT 5",
			RemainingFraction: 0.50,
			IsExhausted:       false,
			ResetTime:         &soon,
		},
		{
			ModelID:           "gemini-3-pro",
			Label:             "Gemini 3 Pro",
			RemainingFraction: 0.60,
			IsExhausted:       false,
			ResetTime:         &later,
		},
		{
			ModelID:           "gemini-3-flash",
			Label:             "Gemini 3 Flash",
			RemainingFraction: 0.20,
			IsExhausted:       false,
			ResetTime:         &soon,
		},
	}

	groups := GroupAntigravityModelsByLogicalQuota(models)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}

	if groups[0].GroupKey != AntigravityQuotaGroupClaudeGPT {
		t.Fatalf("expected first group %q, got %q", AntigravityQuotaGroupClaudeGPT, groups[0].GroupKey)
	}
	if groups[1].GroupKey != AntigravityQuotaGroupGeminiPro {
		t.Fatalf("expected second group %q, got %q", AntigravityQuotaGroupGeminiPro, groups[1].GroupKey)
	}
	if groups[2].GroupKey != AntigravityQuotaGroupGeminiFlash {
		t.Fatalf("expected third group %q, got %q", AntigravityQuotaGroupGeminiFlash, groups[2].GroupKey)
	}

	claudeGPT := groups[0]
	if claudeGPT.DisplayName != "Claude + GPT Quota" {
		t.Fatalf("expected Claude+GPT display name, got %q", claudeGPT.DisplayName)
	}
	if claudeGPT.RemainingFraction < 0.599 || claudeGPT.RemainingFraction > 0.601 {
		t.Fatalf("expected averaged remaining fraction ~0.60, got %.4f", claudeGPT.RemainingFraction)
	}
	if claudeGPT.UsagePercent < 39.9 || claudeGPT.UsagePercent > 40.1 {
		t.Fatalf("expected usage percent ~40, got %.4f", claudeGPT.UsagePercent)
	}
	if claudeGPT.ResetTime == nil || !claudeGPT.ResetTime.Equal(soon) {
		t.Fatalf("expected earliest reset (%v), got %v", soon, claudeGPT.ResetTime)
	}
	if claudeGPT.Color != "#D97757" {
		t.Fatalf("expected Claude+GPT color #D97757, got %q", claudeGPT.Color)
	}

	geminiFlash := groups[2]
	if geminiFlash.RemainingFraction < 0.199 || geminiFlash.RemainingFraction > 0.201 {
		t.Fatalf("expected Gemini Flash remaining fraction ~0.20, got %.4f", geminiFlash.RemainingFraction)
	}
	if geminiFlash.UsagePercent < 79.9 || geminiFlash.UsagePercent > 80.1 {
		t.Fatalf("expected Gemini Flash usage percent ~80, got %.4f", geminiFlash.UsagePercent)
	}
}

func TestGroupAntigravityModelsByLogicalQuota_EmptyStillReturnsFixedGroups(t *testing.T) {
	groups := GroupAntigravityModelsByLogicalQuota(nil)
	if len(groups) != 3 {
		t.Fatalf("expected 3 fixed groups, got %d", len(groups))
	}
	for _, g := range groups {
		if g.RemainingFraction != 1.0 {
			t.Fatalf("expected default remaining fraction 1.0 for %s, got %.4f", g.GroupKey, g.RemainingFraction)
		}
		if g.UsagePercent != 0 {
			t.Fatalf("expected default usage 0 for %s, got %.4f", g.GroupKey, g.UsagePercent)
		}
	}
}
