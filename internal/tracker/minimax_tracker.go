package tracker

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
)

// MiniMaxTracker manages reset cycle detection and usage calculation per MiniMax model.
type MiniMaxTracker struct {
	store           *store.Store
	logger          *slog.Logger
	lastUsed        map[string]int
	lastResetAt     map[string]string
	lastWindowStart map[string]string
	lastWindowEnd   map[string]string
	hasLastValues   bool

	onReset func(modelName string)
}

// MiniMaxSummary contains computed usage statistics for a MiniMax model.
type MiniMaxSummary struct {
	ModelName       string
	Total           int
	CurrentRemain   int
	CurrentUsed     int
	UsagePercent    float64
	ResetAt         *time.Time
	TimeUntilReset  time.Duration
	CurrentRate     float64
	ProjectedUsage  int
	CompletedCycles int
	AvgPerCycle     float64
	PeakCycle       int
	TotalTracked    int
	TrackingSince   time.Time
}

// NewMiniMaxTracker creates a new MiniMaxTracker.
func NewMiniMaxTracker(store *store.Store, logger *slog.Logger) *MiniMaxTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &MiniMaxTracker{
		store:           store,
		logger:          logger,
		lastUsed:        make(map[string]int),
		lastResetAt:     make(map[string]string),
		lastWindowStart: make(map[string]string),
		lastWindowEnd:   make(map[string]string),
	}
}

// SetOnReset registers a callback invoked when a model reset is detected.
func (t *MiniMaxTracker) SetOnReset(fn func(string)) {
	t.onReset = fn
}

// Process processes all model quotas from a MiniMax snapshot.
func (t *MiniMaxTracker) Process(snapshot *api.MiniMaxSnapshot) error {
	for _, model := range snapshot.Models {
		if err := t.processModel(model, snapshot.CapturedAt); err != nil {
			return fmt.Errorf("minimax tracker: %s: %w", model.ModelName, err)
		}
	}
	t.hasLastValues = true
	return nil
}

func (t *MiniMaxTracker) processModel(model api.MiniMaxModelQuota, capturedAt time.Time) error {
	modelName := model.ModelName
	currentUsed := model.Used
	resetAtStr := ""
	if model.ResetAt != nil {
		resetAtStr = model.ResetAt.Format(time.RFC3339Nano)
	}
	windowStart := ""
	if model.WindowStart != nil {
		windowStart = model.WindowStart.Format(time.RFC3339Nano)
	}
	windowEnd := ""
	if model.WindowEnd != nil {
		windowEnd = model.WindowEnd.Format(time.RFC3339Nano)
	}

	cycle, err := t.store.QueryActiveMiniMaxCycle(modelName)
	if err != nil {
		return fmt.Errorf("failed to query active cycle: %w", err)
	}

	if cycle == nil {
		_, err := t.store.CreateMiniMaxCycle(modelName, capturedAt, model.ResetAt)
		if err != nil {
			return fmt.Errorf("failed to create cycle: %w", err)
		}
		if err := t.store.UpdateMiniMaxCycle(modelName, currentUsed, 0); err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}
		t.lastUsed[modelName] = currentUsed
		t.lastResetAt[modelName] = resetAtStr
		t.lastWindowStart[modelName] = windowStart
		t.lastWindowEnd[modelName] = windowEnd
		return nil
	}

	resetDetected := false
	if lastReset, ok := t.lastResetAt[modelName]; ok && lastReset != "" && resetAtStr != "" && resetAtStr != lastReset {
		resetDetected = true
	}
	if !resetDetected {
		if lastStart, ok := t.lastWindowStart[modelName]; ok && lastStart != "" && windowStart != "" && lastStart != windowStart {
			resetDetected = true
		}
	}
	if !resetDetected {
		if lastEnd, ok := t.lastWindowEnd[modelName]; ok && lastEnd != "" && windowEnd != "" && lastEnd != windowEnd {
			resetDetected = true
		}
	}
	if !resetDetected {
		if lastUsed, ok := t.lastUsed[modelName]; ok && lastUsed > 0 && currentUsed < lastUsed/2 {
			resetDetected = true
		}
	}

	if resetDetected {
		if err := t.store.CloseMiniMaxCycle(modelName, capturedAt, cycle.PeakUsed, cycle.TotalDelta); err != nil {
			return fmt.Errorf("failed to close cycle: %w", err)
		}
		if _, err := t.store.CreateMiniMaxCycle(modelName, capturedAt, model.ResetAt); err != nil {
			return fmt.Errorf("failed to create new cycle: %w", err)
		}
		if err := t.store.UpdateMiniMaxCycle(modelName, currentUsed, 0); err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}
		t.lastUsed[modelName] = currentUsed
		t.lastResetAt[modelName] = resetAtStr
		t.lastWindowStart[modelName] = windowStart
		t.lastWindowEnd[modelName] = windowEnd
		if t.onReset != nil {
			t.onReset(modelName)
		}
		return nil
	}

	if t.hasLastValues {
		if lastUsed, ok := t.lastUsed[modelName]; ok {
			usageDelta := currentUsed - lastUsed
			if usageDelta > 0 {
				cycle.TotalDelta += usageDelta
			}
			if currentUsed > cycle.PeakUsed {
				cycle.PeakUsed = currentUsed
			}
			if err := t.store.UpdateMiniMaxCycle(modelName, cycle.PeakUsed, cycle.TotalDelta); err != nil {
				return fmt.Errorf("failed to update cycle: %w", err)
			}
		}
	} else {
		if currentUsed > cycle.PeakUsed {
			cycle.PeakUsed = currentUsed
			if err := t.store.UpdateMiniMaxCycle(modelName, cycle.PeakUsed, cycle.TotalDelta); err != nil {
				return fmt.Errorf("failed to update cycle: %w", err)
			}
		}
	}

	t.lastUsed[modelName] = currentUsed
	t.lastResetAt[modelName] = resetAtStr
	t.lastWindowStart[modelName] = windowStart
	t.lastWindowEnd[modelName] = windowEnd
	return nil
}

// UsageSummary returns computed usage stats for a specific MiniMax model.
func (t *MiniMaxTracker) UsageSummary(modelName string) (*MiniMaxSummary, error) {
	activeCycle, err := t.store.QueryActiveMiniMaxCycle(modelName)
	if err != nil {
		return nil, fmt.Errorf("failed to query active cycle: %w", err)
	}

	history, err := t.store.QueryMiniMaxCycleHistory(modelName)
	if err != nil {
		return nil, fmt.Errorf("failed to query cycle history: %w", err)
	}

	summary := &MiniMaxSummary{ModelName: modelName, CompletedCycles: len(history)}
	if len(history) > 0 {
		var totalDelta int
		summary.TrackingSince = history[len(history)-1].CycleStart
		for _, cycle := range history {
			totalDelta += cycle.TotalDelta
			if cycle.PeakUsed > summary.PeakCycle {
				summary.PeakCycle = cycle.PeakUsed
			}
		}
		summary.AvgPerCycle = float64(totalDelta) / float64(len(history))
		summary.TotalTracked = totalDelta
	}

	if activeCycle != nil {
		summary.TotalTracked += activeCycle.TotalDelta
		if activeCycle.PeakUsed > summary.PeakCycle {
			summary.PeakCycle = activeCycle.PeakUsed
		}
		if activeCycle.ResetAt != nil {
			summary.ResetAt = activeCycle.ResetAt
			summary.TimeUntilReset = time.Until(*activeCycle.ResetAt)
		}

		latest, err := t.store.QueryLatestMiniMax()
		if err != nil {
			return nil, fmt.Errorf("failed to query latest: %w", err)
		}
		if latest != nil {
			for _, m := range latest.Models {
				if m.ModelName == modelName {
					summary.Total = m.Total
					summary.CurrentRemain = m.Remain
					summary.CurrentUsed = m.Used
					summary.UsagePercent = m.UsedPercent
					if summary.ResetAt == nil && m.ResetAt != nil {
						summary.ResetAt = m.ResetAt
						summary.TimeUntilReset = time.Until(*m.ResetAt)
					}
					break
				}
			}

			elapsed := time.Since(activeCycle.CycleStart)
			if elapsed.Minutes() >= 30 && activeCycle.TotalDelta > 0 {
				summary.CurrentRate = float64(activeCycle.TotalDelta) / elapsed.Hours()
				if summary.ResetAt != nil && summary.Total > 0 {
					hoursLeft := time.Until(*summary.ResetAt).Hours()
					if hoursLeft > 0 {
						projected := summary.CurrentUsed + int(summary.CurrentRate*hoursLeft)
						if projected > summary.Total {
							projected = summary.Total
						}
						summary.ProjectedUsage = projected
					}
				}
			}
		}
	}

	return summary, nil
}
