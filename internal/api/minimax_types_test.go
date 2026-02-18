package api

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseMiniMaxResponse(t *testing.T) {
	raw := `{
		"base_resp": {"status_code": 0, "status_msg": ""},
		"model_remains": [
			{
				"model_name": "MiniMax-M2",
				"start_time": "2026-02-15T11:00:00Z",
				"end_time": "2026-02-15T13:00:00Z",
				"remains_time": 7200000,
				"current_interval_total_count": 200,
				"current_interval_usage_count": 42
			},
			{
				"model_name": "MiniMax-Text-01",
				"start_time": "2026-02-15T11:00:00Z",
				"end_time": "2026-02-15T13:00:00Z",
				"remains_time": 7200000,
				"current_interval_total_count": 100,
				"current_interval_usage_count": 20
			}
		]
	}`

	resp, err := ParseMiniMaxResponse([]byte(raw))
	if err != nil {
		t.Fatalf("ParseMiniMaxResponse: %v", err)
	}

	if resp.BaseResp.StatusCode != 0 {
		t.Errorf("BaseResp.StatusCode = %d, want 0", resp.BaseResp.StatusCode)
	}
	if len(resp.ModelRemains) != 2 {
		t.Fatalf("ModelRemains len = %d, want 2", len(resp.ModelRemains))
	}
	if resp.ModelRemains[0].ModelName != "MiniMax-M2" {
		t.Errorf("first model name = %q, want %q", resp.ModelRemains[0].ModelName, "MiniMax-M2")
	}
	if resp.ModelRemains[0].CurrentIntervalTotalCount != 200 {
		t.Errorf("CurrentIntervalTotalCount = %d, want 200", resp.ModelRemains[0].CurrentIntervalTotalCount)
	}
	if resp.ModelRemains[0].CurrentIntervalUsageCount != 42 {
		t.Errorf("CurrentIntervalUsageCount = %d, want 42", resp.ModelRemains[0].CurrentIntervalUsageCount)
	}
}

func TestMiniMaxActiveModelNames(t *testing.T) {
	resp := MiniMaxRemainsResponse{
		ModelRemains: []MiniMaxModelRemain{
			{ModelName: "MiniMax-M2"},
			{ModelName: "MiniMax-Text-01"},
			{ModelName: "MiniMax-M2"}, // duplicate should be removed
			{},                        // empty should be ignored
		},
	}

	names := resp.ActiveModelNames()
	expected := []string{"MiniMax-M2", "MiniMax-Text-01"}
	if len(names) != len(expected) {
		t.Fatalf("ActiveModelNames len = %d, want %d", len(names), len(expected))
	}
	for i, name := range names {
		if name != expected[i] {
			t.Errorf("ActiveModelNames[%d] = %q, want %q", i, name, expected[i])
		}
	}
}

func TestMiniMaxToSnapshot(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	resp := MiniMaxRemainsResponse{
		BaseResp: MiniMaxBaseResp{StatusCode: 0},
		ModelRemains: []MiniMaxModelRemain{
			{
				ModelName:                 "MiniMax-M2",
				StartTime:                 "2026-02-15T11:00:00Z",
				EndTime:                   "2026-02-15T13:00:00Z",
				RemainsTime:               7200000,
				CurrentIntervalTotalCount: 200,
				CurrentIntervalUsageCount: 42,
			},
			{
				ModelName:                 "MiniMax-Text-01",
				StartTime:                 "2026-02-15T11:00:00Z",
				EndTime:                   "2026-02-15T13:00:00Z",
				RemainsTime:               7200000,
				CurrentIntervalTotalCount: 100,
				CurrentIntervalUsageCount: 20,
			},
		},
	}

	snapshot := resp.ToSnapshot(now)
	if snapshot == nil {
		t.Fatal("ToSnapshot returned nil")
	}
	if snapshot.CapturedAt != now {
		t.Errorf("CapturedAt = %v, want %v", snapshot.CapturedAt, now)
	}
	if len(snapshot.Models) != 2 {
		t.Fatalf("Models len = %d, want 2", len(snapshot.Models))
	}
	if snapshot.Models[0].ModelName != "MiniMax-M2" {
		t.Errorf("Models[0].ModelName = %q, want %q", snapshot.Models[0].ModelName, "MiniMax-M2")
	}
	if snapshot.Models[0].Total != 200 {
		t.Errorf("Models[0].Total = %d, want 200", snapshot.Models[0].Total)
	}
	if snapshot.Models[0].Remain != 158 {
		t.Errorf("Models[0].Remain = %d, want 158", snapshot.Models[0].Remain)
	}
	if snapshot.Models[0].Used != 42 {
		t.Errorf("Models[0].Used = %d, want 42", snapshot.Models[0].Used)
	}
	if snapshot.Models[0].UsedPercent != 21.0 {
		t.Errorf("Models[0].UsedPercent = %.2f, want 21.00", snapshot.Models[0].UsedPercent)
	}
	if snapshot.Models[0].WindowStart == nil {
		t.Fatal("Models[0].WindowStart should not be nil")
	}
	if snapshot.Models[0].WindowEnd == nil {
		t.Fatal("Models[0].WindowEnd should not be nil")
	}
	if snapshot.Models[0].ResetAt == nil {
		t.Fatal("Models[0].ResetAt should not be nil")
	}
	if snapshot.Models[0].TimeUntilReset != 2*time.Hour {
		t.Errorf("Models[0].TimeUntilReset = %v, want %v", snapshot.Models[0].TimeUntilReset, 2*time.Hour)
	}
	if snapshot.RawJSON == "" {
		t.Error("RawJSON should not be empty")
	}
}

func TestMiniMaxDisplayName(t *testing.T) {
	if got := MiniMaxDisplayName("MiniMax-M2"); got != "MiniMax-M2" {
		t.Errorf("MiniMaxDisplayName(MiniMax-M2) = %q, want MiniMax-M2", got)
	}
	if got := MiniMaxDisplayName("unknown"); got != "unknown" {
		t.Errorf("MiniMaxDisplayName(unknown) = %q, want unknown", got)
	}
}

func TestParseMiniMaxResponse_InvalidJSON(t *testing.T) {
	_, err := ParseMiniMaxResponse([]byte(`{invalid`))
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestMiniMaxRoundTrip(t *testing.T) {
	raw := `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"MiniMax-M2","start_time":"2026-02-15T11:00:00Z","end_time":"2026-02-15T13:00:00Z","remains_time":3600000,"current_interval_total_count":100,"current_interval_usage_count":40}]}`

	resp, err := ParseMiniMaxResponse([]byte(raw))
	if err != nil {
		t.Fatalf("ParseMiniMaxResponse: %v", err)
	}

	snapshot := resp.ToSnapshot(time.Now().UTC())
	if snapshot.RawJSON == "" {
		t.Fatal("RawJSON should not be empty")
	}

	var roundTripped MiniMaxRemainsResponse
	if err := json.Unmarshal([]byte(snapshot.RawJSON), &roundTripped); err != nil {
		t.Fatalf("Failed to re-parse RawJSON: %v", err)
	}
	if len(roundTripped.ModelRemains) != 1 || roundTripped.ModelRemains[0].ModelName != "MiniMax-M2" {
		t.Errorf("round-trip model remains = %+v", roundTripped.ModelRemains)
	}
}
