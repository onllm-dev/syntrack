// Package agent provides the background polling agent for SynTrack.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/onllm-dev/syntrack/internal/api"
	"github.com/onllm-dev/syntrack/internal/store"
	"github.com/onllm-dev/syntrack/internal/tracker"
)

// ZaiAgent manages the background polling loop for Z.ai quota tracking.
type ZaiAgent struct {
	client    *api.ZaiClient
	store     *store.Store
	tracker   *tracker.ZaiTracker
	interval  time.Duration
	logger    *slog.Logger
	sessionID string
}

// NewZaiAgent creates a new ZaiAgent with the given dependencies.
func NewZaiAgent(client *api.ZaiClient, store *store.Store, tr *tracker.ZaiTracker, interval time.Duration, logger *slog.Logger) *ZaiAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &ZaiAgent{
		client:   client,
		store:    store,
		tracker:  tr,
		interval: interval,
		logger:   logger,
	}
}

// Run starts the Z.ai agent's polling loop. It creates a session, polls immediately,
// then continues at the configured interval until the context is cancelled.
func (a *ZaiAgent) Run(ctx context.Context) error {
	// Generate session ID
	a.sessionID = uuid.New().String()

	// Create session in database
	if err := a.store.CreateSession(a.sessionID, time.Now().UTC(), int(a.interval.Milliseconds()), "zai"); err != nil {
		return fmt.Errorf("zai agent: failed to create session: %w", err)
	}

	a.logger.Info("Z.ai agent started",
		"session_id", a.sessionID,
		"interval", a.interval,
	)

	// Ensure session is closed on exit
	defer func() {
		if err := a.store.CloseSession(a.sessionID, time.Now().UTC()); err != nil {
			a.logger.Error("Failed to close Z.ai session", "error", err)
		} else {
			a.logger.Info("Z.ai agent stopped", "session_id", a.sessionID)
		}
	}()

	// Poll immediately on start
	a.poll(ctx)

	// Create ticker for periodic polling
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	// Main polling loop
	for {
		select {
		case <-ticker.C:
			a.poll(ctx)
		case <-ctx.Done():
			return nil
		}
	}
}

// poll performs a single Z.ai poll cycle: fetch quotas, store snapshot.
func (a *ZaiAgent) poll(ctx context.Context) {
	resp, err := a.client.FetchQuotas(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		a.logger.Error("Failed to fetch Z.ai quotas", "error", err)
		return
	}

	// Convert to snapshot and store
	now := time.Now().UTC()
	snapshot := resp.ToSnapshot(now)

	if _, err := a.store.InsertZaiSnapshot(snapshot); err != nil {
		a.logger.Error("Failed to insert Z.ai snapshot", "error", err)
		return
	}

	// Increment snapshot count for successful storage
	if err := a.store.IncrementSnapshotCount(a.sessionID); err != nil {
		a.logger.Error("Failed to increment Z.ai snapshot count", "error", err)
	}

	// Process with tracker (log error but don't stop)
	if a.tracker != nil {
		if err := a.tracker.Process(snapshot); err != nil {
			a.logger.Error("Z.ai tracker processing failed", "error", err)
		}
	}

	// Update session max values (tokens = sub, time = search, tool calls = tool)
	if err := a.store.UpdateSessionMaxRequests(
		a.sessionID,
		snapshot.TokensCurrentValue,
		snapshot.TimeCurrentValue,
		0, // tool calls derived from time usage details
	); err != nil {
		a.logger.Error("Failed to update Z.ai session max", "error", err)
	}

	// Log poll completion
	a.logger.Info("Z.ai poll complete",
		"session_id", a.sessionID,
		"time_usage", snapshot.TimeUsage,
		"time_limit", snapshot.TimeLimit,
		"tokens_usage", snapshot.TokensUsage,
		"tokens_limit", snapshot.TokensLimit,
		"tokens_percentage", snapshot.TokensPercentage,
	)
}

// SessionID returns the current session ID. Returns empty string if Run() hasn't been called.
func (a *ZaiAgent) SessionID() string {
	return a.sessionID
}
