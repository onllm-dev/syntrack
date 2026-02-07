// Package agent provides the background polling agent for onWatch.
package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
)

// ZaiAgent manages the background polling loop for Z.ai quota tracking.
type ZaiAgent struct {
	client   *api.ZaiClient
	store    *store.Store
	tracker  *tracker.ZaiTracker
	interval time.Duration
	logger   *slog.Logger
	sm       *SessionManager
}

// NewZaiAgent creates a new ZaiAgent with the given dependencies.
func NewZaiAgent(client *api.ZaiClient, store *store.Store, tr *tracker.ZaiTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *ZaiAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &ZaiAgent{
		client:   client,
		store:    store,
		tracker:  tr,
		interval: interval,
		logger:   logger,
		sm:       sm,
	}
}

// Run starts the Z.ai agent's polling loop. It polls immediately,
// then continues at the configured interval until the context is cancelled.
func (a *ZaiAgent) Run(ctx context.Context) error {
	a.logger.Info("Z.ai agent started", "interval", a.interval)

	// Ensure any active session is closed on exit
	defer func() {
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("Z.ai agent stopped")
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

	// Process with tracker (log error but don't stop)
	if a.tracker != nil {
		if err := a.tracker.Process(snapshot); err != nil {
			a.logger.Error("Z.ai tracker processing failed", "error", err)
		}
	}

	// Report to session manager for usage-based session detection
	if a.sm != nil {
		a.sm.ReportPoll([]float64{
			snapshot.TokensCurrentValue,
			snapshot.TimeCurrentValue,
		})
	}

	// Log poll completion
	a.logger.Info("Z.ai poll complete",
		"time_usage", snapshot.TimeUsage,
		"time_limit", snapshot.TimeLimit,
		"tokens_usage", snapshot.TokensUsage,
		"tokens_limit", snapshot.TokensLimit,
		"tokens_percentage", snapshot.TokensPercentage,
	)
}
