package tracker

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
)

// AnthropicTracker manages reset cycle detection and usage calculation for Anthropic quotas.
// Unlike Synthetic/Z.ai trackers, Anthropic has a dynamic number of quotas (five_hour,
// seven_day, etc.) so tracking is done per-quota via maps.
type AnthropicTracker struct {
	store      *store.Store
	logger     *slog.Logger
	lastValues map[string]float64 // quota_name -> last utilization %
	lastResets map[string]string  // quota_name -> last resets_at string
	hasLast    bool

	onReset func(quotaName string) // called when a quota reset is detected
}

// SetOnReset registers a callback that is invoked when a quota reset is detected.
func (t *AnthropicTracker) SetOnReset(fn func(string)) {
	t.onReset = fn
}

// AnthropicSummary contains computed usage statistics for an Anthropic quota.
type AnthropicSummary struct {
	QuotaName       string
	CurrentUtil     float64
	ResetsAt        *time.Time
	TimeUntilReset  time.Duration
	CurrentRate     float64 // utilization % per hour
	ProjectedUtil   float64
	CompletedCycles int
	AvgPerCycle     float64
	PeakCycle       float64
	TotalTracked    float64
	TrackingSince   time.Time
}

// NewAnthropicTracker creates a new AnthropicTracker.
func NewAnthropicTracker(store *store.Store, logger *slog.Logger) *AnthropicTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &AnthropicTracker{
		store:      store,
		logger:     logger,
		lastValues: make(map[string]float64),
		lastResets: make(map[string]string),
	}
}

// Process iterates over all quotas in the snapshot, detects resets, and updates cycles.
func (t *AnthropicTracker) Process(snapshot *api.AnthropicSnapshot) error {
	for _, quota := range snapshot.Quotas {
		if err := t.processQuota(quota, snapshot.CapturedAt); err != nil {
			return fmt.Errorf("anthropic tracker: %s: %w", quota.Name, err)
		}
	}

	t.hasLast = true
	return nil
}

// processQuota handles cycle detection and tracking for a single Anthropic quota.
// Reset detection uses two methods:
// 1. Time-based: If the stored cycle's ResetsAt has passed, the cycle should have ended
// 2. API-based: If the API's ResetsAt differs significantly from stored (>10 min tolerance)
func (t *AnthropicTracker) processQuota(quota api.AnthropicQuota, capturedAt time.Time) error {
	quotaName := quota.Name
	currentUtil := quota.Utilization

	cycle, err := t.store.QueryActiveAnthropicCycle(quotaName)
	if err != nil {
		return fmt.Errorf("failed to query active cycle: %w", err)
	}

	if cycle == nil {
		// First snapshot for this quota -- create new cycle
		_, err := t.store.CreateAnthropicCycle(quotaName, capturedAt, quota.ResetsAt)
		if err != nil {
			return fmt.Errorf("failed to create cycle: %w", err)
		}
		if err := t.store.UpdateAnthropicCycle(quotaName, currentUtil, 0); err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}
		t.lastValues[quotaName] = currentUtil
		if quota.ResetsAt != nil {
			t.lastResets[quotaName] = quota.ResetsAt.Format(time.RFC3339Nano)
		}
		t.logger.Info("Created new Anthropic cycle",
			"quota", quotaName,
			"resetsAt", quota.ResetsAt,
			"initialUtil", currentUtil,
		)
		return nil
	}

	// Reset detection method 1: Time-based check
	// If the stored cycle's ResetsAt has passed, the quota has reset (even if app was offline).
	// Use a small grace period (2 min) to account for clock drift and API delays.
	resetDetected := false
	resetReason := ""
	if cycle.ResetsAt != nil && capturedAt.After(cycle.ResetsAt.Add(2*time.Minute)) {
		resetDetected = true
		resetReason = "time-based (stored ResetsAt passed)"
	}

	// Reset detection method 2: API-based check
	// Compare ResetsAt timestamps with 10-minute tolerance.
	// Anthropic's resets_at jitters by up to ±1 second on each API response
	// (e.g., 22:59:59.709 vs 23:00:00.313), sometimes crossing minute
	// boundaries. Real resets shift by ≥5 hours (the shortest quota window),
	// so any change <10 minutes is guaranteed to be API jitter.
	if !resetDetected {
		if quota.ResetsAt != nil && cycle.ResetsAt != nil {
			diff := quota.ResetsAt.Sub(*cycle.ResetsAt)
			if diff < 0 {
				diff = -diff
			}
			if diff > 10*time.Minute {
				resetDetected = true
				resetReason = "api-based (ResetsAt changed)"
			}
		} else if quota.ResetsAt != nil && cycle.ResetsAt == nil {
			resetDetected = true
			resetReason = "api-based (new ResetsAt appeared)"
		}
	}

	if resetDetected {
		// Determine the actual cycle end time:
		// - If we have a stored ResetsAt and it's in the past, use it as cycle end
		// - Otherwise use capturedAt (API-based detection)
		cycleEndTime := capturedAt
		if cycle.ResetsAt != nil && capturedAt.After(*cycle.ResetsAt) {
			cycleEndTime = *cycle.ResetsAt
		}

		// Update delta from last snapshot before closing
		if t.hasLast {
			if lastUtil, ok := t.lastValues[quotaName]; ok {
				delta := currentUtil - lastUtil
				if delta > 0 {
					cycle.TotalDelta += delta
				}
				if currentUtil > cycle.PeakUtilization {
					cycle.PeakUtilization = currentUtil
				}
			}
		}

		// Close old cycle at the actual reset time
		if err := t.store.CloseAnthropicCycle(quotaName, cycleEndTime, cycle.PeakUtilization, cycle.TotalDelta); err != nil {
			return fmt.Errorf("failed to close cycle: %w", err)
		}

		// Create new cycle starting from capturedAt (when we actually detected it)
		if _, err := t.store.CreateAnthropicCycle(quotaName, capturedAt, quota.ResetsAt); err != nil {
			return fmt.Errorf("failed to create new cycle: %w", err)
		}
		if err := t.store.UpdateAnthropicCycle(quotaName, currentUtil, 0); err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}

		t.lastValues[quotaName] = currentUtil
		if quota.ResetsAt != nil {
			t.lastResets[quotaName] = quota.ResetsAt.Format(time.RFC3339Nano)
		}
		t.logger.Info("Detected Anthropic quota reset",
			"quota", quotaName,
			"reason", resetReason,
			"oldResetsAt", cycle.ResetsAt,
			"newResetsAt", quota.ResetsAt,
			"cycleEndTime", cycleEndTime,
			"totalDelta", cycle.TotalDelta,
		)
		if t.onReset != nil {
			t.onReset(quotaName)
		}
		return nil
	}

	// Same cycle -- update stats
	if t.hasLast {
		if lastUtil, ok := t.lastValues[quotaName]; ok {
			delta := currentUtil - lastUtil
			if delta > 0 {
				cycle.TotalDelta += delta
			}
			if currentUtil > cycle.PeakUtilization {
				cycle.PeakUtilization = currentUtil
			}
			if err := t.store.UpdateAnthropicCycle(quotaName, cycle.PeakUtilization, cycle.TotalDelta); err != nil {
				return fmt.Errorf("failed to update cycle: %w", err)
			}
		} else {
			// First time seeing this quota after tracker started -- update peak if higher
			if currentUtil > cycle.PeakUtilization {
				cycle.PeakUtilization = currentUtil
				if err := t.store.UpdateAnthropicCycle(quotaName, cycle.PeakUtilization, cycle.TotalDelta); err != nil {
					return fmt.Errorf("failed to update cycle: %w", err)
				}
			}
		}
	} else {
		// First snapshot after restart -- update peak if higher
		if currentUtil > cycle.PeakUtilization {
			cycle.PeakUtilization = currentUtil
			if err := t.store.UpdateAnthropicCycle(quotaName, cycle.PeakUtilization, cycle.TotalDelta); err != nil {
				return fmt.Errorf("failed to update cycle: %w", err)
			}
		}
	}

	t.lastValues[quotaName] = currentUtil
	if quota.ResetsAt != nil {
		t.lastResets[quotaName] = quota.ResetsAt.Format(time.RFC3339Nano)
	}
	return nil
}

// UsageSummary returns computed stats for a specific Anthropic quota.
func (t *AnthropicTracker) UsageSummary(quotaName string) (*AnthropicSummary, error) {
	activeCycle, err := t.store.QueryActiveAnthropicCycle(quotaName)
	if err != nil {
		return nil, fmt.Errorf("failed to query active cycle: %w", err)
	}

	history, err := t.store.QueryAnthropicCycleHistory(quotaName)
	if err != nil {
		return nil, fmt.Errorf("failed to query cycle history: %w", err)
	}

	summary := &AnthropicSummary{
		QuotaName:       quotaName,
		CompletedCycles: len(history),
	}

	// Calculate stats from completed cycles
	if len(history) > 0 {
		var totalDelta float64
		summary.TrackingSince = history[len(history)-1].CycleStart // oldest cycle (history is DESC)

		for _, cycle := range history {
			totalDelta += cycle.TotalDelta
			if cycle.PeakUtilization > summary.PeakCycle {
				summary.PeakCycle = cycle.PeakUtilization
			}
		}
		summary.AvgPerCycle = totalDelta / float64(len(history))
		summary.TotalTracked = totalDelta
	}

	// Add active cycle data
	if activeCycle != nil {
		summary.TotalTracked += activeCycle.TotalDelta
		if activeCycle.PeakUtilization > summary.PeakCycle {
			summary.PeakCycle = activeCycle.PeakUtilization
		}
		if activeCycle.ResetsAt != nil {
			summary.ResetsAt = activeCycle.ResetsAt
			summary.TimeUntilReset = time.Until(*activeCycle.ResetsAt)
		}

		// Get latest snapshot for current utilization
		latest, err := t.store.QueryLatestAnthropic()
		if err != nil {
			return nil, fmt.Errorf("failed to query latest: %w", err)
		}

		if latest != nil {
			// Find the matching quota in the latest snapshot
			for _, q := range latest.Quotas {
				if q.Name == quotaName {
					summary.CurrentUtil = q.Utilization
					if summary.ResetsAt == nil && q.ResetsAt != nil {
						summary.ResetsAt = q.ResetsAt
						summary.TimeUntilReset = time.Until(*q.ResetsAt)
					}
					break
				}
			}

			// Calculate rate from tracked delta within this cycle
			// Require at least 30 min of data for meaningful rate
			elapsed := time.Since(activeCycle.CycleStart)
			if elapsed.Minutes() >= 30 && activeCycle.TotalDelta > 0 {
				summary.CurrentRate = activeCycle.TotalDelta / elapsed.Hours()
				if summary.ResetsAt != nil {
					hoursLeft := time.Until(*summary.ResetsAt).Hours()
					if hoursLeft > 0 {
						projected := summary.CurrentUtil + (summary.CurrentRate * hoursLeft)
						if projected > 100 {
							projected = 100
						}
						summary.ProjectedUtil = projected
					}
				}
			}
		}
	}

	return summary, nil
}
