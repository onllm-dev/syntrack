// Package agent provides the background polling agent for onWatch.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
)

// AnthropicAgent manages the background polling loop for Anthropic quota tracking.
type AnthropicAgent struct {
	client    *api.AnthropicClient
	store     *store.Store
	tracker   *tracker.AnthropicTracker
	interval  time.Duration
	logger    *slog.Logger
	sessionID string
}

// NewAnthropicAgent creates a new AnthropicAgent with the given dependencies.
func NewAnthropicAgent(client *api.AnthropicClient, store *store.Store, tr *tracker.AnthropicTracker, interval time.Duration, logger *slog.Logger) *AnthropicAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &AnthropicAgent{
		client:   client,
		store:    store,
		tracker:  tr,
		interval: interval,
		logger:   logger,
	}
}

// Run starts the Anthropic agent's polling loop. It creates a session, polls immediately,
// then continues at the configured interval until the context is cancelled.
func (a *AnthropicAgent) Run(ctx context.Context) error {
	// Generate session ID
	a.sessionID = uuid.New().String()

	// Create session in database
	if err := a.store.CreateSession(a.sessionID, time.Now().UTC(), int(a.interval.Milliseconds()), "anthropic"); err != nil {
		return fmt.Errorf("anthropic agent: failed to create session: %w", err)
	}

	a.logger.Info("Anthropic agent started",
		"session_id", a.sessionID,
		"interval", a.interval,
	)

	// Ensure session is closed on exit
	defer func() {
		if err := a.store.CloseSession(a.sessionID, time.Now().UTC()); err != nil {
			a.logger.Error("Failed to close Anthropic session", "error", err)
		} else {
			a.logger.Info("Anthropic agent stopped", "session_id", a.sessionID)
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

// poll performs a single Anthropic poll cycle: fetch quotas, store snapshot, process with tracker.
func (a *AnthropicAgent) poll(ctx context.Context) {
	resp, err := a.client.FetchQuotas(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		a.logger.Error("Failed to fetch Anthropic quotas", "error", err)
		return
	}

	// Convert to snapshot and store
	now := time.Now().UTC()
	snapshot := resp.ToSnapshot(now)

	if _, err := a.store.InsertAnthropicSnapshot(snapshot); err != nil {
		a.logger.Error("Failed to insert Anthropic snapshot", "error", err)
		return
	}

	// Increment snapshot count for successful storage
	if err := a.store.IncrementSnapshotCount(a.sessionID); err != nil {
		a.logger.Error("Failed to increment Anthropic snapshot count", "error", err)
	}

	// Process with tracker (log error but don't stop)
	if a.tracker != nil {
		if err := a.tracker.Process(snapshot); err != nil {
			a.logger.Error("Anthropic tracker processing failed", "error", err)
		}
	}

	// Update session max values: use highest utilization among quotas as "sub" value
	var maxUtil float64
	quotaCount := len(snapshot.Quotas)
	for _, q := range snapshot.Quotas {
		if q.Utilization > maxUtil {
			maxUtil = q.Utilization
		}
	}

	if err := a.store.UpdateSessionMaxRequests(
		a.sessionID,
		maxUtil,
		0,
		0,
	); err != nil {
		a.logger.Error("Failed to update Anthropic session max", "error", err)
	}

	// Log poll completion
	a.logger.Info("Anthropic poll complete",
		"session_id", a.sessionID,
		"quota_count", quotaCount,
		"max_utilization", maxUtil,
	)
}

// SessionID returns the current session ID. Returns empty string if Run() hasn't been called.
func (a *AnthropicAgent) SessionID() string {
	return a.sessionID
}
