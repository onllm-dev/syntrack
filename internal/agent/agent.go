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

// Agent manages the background polling loop for quota tracking.
type Agent struct {
	client   *api.Client
	store    *store.Store
	tracker  *tracker.Tracker
	interval time.Duration
	logger   *slog.Logger
	sm       *SessionManager
}

// New creates a new Agent with the given dependencies.
func New(client *api.Client, store *store.Store, tracker *tracker.Tracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *Agent {
	if logger == nil {
		logger = slog.Default()
	}
	return &Agent{
		client:   client,
		store:    store,
		tracker:  tracker,
		interval: interval,
		logger:   logger,
		sm:       sm,
	}
}

// Run starts the agent's polling loop. It polls immediately,
// then continues at the configured interval until the context is cancelled.
// Sessions are managed by the SessionManager based on usage changes.
func (a *Agent) Run(ctx context.Context) error {
	a.logger.Info("Agent started", "interval", a.interval)

	// Ensure any active session is closed on exit
	defer func() {
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("Agent stopped")
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

// poll performs a single poll cycle: fetch quotas, store snapshot, update tracker.
func (a *Agent) poll(ctx context.Context) {
	// Fetch quotas from API
	resp, err := a.client.FetchQuotas(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// Context cancelled during request - this is expected during shutdown
			return
		}
		a.logger.Error("Failed to fetch quotas", "error", err)
		return
	}

	// Create snapshot from response
	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        resp.Subscription,
		Search:     resp.Search.Hourly,
		ToolCall:   resp.ToolCallDiscounts,
	}

	// Store snapshot (always do this, even if tracker fails)
	if _, err := a.store.InsertSnapshot(snapshot); err != nil {
		a.logger.Error("Failed to insert snapshot", "error", err)
	}

	// Process with tracker (log error but don't stop)
	if err := a.tracker.Process(snapshot); err != nil {
		a.logger.Error("Tracker processing failed", "error", err)
	}

	// Report to session manager for usage-based session detection
	if a.sm != nil {
		a.sm.ReportPoll([]float64{
			snapshot.Sub.Requests,
			snapshot.Search.Requests,
			snapshot.ToolCall.Requests,
		})
	}

	// Log poll completion with key metrics
	a.logger.Info("Poll complete",
		"sub_requests", resp.Subscription.Requests,
		"sub_limit", resp.Subscription.Limit,
		"search_requests", resp.Search.Hourly.Requests,
		"tool_requests", resp.ToolCallDiscounts.Requests,
		"sub_renews_at", resp.Subscription.RenewsAt,
	)
}
