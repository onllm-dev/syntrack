package api

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CodexUsageResponse is the OAuth usage payload returned by Codex.
type CodexUsageResponse struct {
	PlanType            string         `json:"plan_type"`
	RateLimit           codexRateLimit `json:"rate_limit"`
	CodeReviewRateLimit codexRateLimit `json:"code_review_rate_limit,omitempty"`
	Credits             *codexCredits  `json:"credits,omitempty"`
}

type codexRateLimit struct {
	PrimaryWindow   *codexWindow `json:"primary_window"`
	SecondaryWindow *codexWindow `json:"secondary_window"`
}

type codexWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	ResetAtUnix        int64   `json:"reset_at"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
}

type codexCredits struct {
	Balance codexFloat64 `json:"balance,omitempty"`
}

type codexFloat64 struct {
	Value *float64
}

func (f *codexFloat64) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "null" || s == "" {
		f.Value = nil
		return nil
	}

	var num float64
	if err := json.Unmarshal(data, &num); err == nil {
		f.Value = &num
		return nil
	}

	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(str), 64)
		if err != nil {
			return fmt.Errorf("invalid float string %q: %w", str, err)
		}
		f.Value = &parsed
		return nil
	}

	return fmt.Errorf("invalid float value: %s", s)
}

// CodexQuota represents one normalized Codex quota.
type CodexQuota struct {
	Name        string
	Utilization float64
	ResetsAt    *time.Time
	Status      string
}

// CodexSnapshot stores normalized Codex usage state.
type CodexSnapshot struct {
	ID             int64
	CapturedAt     time.Time
	Quotas         []CodexQuota
	PlanType       string
	CreditsBalance *float64
	RawJSON        string
}

var codexDisplayNames = map[string]string{
	"five_hour":   "5-Hour Limit",
	"seven_day":   "Weekly All-Model",
	"code_review": "Review Requests",
}

// CodexDisplayName returns a display label for a codex quota key.
func CodexDisplayName(key string) string {
	if name, ok := codexDisplayNames[key]; ok {
		return name
	}
	return key
}

// ParseCodexUsageResponse parses raw JSON bytes into CodexUsageResponse.
func ParseCodexUsageResponse(data []byte) (*CodexUsageResponse, error) {
	var resp CodexUsageResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ToSnapshot converts a CodexUsageResponse to a normalized CodexSnapshot.
func (r CodexUsageResponse) ToSnapshot(capturedAt time.Time) *CodexSnapshot {
	snapshot := &CodexSnapshot{
		CapturedAt: capturedAt,
		PlanType:   r.PlanType,
	}

	if r.Credits != nil && r.Credits.Balance.Value != nil {
		balance := *r.Credits.Balance.Value
		snapshot.CreditsBalance = &balance
	}

	if r.RateLimit.PrimaryWindow != nil {
		snapshot.Quotas = append(snapshot.Quotas, codexQuotaFromWindow("five_hour", r.RateLimit.PrimaryWindow))
	}
	if r.RateLimit.SecondaryWindow != nil {
		snapshot.Quotas = append(snapshot.Quotas, codexQuotaFromWindow("seven_day", r.RateLimit.SecondaryWindow))
	}
	if r.CodeReviewRateLimit.PrimaryWindow != nil {
		snapshot.Quotas = append(snapshot.Quotas, codexQuotaFromWindow("code_review", r.CodeReviewRateLimit.PrimaryWindow))
	}

	sort.Slice(snapshot.Quotas, func(i, j int) bool {
		left := codexQuotaSortOrder(snapshot.Quotas[i].Name)
		right := codexQuotaSortOrder(snapshot.Quotas[j].Name)
		if left != right {
			return left < right
		}
		return snapshot.Quotas[i].Name < snapshot.Quotas[j].Name
	})

	if raw, err := json.Marshal(r); err == nil {
		snapshot.RawJSON = string(raw)
	}

	return snapshot
}

func codexQuotaFromWindow(name string, window *codexWindow) CodexQuota {
	q := CodexQuota{
		Name:        name,
		Utilization: window.UsedPercent,
		Status:      codexStatusFromUtilization(window.UsedPercent),
	}
	if window.ResetAtUnix > 0 {
		resetAt := time.Unix(window.ResetAtUnix, 0).UTC()
		q.ResetsAt = &resetAt
	}
	return q
}

func codexQuotaSortOrder(name string) int {
	switch name {
	case "five_hour":
		return 0
	case "seven_day":
		return 1
	case "code_review":
		return 2
	default:
		return 100
	}
}

func codexStatusFromUtilization(utilization float64) string {
	switch {
	case utilization >= 95:
		return "critical"
	case utilization >= 80:
		return "danger"
	case utilization >= 50:
		return "warning"
	default:
		return "healthy"
	}
}
