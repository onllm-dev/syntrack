package tracker

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
)

// AntigravityTracker manages reset cycle detection and usage calculation for Antigravity models.
type AntigravityTracker struct {
	store          *store.Store
	logger         *slog.Logger
	lastFractions  map[string]float64   // model_id -> last remaining fraction
	lastResetTimes map[string]time.Time // model_id -> last reset time
	hasLastValues  bool

	onReset func(modelID string) // called when a model reset is detected
}

// SetOnReset registers a callback that is invoked when a model reset is detected.
func (t *AntigravityTracker) SetOnReset(fn func(string)) {
	t.onReset = fn
}

// AntigravitySummary contains computed usage statistics for an Antigravity model.
type AntigravitySummary struct {
	ModelID           string
	Label             string
	RemainingFraction float64
	UsagePercent      float64
	IsExhausted       bool
	ResetTime         *time.Time
	TimeUntilReset    time.Duration
	CurrentRate       float64 // usage per hour (0.0-1.0 scale)
	ProjectedUsage    float64 // projected usage at reset (0.0-1.0 scale)
	CompletedCycles   int
	AvgPerCycle       float64
	PeakCycle         float64
	TotalTracked      float64
	TrackingSince     time.Time
}

// NewAntigravityTracker creates a new AntigravityTracker.
func NewAntigravityTracker(store *store.Store, logger *slog.Logger) *AntigravityTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &AntigravityTracker{
		store:          store,
		logger:         logger,
		lastFractions:  make(map[string]float64),
		lastResetTimes: make(map[string]time.Time),
	}
}

// Process iterates over all models in the snapshot, detects resets, and updates cycles.
func (t *AntigravityTracker) Process(snapshot *api.AntigravitySnapshot) error {
	for _, model := range snapshot.Models {
		if err := t.processModel(model, snapshot.CapturedAt); err != nil {
			return fmt.Errorf("antigravity tracker: %s: %w", model.ModelID, err)
		}
	}

	t.hasLastValues = true
	return nil
}

// processModel handles cycle detection and tracking for a single Antigravity model.
func (t *AntigravityTracker) processModel(model api.AntigravityModelQuota, capturedAt time.Time) error {
	modelID := model.ModelID
	if modelID == "" {
		return nil // Skip models without ID
	}

	// Current usage (1.0 - remainingFraction)
	currentUsage := 1.0 - model.RemainingFraction

	cycle, err := t.store.QueryActiveAntigravityCycle(modelID)
	if err != nil {
		return fmt.Errorf("failed to query active cycle: %w", err)
	}

	if cycle == nil {
		// First snapshot for this model - create new cycle
		_, err := t.store.CreateAntigravityCycle(modelID, capturedAt, model.ResetTime)
		if err != nil {
			return fmt.Errorf("failed to create cycle: %w", err)
		}
		if err := t.store.UpdateAntigravityCycle(modelID, currentUsage, 0); err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}
		t.lastFractions[modelID] = model.RemainingFraction
		if model.ResetTime != nil {
			t.lastResetTimes[modelID] = *model.ResetTime
		}
		t.logger.Info("Created new Antigravity cycle",
			"model", modelID,
			"label", model.Label,
			"resetTime", model.ResetTime,
			"initialUsage", currentUsage,
		)
		return nil
	}

	// Reset detection
	resetDetected := false
	resetReason := ""

	// Method 1: Reset time changed significantly
	if model.ResetTime != nil {
		if lastReset, ok := t.lastResetTimes[modelID]; ok {
			diff := model.ResetTime.Sub(lastReset)
			if diff < 0 {
				diff = -diff
			}
			if diff > 10*time.Minute {
				resetDetected = true
				resetReason = "reset_time changed"
			}
		}
	}

	// Method 2: Remaining fraction increased significantly (quota reset)
	if !resetDetected && t.hasLastValues {
		if lastFraction, ok := t.lastFractions[modelID]; ok {
			// If remaining fraction increased by more than 10%, likely a reset
			if model.RemainingFraction > lastFraction+0.1 {
				resetDetected = true
				resetReason = "remaining_fraction increased"
			}
		}
	}

	// Method 3: Time-based check - if reset time passed and remaining went up
	if !resetDetected && cycle.ResetTime != nil && capturedAt.After(*cycle.ResetTime) {
		if lastFraction, ok := t.lastFractions[modelID]; ok {
			if model.RemainingFraction > lastFraction {
				resetDetected = true
				resetReason = "time-based (reset time passed + remaining increased)"
			}
		}
	}

	if resetDetected {
		// Close old cycle
		cycleEndTime := capturedAt
		if cycle.ResetTime != nil && capturedAt.After(*cycle.ResetTime) {
			cycleEndTime = *cycle.ResetTime
		}

		if err := t.store.CloseAntigravityCycle(modelID, cycleEndTime, cycle.PeakUsage, cycle.TotalDelta); err != nil {
			return fmt.Errorf("failed to close cycle: %w", err)
		}

		// Create new cycle
		if _, err := t.store.CreateAntigravityCycle(modelID, capturedAt, model.ResetTime); err != nil {
			return fmt.Errorf("failed to create new cycle: %w", err)
		}
		if err := t.store.UpdateAntigravityCycle(modelID, currentUsage, 0); err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}

		t.lastFractions[modelID] = model.RemainingFraction
		if model.ResetTime != nil {
			t.lastResetTimes[modelID] = *model.ResetTime
		}
		t.logger.Info("Detected Antigravity model reset",
			"model", modelID,
			"reason", resetReason,
			"oldResetTime", cycle.ResetTime,
			"newResetTime", model.ResetTime,
		)
		if t.onReset != nil {
			t.onReset(modelID)
		}
		return nil
	}

	// Same cycle - update stats
	if t.hasLastValues {
		if lastFraction, ok := t.lastFractions[modelID]; ok {
			// Usage delta: decrease in remaining fraction
			usageDelta := lastFraction - model.RemainingFraction
			if usageDelta > 0 {
				cycle.TotalDelta += usageDelta
			}
			if currentUsage > cycle.PeakUsage {
				cycle.PeakUsage = currentUsage
			}
			if err := t.store.UpdateAntigravityCycle(modelID, cycle.PeakUsage, cycle.TotalDelta); err != nil {
				return fmt.Errorf("failed to update cycle: %w", err)
			}
		} else {
			if currentUsage > cycle.PeakUsage {
				cycle.PeakUsage = currentUsage
				if err := t.store.UpdateAntigravityCycle(modelID, cycle.PeakUsage, cycle.TotalDelta); err != nil {
					return fmt.Errorf("failed to update cycle: %w", err)
				}
			}
		}
	} else {
		if currentUsage > cycle.PeakUsage {
			cycle.PeakUsage = currentUsage
			if err := t.store.UpdateAntigravityCycle(modelID, cycle.PeakUsage, cycle.TotalDelta); err != nil {
				return fmt.Errorf("failed to update cycle: %w", err)
			}
		}
	}

	t.lastFractions[modelID] = model.RemainingFraction
	if model.ResetTime != nil {
		t.lastResetTimes[modelID] = *model.ResetTime
	}
	return nil
}

// UsageSummary returns computed stats for a specific Antigravity model.
func (t *AntigravityTracker) UsageSummary(modelID string) (*AntigravitySummary, error) {
	activeCycle, err := t.store.QueryActiveAntigravityCycle(modelID)
	if err != nil {
		return nil, fmt.Errorf("failed to query active cycle: %w", err)
	}

	history, err := t.store.QueryAntigravityCycleHistory(modelID)
	if err != nil {
		return nil, fmt.Errorf("failed to query cycle history: %w", err)
	}

	summary := &AntigravitySummary{
		ModelID:         modelID,
		CompletedCycles: len(history),
	}

	// Calculate stats from completed cycles
	if len(history) > 0 {
		var totalDelta float64
		summary.TrackingSince = history[len(history)-1].CycleStart

		for _, cycle := range history {
			totalDelta += cycle.TotalDelta
			if cycle.PeakUsage > summary.PeakCycle {
				summary.PeakCycle = cycle.PeakUsage
			}
		}
		summary.AvgPerCycle = totalDelta / float64(len(history))
		summary.TotalTracked = totalDelta
	}

	// Add active cycle data
	if activeCycle != nil {
		summary.TotalTracked += activeCycle.TotalDelta
		if activeCycle.PeakUsage > summary.PeakCycle {
			summary.PeakCycle = activeCycle.PeakUsage
		}
		if activeCycle.ResetTime != nil {
			summary.ResetTime = activeCycle.ResetTime
			summary.TimeUntilReset = time.Until(*activeCycle.ResetTime)
			if summary.TimeUntilReset < 0 {
				summary.TimeUntilReset = 0
			}
		}

		// Get latest snapshot for current values
		latest, err := t.store.QueryLatestAntigravity()
		if err != nil {
			return nil, fmt.Errorf("failed to query latest: %w", err)
		}

		if latest != nil {
			for _, m := range latest.Models {
				if m.ModelID == modelID {
					summary.Label = m.Label
					summary.RemainingFraction = m.RemainingFraction
					summary.UsagePercent = (1.0 - m.RemainingFraction) * 100
					summary.IsExhausted = m.IsExhausted
					if summary.ResetTime == nil && m.ResetTime != nil {
						summary.ResetTime = m.ResetTime
						summary.TimeUntilReset = time.Until(*m.ResetTime)
						if summary.TimeUntilReset < 0 {
							summary.TimeUntilReset = 0
						}
					}
					break
				}
			}

			// Calculate rate from tracked usage within this cycle
			elapsed := time.Since(activeCycle.CycleStart)
			if elapsed.Minutes() >= 30 && activeCycle.TotalDelta > 0 {
				summary.CurrentRate = activeCycle.TotalDelta / elapsed.Hours()
				if summary.ResetTime != nil {
					hoursLeft := time.Until(*summary.ResetTime).Hours()
					if hoursLeft > 0 {
						currentUsage := 1.0 - summary.RemainingFraction
						projected := currentUsage + summary.CurrentRate*hoursLeft
						if projected > 1.0 {
							projected = 1.0
						}
						summary.ProjectedUsage = projected
					}
				}
			}
		}
	}

	return summary, nil
}
