package api

import (
	"encoding/json"
	"sort"
	"time"
)

// AnthropicQuotaEntry represents a single quota entry from the Anthropic API.
// All fields are pointers because null values indicate the quota is not applicable.
type AnthropicQuotaEntry struct {
	Utilization  *float64 `json:"utilization"`
	ResetsAt     *string  `json:"resets_at"`
	IsEnabled    *bool    `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit,omitempty"`
	UsedCredits  *float64 `json:"used_credits,omitempty"`
}

// AnthropicQuotaResponse is the full response from the Anthropic usage API.
// Keys are dynamic (five_hour, seven_day, etc.).
type AnthropicQuotaResponse map[string]*AnthropicQuotaEntry

// AnthropicQuota represents a single normalized quota for storage.
type AnthropicQuota struct {
	Name        string
	Utilization float64
	ResetsAt    *time.Time
}

// AnthropicSnapshot represents a point-in-time capture of all Anthropic quotas.
type AnthropicSnapshot struct {
	ID         int64
	CapturedAt time.Time
	Quotas     []AnthropicQuota
	RawJSON    string
}

// anthropicDisplayNames maps API keys to human-readable labels.
var anthropicDisplayNames = map[string]string{
	"five_hour":        "5-Hour",
	"seven_day":        "7-Day",
	"seven_day_sonnet": "7-Day Sonnet",
	"monthly_limit":    "Monthly",
	"extra_usage":      "Extra Usage",
}

// AnthropicDisplayName returns the human-readable name for a quota key.
func AnthropicDisplayName(key string) string {
	if name, ok := anthropicDisplayNames[key]; ok {
		return name
	}
	return key
}

// ActiveQuotaNames returns sorted names of quotas that are active (non-null utilization,
// and not disabled via is_enabled=false). extra_usage with is_enabled=false is skipped.
func (r AnthropicQuotaResponse) ActiveQuotaNames() []string {
	var names []string
	for key, entry := range r {
		if entry == nil || entry.Utilization == nil {
			continue
		}
		// Skip disabled quotas (e.g., extra_usage with is_enabled=false)
		if entry.IsEnabled != nil && !*entry.IsEnabled {
			continue
		}
		names = append(names, key)
	}
	sort.Strings(names)
	return names
}

// ToSnapshot converts an AnthropicQuotaResponse to an AnthropicSnapshot.
func (r AnthropicQuotaResponse) ToSnapshot(capturedAt time.Time) *AnthropicSnapshot {
	snapshot := &AnthropicSnapshot{
		CapturedAt: capturedAt,
	}

	for _, name := range r.ActiveQuotaNames() {
		entry := r[name]
		q := AnthropicQuota{
			Name:        name,
			Utilization: *entry.Utilization,
		}
		if entry.ResetsAt != nil && *entry.ResetsAt != "" {
			if t, err := time.Parse(time.RFC3339, *entry.ResetsAt); err == nil {
				q.ResetsAt = &t
			}
		}
		snapshot.Quotas = append(snapshot.Quotas, q)
	}

	// Store raw JSON for debugging/auditing
	if raw, err := json.Marshal(r); err == nil {
		snapshot.RawJSON = string(raw)
	}

	return snapshot
}

// ParseAnthropicResponse parses raw JSON bytes into an AnthropicQuotaResponse.
func ParseAnthropicResponse(data []byte) (*AnthropicQuotaResponse, error) {
	var resp AnthropicQuotaResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
