package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// ZaiResponse is the generic wrapper for all Z.ai API responses
type ZaiResponse[T any] struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Success bool   `json:"success"`
	Data    T      `json:"data"`
}

// ZaiQuotaResponse is the response from GET /monitor/usage/quota/limit
type ZaiQuotaResponse struct {
	Limits []ZaiLimit `json:"limits"`
}

// ZaiLimit represents an individual limit (TIME_LIMIT or TOKENS_LIMIT)
type ZaiLimit struct {
	Type         string           `json:"type"`
	Unit         int              `json:"unit"`
	Number       int              `json:"number"`
	Usage        float64          `json:"usage"`
	CurrentValue float64          `json:"currentValue"`
	Remaining    float64          `json:"remaining"`
	Percentage   int              `json:"percentage"`
	NextResetMs  *int64           `json:"nextResetTime,omitempty"`
	UsageDetails []ZaiUsageDetail `json:"usageDetails,omitempty"`
}

// ZaiUsageDetail represents per-model usage breakdown
type ZaiUsageDetail struct {
	ModelCode string  `json:"modelCode"`
	Usage     float64 `json:"usage"`
}

// GetResetTime returns the reset time as a time.Time pointer.
// Returns nil if there is no reset time (TIME_LIMIT has no reset).
func (l *ZaiLimit) GetResetTime() *time.Time {
	if l.NextResetMs == nil {
		return nil
	}
	// Z.ai returns epoch milliseconds
	t := time.UnixMilli(*l.NextResetMs)
	return &t
}

// ZaiSnapshot is the storage representation (flat, for SQLite)
type ZaiSnapshot struct {
	ID         int64
	CapturedAt time.Time
	// TIME_LIMIT fields
	TimeLimit        int
	TimeUnit         int
	TimeNumber       int
	TimeUsage        float64
	TimeCurrentValue float64
	TimeRemaining    float64
	TimePercentage   int
	// TOKENS_LIMIT fields
	TokensLimit         int
	TokensUnit          int
	TokensNumber        int
	TokensUsage         float64
	TokensCurrentValue  float64
	TokensRemaining     float64
	TokensPercentage    int
	TokensNextResetTime *time.Time
}

// ToSnapshot converts ZaiQuotaResponse to ZaiSnapshot
func (r *ZaiQuotaResponse) ToSnapshot(capturedAt time.Time) *ZaiSnapshot {
	snapshot := &ZaiSnapshot{
		CapturedAt: capturedAt,
	}

	for _, limit := range r.Limits {
		switch limit.Type {
		case "TIME_LIMIT":
			snapshot.TimeLimit = limit.Unit * limit.Number
			snapshot.TimeUnit = limit.Unit
			snapshot.TimeNumber = limit.Number
			snapshot.TimeUsage = limit.Usage
			snapshot.TimeCurrentValue = limit.CurrentValue
			snapshot.TimeRemaining = limit.Remaining
			snapshot.TimePercentage = limit.Percentage
		case "TOKENS_LIMIT":
			snapshot.TokensLimit = limit.Unit * limit.Number
			snapshot.TokensUnit = limit.Unit
			snapshot.TokensNumber = limit.Number
			snapshot.TokensUsage = limit.Usage
			snapshot.TokensCurrentValue = limit.CurrentValue
			snapshot.TokensRemaining = limit.Remaining
			snapshot.TokensPercentage = limit.Percentage
			if limit.NextResetMs != nil {
				t := time.UnixMilli(*limit.NextResetMs)
				snapshot.TokensNextResetTime = &t
			}
		}
	}

	return snapshot
}

// ParseZaiResponse parses a Z.ai API response from JSON bytes
func ParseZaiResponse(data []byte) (*ZaiQuotaResponse, error) {
	var wrapper ZaiResponse[ZaiQuotaResponse]
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}

	if !wrapper.Success {
		return nil, fmt.Errorf("API error: code=%d, msg=%s", wrapper.Code, wrapper.Msg)
	}

	return &wrapper.Data, nil
}
