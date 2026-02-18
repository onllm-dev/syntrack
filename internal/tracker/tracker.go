package tracker

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
)

// Tracker manages reset cycle detection and usage calculation
type Tracker struct {
	store  *store.Store
	logger *slog.Logger

	// Cache of last seen values per quota type to calculate deltas
	lastSubRequests    float64
	lastSearchRequests float64
	lastToolRequests   float64
	hasLastValues      bool

	onReset func(quotaName string) // called when a quota reset is detected
}

// SetOnReset registers a callback that is invoked when a quota reset is detected.
func (t *Tracker) SetOnReset(fn func(string)) {
	t.onReset = fn
}

// Summary contains computed usage statistics
type Summary struct {
	QuotaType       string
	CurrentUsage    float64
	CurrentLimit    float64
	UsagePercent    float64
	RenewsAt        time.Time
	TimeUntilReset  time.Duration
	CurrentRate     float64 // requests per hour
	ProjectedUsage  float64 // estimated total before reset
	CompletedCycles int
	AvgPerCycle     float64
	PeakCycle       float64
	TotalTracked    float64
	TrackingSince   time.Time
}

// New creates a new Tracker
func New(store *store.Store, logger *slog.Logger) *Tracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Tracker{
		store:  store,
		logger: logger,
	}
}

// Process compares current snapshot with previous, detects resets, updates cycles
func (t *Tracker) Process(snapshot *api.Snapshot) error {
	// Process each quota type
	if err := t.processQuota("subscription", snapshot.CapturedAt, snapshot.Sub, &t.lastSubRequests); err != nil {
		return fmt.Errorf("tracker: subscription: %w", err)
	}
	if err := t.processQuota("search", snapshot.CapturedAt, snapshot.Search, &t.lastSearchRequests); err != nil {
		return fmt.Errorf("tracker: search: %w", err)
	}
	if err := t.processQuota("toolcall", snapshot.CapturedAt, snapshot.ToolCall, &t.lastToolRequests); err != nil {
		return fmt.Errorf("tracker: toolcall: %w", err)
	}

	t.hasLastValues = true
	return nil
}

func (t *Tracker) processQuota(quotaType string, capturedAt time.Time, info api.QuotaInfo, lastRequests *float64) error {
	// Get active cycle for this quota type
	cycle, err := t.store.QueryActiveCycle(quotaType)
	if err != nil {
		return fmt.Errorf("failed to query active cycle: %w", err)
	}

	if cycle == nil {
		// First snapshot - create new cycle with initial peak
		_, err := t.store.CreateCycle(quotaType, capturedAt, info.RenewsAt)
		if err != nil {
			return fmt.Errorf("failed to create cycle: %w", err)
		}
		// Set initial peak to current requests
		err = t.store.UpdateCycle(quotaType, info.Requests, 0)
		if err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}
		*lastRequests = info.Requests
		t.logger.Info("Created new cycle",
			"quotaType", quotaType,
			"renewsAt", info.RenewsAt,
			"initialPeak", info.Requests,
		)
		return nil
	}

	// Reset detection method 1: Time-based check
	// If the stored cycle's RenewsAt has passed, the quota has reset (even if app was offline).
	// Use a small grace period (2 min) to account for clock drift and API delays.
	resetDetected := false
	resetReason := ""
	if capturedAt.After(cycle.RenewsAt.Add(2 * time.Minute)) {
		resetDetected = true
		resetReason = "time-based (stored RenewsAt passed)"
	}

	// Reset detection method 2: API-based check
	// Compare renewsAt at hour precision (ignore minutes and seconds).
	// Synthetic API may return "rolling window" renewal times that shift forward
	// by the poll interval on each request (e.g., search quota's hourly window
	// returns "now + 1 hour"). Real resets shift by the full quota window duration
	// (1 hour for search, longer for subscription/toolcall), so comparing at hour
	// precision catches real resets while ignoring minute-level drift.
	if !resetDetected {
		oldHour := cycle.RenewsAt.Truncate(time.Hour)
		newHour := info.RenewsAt.Truncate(time.Hour)
		if !oldHour.Equal(newHour) {
			resetDetected = true
			resetReason = "api-based (RenewsAt changed)"
		}
	}

	if resetDetected {
		// Determine the actual cycle end time:
		// - If we have a stored RenewsAt and it's in the past, use it as cycle end
		// - Otherwise use capturedAt (API-based detection)
		cycleEndTime := capturedAt
		if capturedAt.After(cycle.RenewsAt) {
			cycleEndTime = cycle.RenewsAt
		}

		// Calculate final delta from the last snapshot before closing.
		// The delta includes the change up to the reset point.
		if t.hasLastValues {
			delta := info.Requests - *lastRequests
			if delta > 0 {
				cycle.TotalDelta += delta
			}
			// NOTE: We intentionally do NOT update peak_requests here.
			// The current snapshot's timestamp becomes cycle_end, and the
			// cross-quota query uses `captured_at < cycle_end`, so this
			// snapshot is excluded. Including its value in peak_requests
			// would create an inconsistency where the "max" shown in the
			// overview doesn't match any snapshot in the query range.
		}

		// Close old cycle at the actual reset time
		err := t.store.CloseCycle(quotaType, cycleEndTime, cycle.PeakRequests, cycle.TotalDelta)
		if err != nil {
			return fmt.Errorf("failed to close cycle: %w", err)
		}

		// Create new cycle starting from capturedAt (when we actually detected it)
		_, err = t.store.CreateCycle(quotaType, capturedAt, info.RenewsAt)
		if err != nil {
			return fmt.Errorf("failed to create new cycle: %w", err)
		}

		// Set initial peak for new cycle
		err = t.store.UpdateCycle(quotaType, info.Requests, 0)
		if err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}

		// Reset last requests for new cycle
		*lastRequests = info.Requests

		t.logger.Info("Detected quota reset",
			"quotaType", quotaType,
			"reason", resetReason,
			"oldRenewsAt", cycle.RenewsAt,
			"newRenewsAt", info.RenewsAt,
			"cycleEndTime", cycleEndTime,
			"totalDelta", cycle.TotalDelta,
		)
		if t.onReset != nil {
			t.onReset(quotaType)
		}
		return nil
	}

	// Same cycle - update stats
	if t.hasLastValues {
		// Calculate delta
		delta := info.Requests - *lastRequests

		// Only add positive deltas (ignore drops within same cycle)
		if delta > 0 {
			cycle.TotalDelta += delta
		}

		// Update peak (highest value seen in cycle)
		if info.Requests > cycle.PeakRequests {
			cycle.PeakRequests = info.Requests
		}

		// Update cycle in database
		err = t.store.UpdateCycle(quotaType, cycle.PeakRequests, cycle.TotalDelta)
		if err != nil {
			return fmt.Errorf("failed to update cycle: %w", err)
		}
	} else {
		// First snapshot after restart - set initial peak
		if info.Requests > cycle.PeakRequests {
			cycle.PeakRequests = info.Requests
			err = t.store.UpdateCycle(quotaType, cycle.PeakRequests, cycle.TotalDelta)
			if err != nil {
				return fmt.Errorf("failed to update cycle: %w", err)
			}
		}
	}

	*lastRequests = info.Requests
	return nil
}

// UsageSummary returns computed stats for a quota type
func (t *Tracker) UsageSummary(quotaType string) (*Summary, error) {
	// Get active cycle
	activeCycle, err := t.store.QueryActiveCycle(quotaType)
	if err != nil {
		return nil, fmt.Errorf("failed to query active cycle: %w", err)
	}

	// Get cycle history
	history, err := t.store.QueryCycleHistory(quotaType)
	if err != nil {
		return nil, fmt.Errorf("failed to query cycle history: %w", err)
	}

	summary := &Summary{
		QuotaType:       quotaType,
		CompletedCycles: len(history),
	}

	// Calculate stats from completed cycles
	if len(history) > 0 {
		var totalDelta float64
		summary.TrackingSince = history[len(history)-1].CycleStart

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
		summary.RenewsAt = activeCycle.RenewsAt
		summary.TimeUntilReset = time.Until(activeCycle.RenewsAt)

		// Get latest snapshot for current usage
		latest, err := t.store.QueryLatest()
		if err != nil {
			return nil, fmt.Errorf("failed to query latest: %w", err)
		}

		if latest != nil {
			switch quotaType {
			case "subscription":
				summary.CurrentUsage = latest.Sub.Requests
				summary.CurrentLimit = latest.Sub.Limit
			case "search":
				summary.CurrentUsage = latest.Search.Requests
				summary.CurrentLimit = latest.Search.Limit
			case "toolcall":
				summary.CurrentUsage = latest.ToolCall.Requests
				summary.CurrentLimit = latest.ToolCall.Limit
			}

			if summary.CurrentLimit > 0 {
				summary.UsagePercent = (summary.CurrentUsage / summary.CurrentLimit) * 100
			}
		}
	}

	return summary, nil
}
