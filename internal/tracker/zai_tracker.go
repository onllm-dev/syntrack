package tracker

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
)

// ZaiTracker manages reset cycle detection and usage calculation for Z.ai quotas.
type ZaiTracker struct {
	store  *store.Store
	logger *slog.Logger

	// Cache last seen values for delta calculation
	lastTokensValue float64
	lastTimeValue   float64
	hasLastValues   bool

	onReset func(quotaName string) // called when a quota reset is detected
}

// SetOnReset registers a callback that is invoked when a quota reset is detected.
func (t *ZaiTracker) SetOnReset(fn func(string)) {
	t.onReset = fn
}

// ZaiSummary contains computed usage statistics for a Z.ai quota type.
type ZaiSummary struct {
	QuotaType       string
	CurrentUsage    float64
	CurrentLimit    float64
	UsagePercent    float64
	RenewsAt        *time.Time
	TimeUntilReset  time.Duration
	CurrentRate     float64 // per hour
	ProjectedUsage  float64
	CompletedCycles int
	AvgPerCycle     float64
	PeakCycle       float64
	TotalTracked    float64
	TrackingSince   time.Time
}

// NewZaiTracker creates a new ZaiTracker.
func NewZaiTracker(store *store.Store, logger *slog.Logger) *ZaiTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &ZaiTracker{
		store:  store,
		logger: logger,
	}
}

// Process compares current snapshot with previous, detects resets, updates cycles.
func (t *ZaiTracker) Process(snapshot *api.ZaiSnapshot) error {
	if err := t.processTokensQuota(snapshot); err != nil {
		return fmt.Errorf("zai tracker: tokens: %w", err)
	}
	if err := t.processTimeQuota(snapshot); err != nil {
		return fmt.Errorf("zai tracker: time: %w", err)
	}

	t.hasLastValues = true
	return nil
}

// processTokensQuota tracks the tokens quota cycle.
// Reset detection: TokensNextResetTime changes.
func (t *ZaiTracker) processTokensQuota(snapshot *api.ZaiSnapshot) error {
	quotaType := "tokens"
	currentValue := snapshot.TokensCurrentValue

	cycle, err := t.store.QueryActiveZaiCycle(quotaType)
	if err != nil {
		return fmt.Errorf("failed to query active cycle: %w", err)
	}

	if cycle == nil {
		// First snapshot — create new cycle
		_, err := t.store.CreateZaiCycle(quotaType, snapshot.CapturedAt, snapshot.TokensNextResetTime)
		if err != nil {
			return fmt.Errorf("failed to create cycle: %w", err)
		}
		if err := t.store.UpdateZaiCycle(quotaType, int64(currentValue), 0); err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}
		t.lastTokensValue = currentValue
		t.logger.Info("Created new Z.ai tokens cycle",
			"nextReset", snapshot.TokensNextResetTime,
			"initialPeak", currentValue,
		)
		return nil
	}

	// Reset detection method 1: Time-based check
	// If the stored cycle's NextReset has passed, the quota has reset (even if app was offline).
	// Use a small grace period (2 min) to account for clock drift and API delays.
	resetDetected := false
	resetReason := ""
	if cycle.NextReset != nil && snapshot.CapturedAt.After(cycle.NextReset.Add(2*time.Minute)) {
		resetDetected = true
		resetReason = "time-based (stored NextReset passed)"
	}

	// Reset detection method 2: API-based check
	// Compare nextResetTime timestamps
	if !resetDetected {
		if snapshot.TokensNextResetTime != nil && cycle.NextReset != nil {
			if !snapshot.TokensNextResetTime.Equal(*cycle.NextReset) {
				resetDetected = true
				resetReason = "api-based (NextReset changed)"
			}
		} else if snapshot.TokensNextResetTime != nil && cycle.NextReset == nil {
			resetDetected = true
			resetReason = "api-based (new NextReset appeared)"
		}
	}

	if resetDetected {
		// Determine the actual cycle end time:
		// - If we have a stored NextReset and it's in the past, use it as cycle end
		// - Otherwise use capturedAt (API-based detection)
		cycleEndTime := snapshot.CapturedAt
		if cycle.NextReset != nil && snapshot.CapturedAt.After(*cycle.NextReset) {
			cycleEndTime = *cycle.NextReset
		}

		// Update delta from last snapshot before closing
		if t.hasLastValues {
			delta := currentValue - t.lastTokensValue
			if delta > 0 {
				cycle.TotalDelta += int64(delta)
			}
			if int64(currentValue) > cycle.PeakValue {
				cycle.PeakValue = int64(currentValue)
			}
		}

		// Close old cycle at the actual reset time
		if err := t.store.CloseZaiCycle(quotaType, cycleEndTime, cycle.PeakValue, cycle.TotalDelta); err != nil {
			return fmt.Errorf("failed to close cycle: %w", err)
		}

		// Create new cycle starting from capturedAt (when we actually detected it)
		if _, err := t.store.CreateZaiCycle(quotaType, snapshot.CapturedAt, snapshot.TokensNextResetTime); err != nil {
			return fmt.Errorf("failed to create new cycle: %w", err)
		}
		if err := t.store.UpdateZaiCycle(quotaType, int64(currentValue), 0); err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}

		t.lastTokensValue = currentValue
		t.logger.Info("Detected Z.ai tokens reset",
			"reason", resetReason,
			"oldNextReset", cycle.NextReset,
			"newNextReset", snapshot.TokensNextResetTime,
			"cycleEndTime", cycleEndTime,
			"totalDelta", cycle.TotalDelta,
		)
		if t.onReset != nil {
			t.onReset(quotaType)
		}
		return nil
	}

	// Same cycle — update stats
	if t.hasLastValues {
		delta := currentValue - t.lastTokensValue
		if delta > 0 {
			cycle.TotalDelta += int64(delta)
		}
		if int64(currentValue) > cycle.PeakValue {
			cycle.PeakValue = int64(currentValue)
		}
		if err := t.store.UpdateZaiCycle(quotaType, cycle.PeakValue, cycle.TotalDelta); err != nil {
			return fmt.Errorf("failed to update cycle: %w", err)
		}
	} else {
		// First snapshot after restart — update peak if higher
		if int64(currentValue) > cycle.PeakValue {
			cycle.PeakValue = int64(currentValue)
			if err := t.store.UpdateZaiCycle(quotaType, cycle.PeakValue, cycle.TotalDelta); err != nil {
				return fmt.Errorf("failed to update cycle: %w", err)
			}
		}
	}

	t.lastTokensValue = currentValue
	return nil
}

// processTimeQuota tracks the time quota cycle.
// Reset detection: value drops significantly (TIME_LIMIT has no nextResetTime).
func (t *ZaiTracker) processTimeQuota(snapshot *api.ZaiSnapshot) error {
	quotaType := "time"
	currentValue := snapshot.TimeCurrentValue

	cycle, err := t.store.QueryActiveZaiCycle(quotaType)
	if err != nil {
		return fmt.Errorf("failed to query active cycle: %w", err)
	}

	if cycle == nil {
		// First snapshot — create new cycle (no nextReset for TIME_LIMIT)
		_, err := t.store.CreateZaiCycle(quotaType, snapshot.CapturedAt, nil)
		if err != nil {
			return fmt.Errorf("failed to create cycle: %w", err)
		}
		if err := t.store.UpdateZaiCycle(quotaType, int64(currentValue), 0); err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}
		t.lastTimeValue = currentValue
		t.logger.Info("Created new Z.ai time cycle",
			"initialPeak", currentValue,
		)
		return nil
	}

	// Check for reset: detect significant drop in value
	resetDetected := false
	if t.hasLastValues && t.lastTimeValue > 0 && currentValue < t.lastTimeValue*0.5 {
		resetDetected = true
	}

	if resetDetected {
		// Close old cycle with final stats
		if err := t.store.CloseZaiCycle(quotaType, snapshot.CapturedAt, cycle.PeakValue, cycle.TotalDelta); err != nil {
			return fmt.Errorf("failed to close cycle: %w", err)
		}

		// Create new cycle
		if _, err := t.store.CreateZaiCycle(quotaType, snapshot.CapturedAt, nil); err != nil {
			return fmt.Errorf("failed to create new cycle: %w", err)
		}
		if err := t.store.UpdateZaiCycle(quotaType, int64(currentValue), 0); err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}

		t.lastTimeValue = currentValue
		t.logger.Info("Detected Z.ai time reset",
			"lastValue", t.lastTimeValue,
			"newValue", currentValue,
			"totalDelta", cycle.TotalDelta,
		)
		if t.onReset != nil {
			t.onReset(quotaType)
		}
		return nil
	}

	// Same cycle — update stats
	if t.hasLastValues {
		delta := currentValue - t.lastTimeValue
		if delta > 0 {
			cycle.TotalDelta += int64(delta)
		}
		if int64(currentValue) > cycle.PeakValue {
			cycle.PeakValue = int64(currentValue)
		}
		if err := t.store.UpdateZaiCycle(quotaType, cycle.PeakValue, cycle.TotalDelta); err != nil {
			return fmt.Errorf("failed to update cycle: %w", err)
		}
	} else {
		if int64(currentValue) > cycle.PeakValue {
			cycle.PeakValue = int64(currentValue)
			if err := t.store.UpdateZaiCycle(quotaType, cycle.PeakValue, cycle.TotalDelta); err != nil {
				return fmt.Errorf("failed to update cycle: %w", err)
			}
		}
	}

	t.lastTimeValue = currentValue
	return nil
}

// UsageSummary returns computed stats for a Z.ai quota type.
func (t *ZaiTracker) UsageSummary(quotaType string) (*ZaiSummary, error) {
	activeCycle, err := t.store.QueryActiveZaiCycle(quotaType)
	if err != nil {
		return nil, fmt.Errorf("failed to query active cycle: %w", err)
	}

	history, err := t.store.QueryZaiCycleHistory(quotaType)
	if err != nil {
		return nil, fmt.Errorf("failed to query cycle history: %w", err)
	}

	summary := &ZaiSummary{
		QuotaType:       quotaType,
		CompletedCycles: len(history),
	}

	// Calculate stats from completed cycles
	if len(history) > 0 {
		var totalDelta int64
		summary.TrackingSince = history[len(history)-1].CycleStart // oldest cycle (history is DESC)

		for _, cycle := range history {
			totalDelta += cycle.TotalDelta
			if float64(cycle.TotalDelta) > summary.PeakCycle {
				summary.PeakCycle = float64(cycle.TotalDelta)
			}
		}
		summary.AvgPerCycle = float64(totalDelta) / float64(len(history))
		summary.TotalTracked = float64(totalDelta)
	}

	// Add active cycle data
	if activeCycle != nil {
		summary.TotalTracked += float64(activeCycle.TotalDelta)
		if activeCycle.NextReset != nil {
			summary.RenewsAt = activeCycle.NextReset
			summary.TimeUntilReset = time.Until(*activeCycle.NextReset)
		}

		// Get latest snapshot for current usage
		latest, err := t.store.QueryLatestZai()
		if err != nil {
			return nil, fmt.Errorf("failed to query latest: %w", err)
		}

		if latest != nil {
			switch quotaType {
			case "tokens":
				summary.CurrentUsage = latest.TokensCurrentValue
				summary.CurrentLimit = latest.TokensUsage // Z.ai: "usage" = budget
				if summary.RenewsAt == nil && latest.TokensNextResetTime != nil {
					summary.RenewsAt = latest.TokensNextResetTime
					summary.TimeUntilReset = time.Until(*latest.TokensNextResetTime)
				}
			case "time":
				summary.CurrentUsage = latest.TimeCurrentValue
				summary.CurrentLimit = latest.TimeUsage // Z.ai: "usage" = budget
			}

			if summary.CurrentLimit > 0 {
				summary.UsagePercent = (summary.CurrentUsage / summary.CurrentLimit) * 100
			}

			// Calculate rate from active cycle timing
			elapsed := time.Since(activeCycle.CycleStart)
			if elapsed.Hours() > 0 && summary.CurrentUsage > 0 {
				summary.CurrentRate = summary.CurrentUsage / elapsed.Hours()
				if summary.RenewsAt != nil {
					hoursLeft := time.Until(*summary.RenewsAt).Hours()
					if hoursLeft > 0 {
						summary.ProjectedUsage = summary.CurrentUsage + (summary.CurrentRate * hoursLeft)
					}
				}
			}
		}
	}

	return summary, nil
}
