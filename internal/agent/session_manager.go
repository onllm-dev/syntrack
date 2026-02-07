package agent

import (
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/onllm-dev/onwatch/internal/store"
)

// SessionManager provides usage-based session detection for any provider.
// A session starts when API usage values change, and closes after an idle
// timeout with no further changes. Each agent gets its own SessionManager.
type SessionManager struct {
	store       *store.Store
	provider    string
	idleTimeout time.Duration
	logger      *slog.Logger

	sessionID        string    // empty = no active session
	lastActivityTime time.Time // last time usage changed
	prevValues       []float64 // previous poll values for comparison
	hasPrev          bool      // true after first poll (baseline established)
}

// NewSessionManager creates a SessionManager for the given provider.
func NewSessionManager(store *store.Store, provider string, idleTimeout time.Duration, logger *slog.Logger) *SessionManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionManager{
		store:       store,
		provider:    provider,
		idleTimeout: idleTimeout,
		logger:      logger,
	}
}

// ReportPoll is called after each successful poll with current usage values.
// values is a flat slice of comparable numbers (requests, utilization, etc.).
// Returns true if usage changed (session is active).
func (sm *SessionManager) ReportPoll(values []float64) bool {
	now := time.Now().UTC()
	changed := sm.hasChanged(values)

	// Save old previous values before overwriting (needed for session start values)
	oldPrev := make([]float64, len(sm.prevValues))
	copy(oldPrev, sm.prevValues)

	// Update stored previous values
	sm.prevValues = make([]float64, len(values))
	copy(sm.prevValues, values)
	if !sm.hasPrev {
		sm.hasPrev = true
		return false // first poll is always baseline, never a change
	}

	if changed {
		if sm.sessionID == "" {
			// Start new session — pass previous values as start values (baseline before the change)
			sm.sessionID = uuid.New().String()
			sm.lastActivityTime = now

			// Extract start values from the previous baseline (before this change)
			var startSub, startSearch, startTool float64
			if len(oldPrev) > 0 {
				startSub = oldPrev[0]
			}
			if len(oldPrev) > 1 {
				startSearch = oldPrev[1]
			}
			if len(oldPrev) > 2 {
				startTool = oldPrev[2]
			}

			if err := sm.store.CreateSession(sm.sessionID, now, 0, sm.provider, startSub, startSearch, startTool); err != nil {
				sm.logger.Error("Failed to create session", "provider", sm.provider, "error", err)
				sm.sessionID = ""
				return true
			}
			sm.logger.Info("Usage session started", "provider", sm.provider, "session_id", sm.sessionID)
		}

		sm.lastActivityTime = now
		sm.incrementAndUpdate(values)
		return true
	}

	// No usage change
	if sm.sessionID != "" {
		if now.Sub(sm.lastActivityTime) > sm.idleTimeout {
			// Idle timeout exceeded → close session
			sm.closeSession(now)
		} else {
			// Still within idle window — count the snapshot
			sm.incrementAndUpdate(values)
		}
	}

	return false
}

// Close closes any active session (called on agent shutdown).
func (sm *SessionManager) Close() {
	if sm.sessionID == "" {
		return
	}
	sm.closeSession(time.Now().UTC())
}

// hasChanged compares current values with previous values.
func (sm *SessionManager) hasChanged(values []float64) bool {
	if !sm.hasPrev {
		return false
	}
	if len(values) != len(sm.prevValues) {
		return true
	}
	for i, v := range values {
		if v != sm.prevValues[i] {
			return true
		}
	}
	return false
}

// closeSession closes the current active session.
func (sm *SessionManager) closeSession(endTime time.Time) {
	if err := sm.store.CloseSession(sm.sessionID, endTime); err != nil {
		sm.logger.Error("Failed to close session", "provider", sm.provider, "session_id", sm.sessionID, "error", err)
	} else {
		sm.logger.Info("Usage session ended", "provider", sm.provider, "session_id", sm.sessionID)
	}
	sm.sessionID = ""
}

// incrementAndUpdate increments the snapshot count and updates session max values.
func (sm *SessionManager) incrementAndUpdate(values []float64) {
	if err := sm.store.IncrementSnapshotCount(sm.sessionID); err != nil {
		sm.logger.Error("Failed to increment snapshot count", "provider", sm.provider, "error", err)
	}

	// Extract up to 3 values for session max (sub, search, tool)
	var sub, search, tool float64
	if len(values) > 0 {
		sub = values[0]
	}
	if len(values) > 1 {
		search = values[1]
	}
	if len(values) > 2 {
		tool = values[2]
	}

	if err := sm.store.UpdateSessionMaxRequests(sm.sessionID, sub, search, tool); err != nil {
		sm.logger.Error("Failed to update session max", "provider", sm.provider, "error", err)
	}
}
