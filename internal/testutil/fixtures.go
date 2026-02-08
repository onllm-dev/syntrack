// Package testutil provides shared test infrastructure for onWatch.
package testutil

import (
	"encoding/json"
	"fmt"
	"time"
)

// --- Synthetic API Fixtures ---

// SyntheticResponseJSON returns a valid Synthetic /v2/quotas JSON response.
func SyntheticResponseJSON(subRequests, searchRequests, toolRequests float64, renewsAt time.Time) string {
	return fmt.Sprintf(`{
		"subscription": {"limit": 1350, "requests": %f, "renewsAt": "%s"},
		"search": {"hourly": {"limit": 250, "requests": %f, "renewsAt": "%s"}},
		"toolCallDiscounts": {"limit": 16200, "requests": %f, "renewsAt": "%s"}
	}`, subRequests, renewsAt.Format(time.RFC3339), searchRequests, renewsAt.Format(time.RFC3339), toolRequests, renewsAt.Format(time.RFC3339))
}

// DefaultSyntheticResponse returns a typical Synthetic response with moderate usage.
func DefaultSyntheticResponse() string {
	renewsAt := time.Now().UTC().Add(4 * time.Hour)
	return SyntheticResponseJSON(154.3, 10, 7635, renewsAt)
}

// SyntheticResponseSequence returns n Synthetic responses with incrementing usage.
func SyntheticResponseSequence(n int) []string {
	renewsAt := time.Now().UTC().Add(4 * time.Hour)
	responses := make([]string, n)
	for i := range n {
		sub := 100.0 + float64(i)*10
		search := float64(i) * 5
		tool := 5000.0 + float64(i)*100
		responses[i] = SyntheticResponseJSON(sub, search, tool, renewsAt)
	}
	return responses
}

// SyntheticResponseWithReset returns two responses where the second has a different renewsAt,
// simulating a quota reset.
func SyntheticResponseWithReset() (before, after string) {
	now := time.Now().UTC()
	beforeRenew := now.Add(1 * time.Hour)
	afterRenew := now.Add(25 * time.Hour)

	before = SyntheticResponseJSON(500, 100, 10000, beforeRenew)
	after = SyntheticResponseJSON(5, 0, 50, afterRenew)
	return before, after
}

// --- Z.ai API Fixtures ---

// ZaiResponseJSON returns a valid Z.ai quota response JSON.
func ZaiResponseJSON(tokensUsage, tokensCurrentValue float64, tokensResetMs *int64, timeUsage, timeCurrentValue float64) string {
	limits := []map[string]interface{}{
		{
			"type":         "TIME_LIMIT",
			"unit":         1,
			"number":       1000,
			"usage":        timeUsage,
			"currentValue": timeCurrentValue,
			"remaining":    timeUsage - timeCurrentValue,
			"percentage":   int(timeCurrentValue / timeUsage * 100),
		},
		{
			"type":         "TOKENS_LIMIT",
			"unit":         1,
			"number":       200000000,
			"usage":        tokensUsage,
			"currentValue": tokensCurrentValue,
			"remaining":    tokensUsage - tokensCurrentValue,
			"percentage":   int(tokensCurrentValue / tokensUsage * 100),
		},
	}

	if tokensResetMs != nil {
		limits[1]["nextResetTime"] = *tokensResetMs
	}

	data, _ := json.Marshal(map[string]interface{}{
		"code":    200,
		"msg":     "success",
		"success": true,
		"data": map[string]interface{}{
			"limits": limits,
		},
	})
	return string(data)
}

// DefaultZaiResponse returns a typical Z.ai response with moderate usage.
func DefaultZaiResponse() string {
	resetMs := time.Now().UTC().Add(7 * 24 * time.Hour).UnixMilli()
	return ZaiResponseJSON(200000000, 50000000, &resetMs, 1000, 19)
}

// ZaiResponseSequence returns n Z.ai responses with incrementing token usage.
func ZaiResponseSequence(n int) []string {
	resetMs := time.Now().UTC().Add(7 * 24 * time.Hour).UnixMilli()
	responses := make([]string, n)
	for i := range n {
		tokensCV := 10000000.0 + float64(i)*5000000
		timeCV := 10.0 + float64(i)*3
		responses[i] = ZaiResponseJSON(200000000, tokensCV, &resetMs, 1000, timeCV)
	}
	return responses
}

// ZaiResponseWithReset returns two responses where the second has a different nextResetTime,
// simulating a quota reset.
func ZaiResponseWithReset() (before, after string) {
	resetBefore := time.Now().UTC().Add(1 * time.Hour).UnixMilli()
	resetAfter := time.Now().UTC().Add(8 * 24 * time.Hour).UnixMilli()

	before = ZaiResponseJSON(200000000, 190000000, &resetBefore, 1000, 900)
	after = ZaiResponseJSON(200000000, 1000000, &resetAfter, 1000, 5)
	return before, after
}

// ZaiAuthErrorResponse returns a Z.ai 401-in-body error response (HTTP 200, code 401).
func ZaiAuthErrorResponse() string {
	data, _ := json.Marshal(map[string]interface{}{
		"code":    401,
		"msg":     "token expired or incorrect",
		"success": false,
		"data":    nil,
	})
	return string(data)
}

// --- Anthropic API Fixtures ---

// AnthropicResponseJSON returns a valid Anthropic /api/oauth/usage JSON response.
func AnthropicResponseJSON(fiveHour, sevenDay, sevenDaySonnet float64, fiveHourReset, sevenDayReset time.Time) string {
	boolTrue := true
	boolFalse := false
	resp := map[string]*struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    *string  `json:"resets_at"`
		IsEnabled   *bool    `json:"is_enabled"`
	}{
		"five_hour": {
			Utilization: &fiveHour,
			ResetsAt:    strPtr(fiveHourReset.Format(time.RFC3339)),
			IsEnabled:   &boolTrue,
		},
		"seven_day": {
			Utilization: &sevenDay,
			ResetsAt:    strPtr(sevenDayReset.Format(time.RFC3339)),
			IsEnabled:   &boolTrue,
		},
		"seven_day_sonnet": {
			Utilization: &sevenDaySonnet,
			ResetsAt:    strPtr(sevenDayReset.Format(time.RFC3339)),
			IsEnabled:   &boolTrue,
		},
		"extra_usage": {
			Utilization: floatPtr(0),
			ResetsAt:    nil,
			IsEnabled:   &boolFalse,
		},
	}
	data, _ := json.Marshal(resp)
	return string(data)
}

func strPtr(s string) *string    { return &s }
func floatPtr(f float64) *float64 { return &f }

// DefaultAnthropicResponse returns a typical Anthropic response with moderate usage.
func DefaultAnthropicResponse() string {
	now := time.Now().UTC()
	fiveHourReset := now.Add(3 * time.Hour)
	sevenDayReset := now.Add(5 * 24 * time.Hour)
	return AnthropicResponseJSON(45.2, 12.8, 5.1, fiveHourReset, sevenDayReset)
}

// AnthropicResponseSequence returns n Anthropic responses with incrementing utilization.
func AnthropicResponseSequence(n int) []string {
	now := time.Now().UTC()
	fiveHourReset := now.Add(3 * time.Hour)
	sevenDayReset := now.Add(5 * 24 * time.Hour)
	responses := make([]string, n)
	for i := range n {
		fiveHour := 10.0 + float64(i)*8
		sevenDay := 5.0 + float64(i)*3
		sonnet := 2.0 + float64(i)*1.5
		responses[i] = AnthropicResponseJSON(fiveHour, sevenDay, sonnet, fiveHourReset, sevenDayReset)
	}
	return responses
}

// AnthropicResponseWithReset returns two responses where resets_at has changed.
func AnthropicResponseWithReset() (before, after string) {
	now := time.Now().UTC()
	beforeReset := now.Add(30 * time.Minute)
	afterReset := now.Add(5 * time.Hour)
	sevenDayReset := now.Add(5 * 24 * time.Hour)

	before = AnthropicResponseJSON(85.0, 30.0, 15.0, beforeReset, sevenDayReset)
	after = AnthropicResponseJSON(5.0, 30.5, 15.2, afterReset, sevenDayReset)
	return before, after
}

// AnthropicResponseNullQuotas returns a response where some quotas are null
// (e.g., extra_usage is null).
func AnthropicResponseNullQuotas() string {
	now := time.Now().UTC()
	fiveHourReset := now.Add(3 * time.Hour)
	sevenDayReset := now.Add(5 * 24 * time.Hour)

	boolTrue := true
	fiveHour := 45.2
	sevenDay := 12.8
	resp := map[string]*struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    *string  `json:"resets_at"`
		IsEnabled   *bool    `json:"is_enabled"`
	}{
		"five_hour": {
			Utilization: &fiveHour,
			ResetsAt:    strPtr(fiveHourReset.Format(time.RFC3339)),
			IsEnabled:   &boolTrue,
		},
		"seven_day": {
			Utilization: &sevenDay,
			ResetsAt:    strPtr(sevenDayReset.Format(time.RFC3339)),
			IsEnabled:   &boolTrue,
		},
		"extra_usage": nil,
	}
	data, _ := json.Marshal(resp)
	return string(data)
}
