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
// Reset detection: ResetsAt timestamp changes (like Z.ai tokens quota).
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

	// Check for reset: compare ResetsAt timestamps
	resetDetected := false
	if quota.ResetsAt != nil && cycle.ResetsAt != nil {
		if !quota.ResetsAt.Equal(*cycle.ResetsAt) {
			resetDetected = true
		}
	} else if quota.ResetsAt != nil && cycle.ResetsAt == nil {
		resetDetected = true
	}

	if resetDetected {
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

		// Close old cycle
		if err := t.store.CloseAnthropicCycle(quotaName, capturedAt, cycle.PeakUtilization, cycle.TotalDelta); err != nil {
			return fmt.Errorf("failed to close cycle: %w", err)
		}

		// Create new cycle
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
			"oldResetsAt", cycle.ResetsAt,
			"newResetsAt", quota.ResetsAt,
			"totalDelta", cycle.TotalDelta,
		)
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
			if cycle.TotalDelta > summary.PeakCycle {
				summary.PeakCycle = cycle.TotalDelta
			}
		}
		summary.AvgPerCycle = totalDelta / float64(len(history))
		summary.TotalTracked = totalDelta
	}

	// Add active cycle data
	if activeCycle != nil {
		summary.TotalTracked += activeCycle.TotalDelta
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

			// Calculate rate from active cycle timing
			elapsed := time.Since(activeCycle.CycleStart)
			if elapsed.Hours() > 0 && summary.CurrentUtil > 0 {
				summary.CurrentRate = summary.CurrentUtil / elapsed.Hours()
				if summary.ResetsAt != nil {
					hoursLeft := time.Until(*summary.ResetsAt).Hours()
					if hoursLeft > 0 {
						summary.ProjectedUtil = summary.CurrentUtil + (summary.CurrentRate * hoursLeft)
					}
				}
			}
		}
	}

	return summary, nil
}
