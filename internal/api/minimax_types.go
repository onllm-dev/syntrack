package api

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"
)

// MiniMaxBaseResp contains top-level API status metadata.
type MiniMaxBaseResp struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

// MiniMaxModelRemain represents quota remain data for one model.
type MiniMaxModelRemain struct {
	ModelName string `json:"model_name"`

	// MiniMax currently returns epoch milliseconds for these fields,
	// but we also accept timestamp strings for compatibility.
	StartTime   interface{} `json:"start_time"`
	EndTime     interface{} `json:"end_time"`
	RemainsTime int64       `json:"remains_time"`

	CurrentIntervalTotalCount int `json:"current_interval_total_count"`
	CurrentIntervalUsageCount int `json:"current_interval_usage_count"`

	// Legacy fields retained for compatibility with older fixtures.
	Total          int    `json:"total"`
	Remain         int    `json:"remain"`
	Used           int    `json:"used"`
	ResetInMinutes int    `json:"reset_in"`
	NextResetTime  string `json:"next_reset_time"`
	RemainsTimeMs  int64  `json:"remains_time_ms"`
}

// MiniMaxRemainsResponse is the full response for coding plan remains.
type MiniMaxRemainsResponse struct {
	BaseResp     MiniMaxBaseResp      `json:"base_resp"`
	ModelRemains []MiniMaxModelRemain `json:"model_remains"`
}

// MiniMaxModelQuota is a normalized model quota record for storage.
type MiniMaxModelQuota struct {
	ModelName      string
	Total          int
	Remain         int
	Used           int
	UsedPercent    float64
	ResetAt        *time.Time
	WindowStart    *time.Time
	WindowEnd      *time.Time
	TimeUntilReset time.Duration
}

// MiniMaxSnapshot is a point-in-time capture of MiniMax model remains.
type MiniMaxSnapshot struct {
	ID         int64
	CapturedAt time.Time
	Models     []MiniMaxModelQuota
	RawJSON    string
}

// MiniMaxDisplayName returns user-facing display text for a model key.
func MiniMaxDisplayName(key string) string {
	return key
}

// ActiveModelNames returns sorted unique model names from the response.
func (r MiniMaxRemainsResponse) ActiveModelNames() []string {
	seen := make(map[string]struct{})
	var names []string
	for _, model := range r.ModelRemains {
		if model.ModelName == "" {
			continue
		}
		if _, ok := seen[model.ModelName]; ok {
			continue
		}
		seen[model.ModelName] = struct{}{}
		names = append(names, model.ModelName)
	}
	sort.Strings(names)
	return names
}

func parseMiniMaxTimestamp(v interface{}) *time.Time {
	switch ts := v.(type) {
	case string:
		if ts == "" {
			return nil
		}
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			return &t
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, time.UTC); err == nil {
			return &t
		}
		if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
			t := time.UnixMilli(n).UTC()
			return &t
		}
	case float64:
		t := time.UnixMilli(int64(ts)).UTC()
		return &t
	case int64:
		t := time.UnixMilli(ts).UTC()
		return &t
	case int:
		t := time.UnixMilli(int64(ts)).UTC()
		return &t
	}
	return nil
}

func normalizeMiniMaxCounts(model MiniMaxModelRemain) (total int, remain int, used int) {
	total = model.CurrentIntervalTotalCount
	used = model.CurrentIntervalUsageCount
	if total > 0 {
		remain = total - used
		if remain < 0 {
			remain = 0
		}
		return total, remain, used
	}

	// Legacy fallback.
	total = model.Total
	used = model.Used
	remain = model.Remain
	if total > 0 && remain == 0 && used >= 0 {
		remain = total - used
		if remain < 0 {
			remain = 0
		}
	}
	return total, remain, used
}

func normalizeMiniMaxReset(model MiniMaxModelRemain) (*time.Time, time.Duration) {
	if model.RemainsTime > 0 {
		d := time.Duration(model.RemainsTime) * time.Millisecond
		resetAt := time.Now().UTC().Add(d)
		return &resetAt, d
	}
	if end := parseMiniMaxTimestamp(model.EndTime); end != nil {
		return end, time.Until(*end)
	}

	// Legacy fallback.
	if model.RemainsTimeMs > 0 {
		d := time.Duration(model.RemainsTimeMs) * time.Millisecond
		resetAt := time.Now().UTC().Add(d)
		return &resetAt, d
	}
	if next := parseMiniMaxTimestamp(model.NextResetTime); next != nil {
		return next, time.Until(*next)
	}
	return nil, 0
}

// ToSnapshot converts MiniMaxRemainsResponse to MiniMaxSnapshot.
func (r MiniMaxRemainsResponse) ToSnapshot(capturedAt time.Time) *MiniMaxSnapshot {
	snapshot := &MiniMaxSnapshot{CapturedAt: capturedAt}

	modelByName := make(map[string]MiniMaxModelRemain, len(r.ModelRemains))
	for _, model := range r.ModelRemains {
		if model.ModelName == "" {
			continue
		}
		modelByName[model.ModelName] = model
	}

	for _, name := range r.ActiveModelNames() {
		model := modelByName[name]
		total, remain, used := normalizeMiniMaxCounts(model)
		resetAt, untilReset := normalizeMiniMaxReset(model)
		quota := MiniMaxModelQuota{
			ModelName:      model.ModelName,
			Total:          total,
			Remain:         remain,
			Used:           used,
			ResetAt:        resetAt,
			WindowStart:    parseMiniMaxTimestamp(model.StartTime),
			WindowEnd:      parseMiniMaxTimestamp(model.EndTime),
			TimeUntilReset: untilReset,
		}
		if quota.Total > 0 {
			quota.UsedPercent = (float64(quota.Used) / float64(quota.Total)) * 100
		}
		snapshot.Models = append(snapshot.Models, quota)
	}

	if raw, err := json.Marshal(r); err == nil {
		snapshot.RawJSON = string(raw)
	}

	return snapshot
}

// ParseMiniMaxResponse parses raw JSON bytes into MiniMaxRemainsResponse.
func ParseMiniMaxResponse(data []byte) (*MiniMaxRemainsResponse, error) {
	var resp MiniMaxRemainsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
