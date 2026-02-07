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

// AnthropicAgent manages the background polling loop for Anthropic quota tracking.
type AnthropicAgent struct {
	client   *api.AnthropicClient
	store    *store.Store
	tracker  *tracker.AnthropicTracker
	interval time.Duration
	logger   *slog.Logger
	sm       *SessionManager
}

// NewAnthropicAgent creates a new AnthropicAgent with the given dependencies.
func NewAnthropicAgent(client *api.AnthropicClient, store *store.Store, tr *tracker.AnthropicTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *AnthropicAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &AnthropicAgent{
		client:   client,
		store:    store,
		tracker:  tr,
		interval: interval,
		logger:   logger,
		sm:       sm,
	}
}

// Run starts the Anthropic agent's polling loop. It polls immediately,
// then continues at the configured interval until the context is cancelled.
func (a *AnthropicAgent) Run(ctx context.Context) error {
	a.logger.Info("Anthropic agent started", "interval", a.interval)

	// Ensure any active session is closed on exit
	defer func() {
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("Anthropic agent stopped")
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

	// Process with tracker (log error but don't stop)
	if a.tracker != nil {
		if err := a.tracker.Process(snapshot); err != nil {
			a.logger.Error("Anthropic tracker processing failed", "error", err)
		}
	}

	// Report to session manager — extract utilization values for change detection
	if a.sm != nil {
		values := make([]float64, len(snapshot.Quotas))
		for i, q := range snapshot.Quotas {
			values[i] = q.Utilization
		}
		a.sm.ReportPoll(values)
	}

	// Log poll completion
	quotaCount := len(snapshot.Quotas)
	var maxUtil float64
	for _, q := range snapshot.Quotas {
		if q.Utilization > maxUtil {
			maxUtil = q.Utilization
		}
	}

	a.logger.Info("Anthropic poll complete",
		"quota_count", quotaCount,
		"max_utilization", maxUtil,
	)
}
