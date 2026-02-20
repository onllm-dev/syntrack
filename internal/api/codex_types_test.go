package api

import (
	"testing"
	"time"
)

func TestParseCodexUsageResponse_ToSnapshot_PrimaryAndSecondary(t *testing.T) {
	payload := []byte(`{
	  "plan_type": "pro",
	  "rate_limit": {
	    "primary_window": {"used_percent": 22.5, "reset_at": 1766000000, "limit_window_seconds": 18000},
	    "secondary_window": {"used_percent": 41.0, "reset_at": 1766400000, "limit_window_seconds": 604800}
	  },
	  "credits": {"balance": 123.4}
	}`)

	resp, err := ParseCodexUsageResponse(payload)
	if err != nil {
		t.Fatalf("ParseCodexUsageResponse: %v", err)
	}

	snap := resp.ToSnapshot(time.Unix(1765900000, 0).UTC())
	if len(snap.Quotas) != 2 {
		t.Fatalf("quota len = %d, want 2", len(snap.Quotas))
	}

	if snap.Quotas[0].Name != "five_hour" {
		t.Fatalf("first quota name = %q, want five_hour", snap.Quotas[0].Name)
	}
	if snap.Quotas[1].Name != "seven_day" {
		t.Fatalf("second quota name = %q, want seven_day", snap.Quotas[1].Name)
	}
	if snap.Quotas[0].Utilization != 22.5 {
		t.Fatalf("five_hour utilization = %f, want 22.5", snap.Quotas[0].Utilization)
	}
	if snap.Quotas[0].Status != "healthy" {
		t.Fatalf("five_hour status = %q, want healthy", snap.Quotas[0].Status)
	}
	if snap.Quotas[1].Status != "healthy" {
		t.Fatalf("seven_day status = %q, want healthy", snap.Quotas[1].Status)
	}
	if snap.CreditsBalance == nil || *snap.CreditsBalance != 123.4 {
		t.Fatalf("credits balance missing or incorrect")
	}
	if snap.PlanType != "pro" {
		t.Fatalf("plan type = %q, want pro", snap.PlanType)
	}
	if snap.RawJSON == "" {
		t.Fatal("RawJSON should not be empty")
	}
}

func TestCodexStatusFromUtilization(t *testing.T) {
	tests := []struct {
		name        string
		utilization float64
		want        string
	}{
		{name: "healthy", utilization: 10, want: "healthy"},
		{name: "warning", utilization: 60, want: "warning"},
		{name: "danger", utilization: 85, want: "danger"},
		{name: "critical", utilization: 99, want: "critical"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexStatusFromUtilization(tt.utilization); got != tt.want {
				t.Fatalf("codexStatusFromUtilization(%v) = %q, want %q", tt.utilization, got, tt.want)
			}
		})
	}
}

func TestParseCodexUsageResponse_CreditsBalanceString(t *testing.T) {
	payload := []byte(`{
	  "plan_type": "pro",
	  "rate_limit": {
	    "primary_window": {"used_percent": 22.5, "reset_at": 1766000000, "limit_window_seconds": 18000}
	  },
	  "credits": {"balance": "123.4"}
	}`)

	resp, err := ParseCodexUsageResponse(payload)
	if err != nil {
		t.Fatalf("ParseCodexUsageResponse: %v", err)
	}

	snap := resp.ToSnapshot(time.Unix(1765900000, 0).UTC())
	if snap.CreditsBalance == nil || *snap.CreditsBalance != 123.4 {
		t.Fatalf("credits balance missing or incorrect, got %#v", snap.CreditsBalance)
	}
}

func TestCodexDisplayName(t *testing.T) {
	if got := CodexDisplayName("five_hour"); got != "5-Hour Limit" {
		t.Fatalf("CodexDisplayName(five_hour) = %q", got)
	}
	if got := CodexDisplayName("seven_day"); got != "Weekly All-Model" {
		t.Fatalf("CodexDisplayName(seven_day) = %q", got)
	}
	if got := CodexDisplayName("unknown"); got != "unknown" {
		t.Fatalf("CodexDisplayName(unknown) = %q", got)
	}
}
