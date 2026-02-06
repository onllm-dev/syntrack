package api

import (
	"testing"
	"time"
)

// realZaiAPIResponse is a sample response from Z.ai API
const realZaiAPIResponse = `{
  "code": 200,
  "msg": "Operation successful",
  "success": true,
  "data": {
    "limits": [
      {
        "type": "TIME_LIMIT",
        "unit": 5,
        "number": 1,
        "usage": 1000,
        "currentValue": 19,
        "remaining": 981,
        "percentage": 1,
        "usageDetails": [{"modelCode": "search-prime", "usage": 16}]
      },
      {
        "type": "TOKENS_LIMIT",
        "unit": 3,
        "number": 5,
        "usage": 200000000,
        "currentValue": 200112618,
        "remaining": 0,
        "percentage": 100,
        "nextResetTime": 1770398385482
      }
    ]
  }
}`

func TestZaiQuotaResponse_ParseZaiResponse_RealData(t *testing.T) {
	resp, err := ParseZaiResponse([]byte(realZaiAPIResponse))
	if err != nil {
		t.Fatalf("Failed to parse real Z.ai API response: %v", err)
	}

	// Verify we have 2 limits
	if len(resp.Limits) != 2 {
		t.Fatalf("Expected 2 limits, got %d", len(resp.Limits))
	}

	// Verify TIME_LIMIT
	timeLimit := resp.Limits[0]
	if timeLimit.Type != "TIME_LIMIT" {
		t.Errorf("First limit type = %q, want %q", timeLimit.Type, "TIME_LIMIT")
	}
	if timeLimit.Unit != 5 {
		t.Errorf("TIME_LIMIT unit = %d, want %d", timeLimit.Unit, 5)
	}
	if timeLimit.Number != 1 {
		t.Errorf("TIME_LIMIT number = %d, want %d", timeLimit.Number, 1)
	}
	if timeLimit.Usage != 1000 {
		t.Errorf("TIME_LIMIT usage = %v, want %v", timeLimit.Usage, 1000)
	}
	if timeLimit.CurrentValue != 19 {
		t.Errorf("TIME_LIMIT currentValue = %v, want %v", timeLimit.CurrentValue, 19)
	}
	if timeLimit.Remaining != 981 {
		t.Errorf("TIME_LIMIT remaining = %v, want %v", timeLimit.Remaining, 981)
	}
	if timeLimit.Percentage != 1 {
		t.Errorf("TIME_LIMIT percentage = %d, want %d", timeLimit.Percentage, 1)
	}

	// Verify TIME_LIMIT has no reset time
	if timeLimit.NextResetMs != nil {
		t.Errorf("TIME_LIMIT should not have nextResetTime")
	}

	// Verify TIME_LIMIT usage details
	if len(timeLimit.UsageDetails) != 1 {
		t.Fatalf("Expected 1 usage detail, got %d", len(timeLimit.UsageDetails))
	}
	if timeLimit.UsageDetails[0].ModelCode != "search-prime" {
		t.Errorf("ModelCode = %q, want %q", timeLimit.UsageDetails[0].ModelCode, "search-prime")
	}
	if timeLimit.UsageDetails[0].Usage != 16 {
		t.Errorf("Usage = %v, want %v", timeLimit.UsageDetails[0].Usage, 16)
	}

	// Verify TOKENS_LIMIT
	tokensLimit := resp.Limits[1]
	if tokensLimit.Type != "TOKENS_LIMIT" {
		t.Errorf("Second limit type = %q, want %q", tokensLimit.Type, "TOKENS_LIMIT")
	}
	if tokensLimit.Unit != 3 {
		t.Errorf("TOKENS_LIMIT unit = %d, want %d", tokensLimit.Unit, 3)
	}
	if tokensLimit.Number != 5 {
		t.Errorf("TOKENS_LIMIT number = %d, want %d", tokensLimit.Number, 5)
	}
	if tokensLimit.Usage != 200000000 {
		t.Errorf("TOKENS_LIMIT usage = %v, want %v", tokensLimit.Usage, 200000000)
	}
	if tokensLimit.CurrentValue != 200112618 {
		t.Errorf("TOKENS_LIMIT currentValue = %v, want %v", tokensLimit.CurrentValue, 200112618)
	}
	if tokensLimit.Remaining != 0 {
		t.Errorf("TOKENS_LIMIT remaining = %v, want %v", tokensLimit.Remaining, 0)
	}
	if tokensLimit.Percentage != 100 {
		t.Errorf("TOKENS_LIMIT percentage = %d, want %d", tokensLimit.Percentage, 100)
	}
}

func TestZaiLimit_NextResetTime_EpochMilliseconds(t *testing.T) {
	resp, err := ParseZaiResponse([]byte(realZaiAPIResponse))
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	// TIME_LIMIT should have nil reset time
	timeReset := resp.Limits[0].GetResetTime()
	if timeReset != nil {
		t.Errorf("TIME_LIMIT reset time should be nil, got %v", timeReset)
	}

	// TOKENS_LIMIT should have the epoch timestamp converted to time.Time
	tokensReset := resp.Limits[1].GetResetTime()
	if tokensReset == nil {
		t.Fatal("TOKENS_LIMIT should have a reset time")
	}

	// 1770398385482 ms = 2026-02-07 15:59:45.482 +0000 UTC
	expectedTime := time.UnixMilli(1770398385482)
	if !tokensReset.Equal(expectedTime) {
		t.Errorf("TOKENS_LIMIT reset time = %v, want %v", tokensReset, expectedTime)
	}
}

func TestZaiQuotaResponse_ToSnapshot(t *testing.T) {
	resp, err := ParseZaiResponse([]byte(realZaiAPIResponse))
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	capturedAt := time.Date(2026, 2, 6, 10, 30, 0, 0, time.UTC)
	snapshot := resp.ToSnapshot(capturedAt)

	// Verify captured time
	if !snapshot.CapturedAt.Equal(capturedAt) {
		t.Errorf("CapturedAt = %v, want %v", snapshot.CapturedAt, capturedAt)
	}

	// Verify TIME_LIMIT fields (unit * number = 5 * 1 = 5)
	if snapshot.TimeLimit != 5 {
		t.Errorf("TimeLimit = %d, want %d", snapshot.TimeLimit, 5)
	}
	if snapshot.TimeUnit != 5 {
		t.Errorf("TimeUnit = %d, want %d", snapshot.TimeUnit, 5)
	}
	if snapshot.TimeNumber != 1 {
		t.Errorf("TimeNumber = %d, want %d", snapshot.TimeNumber, 1)
	}
	if snapshot.TimeUsage != 1000 {
		t.Errorf("TimeUsage = %v, want %v", snapshot.TimeUsage, 1000)
	}
	if snapshot.TimeCurrentValue != 19 {
		t.Errorf("TimeCurrentValue = %v, want %v", snapshot.TimeCurrentValue, 19)
	}
	if snapshot.TimeRemaining != 981 {
		t.Errorf("TimeRemaining = %v, want %v", snapshot.TimeRemaining, 981)
	}
	if snapshot.TimePercentage != 1 {
		t.Errorf("TimePercentage = %d, want %d", snapshot.TimePercentage, 1)
	}

	// Verify TOKENS_LIMIT fields (unit * number = 3 * 5 = 15)
	if snapshot.TokensLimit != 15 {
		t.Errorf("TokensLimit = %d, want %d", snapshot.TokensLimit, 15)
	}
	if snapshot.TokensUnit != 3 {
		t.Errorf("TokensUnit = %d, want %d", snapshot.TokensUnit, 3)
	}
	if snapshot.TokensNumber != 5 {
		t.Errorf("TokensNumber = %d, want %d", snapshot.TokensNumber, 5)
	}
	if snapshot.TokensUsage != 200000000 {
		t.Errorf("TokensUsage = %v, want %v", snapshot.TokensUsage, 200000000)
	}
	if snapshot.TokensCurrentValue != 200112618 {
		t.Errorf("TokensCurrentValue = %v, want %v", snapshot.TokensCurrentValue, 200112618)
	}
	if snapshot.TokensRemaining != 0 {
		t.Errorf("TokensRemaining = %v, want %v", snapshot.TokensRemaining, 0)
	}
	if snapshot.TokensPercentage != 100 {
		t.Errorf("TokensPercentage = %d, want %d", snapshot.TokensPercentage, 100)
	}

	// Verify reset time
	if snapshot.TokensNextResetTime == nil {
		t.Fatal("TokensNextResetTime should not be nil")
	}
	expectedReset := time.UnixMilli(1770398385482)
	if !snapshot.TokensNextResetTime.Equal(expectedReset) {
		t.Errorf("TokensNextResetTime = %v, want %v", snapshot.TokensNextResetTime, expectedReset)
	}
}

func TestZaiQuotaResponse_FloatValues(t *testing.T) {
	jsonData := `{
		"code": 200,
		"msg": "OK",
		"success": true,
		"data": {
			"limits": [
				{
					"type": "TIME_LIMIT",
					"unit": 5,
					"number": 1,
					"usage": 1000.5,
					"currentValue": 19.7,
					"remaining": 980.3,
					"percentage": 1
				}
			]
		}
	}`

	resp, err := ParseZaiResponse([]byte(jsonData))
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	// Verify float values are preserved
	if resp.Limits[0].Usage != 1000.5 {
		t.Errorf("Usage = %v, want %v", resp.Limits[0].Usage, 1000.5)
	}
	if resp.Limits[0].CurrentValue != 19.7 {
		t.Errorf("CurrentValue = %v, want %v", resp.Limits[0].CurrentValue, 19.7)
	}
	if resp.Limits[0].Remaining != 980.3 {
		t.Errorf("Remaining = %v, want %v", resp.Limits[0].Remaining, 980.3)
	}
}

func TestZaiQuotaResponse_NoUsageDetails(t *testing.T) {
	jsonData := `{
		"code": 200,
		"msg": "OK",
		"success": true,
		"data": {
			"limits": [
				{
					"type": "TOKENS_LIMIT",
					"unit": 3,
					"number": 5,
					"usage": 100,
					"currentValue": 100,
					"remaining": 0,
					"percentage": 100,
					"nextResetTime": 1770398385482
				}
			]
		}
	}`

	resp, err := ParseZaiResponse([]byte(jsonData))
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	// Should have empty usage details
	if len(resp.Limits[0].UsageDetails) != 0 {
		t.Errorf("Expected 0 usage details, got %d", len(resp.Limits[0].UsageDetails))
	}
}

func TestZaiQuotaResponse_UnknownFields_Ignored(t *testing.T) {
	jsonData := `{
		"code": 200,
		"msg": "OK",
		"success": true,
		"data": {
			"limits": [
				{
					"type": "TIME_LIMIT",
					"unit": 5,
					"number": 1,
					"usage": 1000,
					"currentValue": 19,
					"remaining": 981,
					"percentage": 1,
					"unknownField": "ignored"
				}
			],
			"extraField": "also ignored"
		},
		"extraTopLevel": "ignored too"
	}`

	resp, err := ParseZaiResponse([]byte(jsonData))
	if err != nil {
		t.Fatalf("Failed to parse with unknown fields: %v", err)
	}

	// Should still parse known fields correctly
	if resp.Limits[0].Type != "TIME_LIMIT" {
		t.Errorf("Type = %q, want %q", resp.Limits[0].Type, "TIME_LIMIT")
	}
	if resp.Limits[0].Usage != 1000 {
		t.Errorf("Usage = %v, want %v", resp.Limits[0].Usage, 1000)
	}
}
