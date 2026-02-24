package agent

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/notify"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
)

// AntigravityAgent manages the background polling loop for Antigravity quota tracking.
// It supports both auto-detection of the local language server and manual configuration
// for Docker/containerized environments.
type AntigravityAgent struct {
	client       *api.AntigravityClient
	store        *store.Store
	tracker      *tracker.AntigravityTracker
	interval     time.Duration
	logger       *slog.Logger
	sm           *SessionManager
	notifier     *notify.NotificationEngine
	pollingCheck func() bool

	// Manual configuration for Docker environments
	manualBaseURL   string
	manualCSRFToken string
}

// AntigravityAgentOption configures an AntigravityAgent.
type AntigravityAgentOption func(*AntigravityAgent)

// WithAntigravityManualConfig sets manual connection config (for Docker environments).
// When set, auto-detection is skipped and the agent connects directly to the specified URL.
func WithAntigravityManualConfig(baseURL, csrfToken string) AntigravityAgentOption {
	return func(a *AntigravityAgent) {
		a.manualBaseURL = baseURL
		a.manualCSRFToken = csrfToken
	}
}

// SetPollingCheck sets a function that is called before each poll.
// If it returns false, the poll is skipped (provider polling disabled).
func (a *AntigravityAgent) SetPollingCheck(fn func() bool) {
	a.pollingCheck = fn
}

// SetNotifier sets the notification engine for sending alerts.
func (a *AntigravityAgent) SetNotifier(n *notify.NotificationEngine) {
	a.notifier = n
}

// NewAntigravityAgent creates a new AntigravityAgent with the given dependencies.
// For Docker environments, use WithAntigravityManualConfig to set connection details.
func NewAntigravityAgent(
	client *api.AntigravityClient,
	store *store.Store,
	tracker *tracker.AntigravityTracker,
	interval time.Duration,
	logger *slog.Logger,
	sm *SessionManager,
	opts ...AntigravityAgentOption,
) *AntigravityAgent {
	if logger == nil {
		logger = slog.Default()
	}
	agent := &AntigravityAgent{
		client:   client,
		store:    store,
		tracker:  tracker,
		interval: interval,
		logger:   logger,
		sm:       sm,
	}

	for _, opt := range opts {
		opt(agent)
	}

	// Check for Docker environment variables
	if agent.manualBaseURL == "" {
		agent.manualBaseURL = os.Getenv("ANTIGRAVITY_BASE_URL")
	}
	if agent.manualCSRFToken == "" {
		agent.manualCSRFToken = os.Getenv("ANTIGRAVITY_CSRF_TOKEN")
	}

	// If manual config is set, configure the client
	if agent.manualBaseURL != "" {
		conn := &api.AntigravityConnection{
			BaseURL:   agent.manualBaseURL,
			CSRFToken: agent.manualCSRFToken,
			Protocol:  "https",
		}
		agent.client = api.NewAntigravityClient(logger, api.WithAntigravityConnection(conn))
		logger.Info("Antigravity agent using manual configuration",
			"baseURL", agent.manualBaseURL,
			"hasToken", agent.manualCSRFToken != "",
		)
	}

	return agent
}

// Run starts the agent's polling loop. It polls immediately,
// then continues at the configured interval until the context is cancelled.
func (a *AntigravityAgent) Run(ctx context.Context) error {
	a.logger.Info("Antigravity agent started", "interval", a.interval)

	defer func() {
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("Antigravity agent stopped")
	}()

	// Poll immediately on start
	a.poll(ctx)

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.poll(ctx)
		case <-ctx.Done():
			return nil
		}
	}
}

// poll performs a single poll cycle: detect process, fetch quotas, store snapshot, update tracker.
func (a *AntigravityAgent) poll(ctx context.Context) {
	if a.pollingCheck != nil && !a.pollingCheck() {
		return
	}

	resp, err := a.client.FetchQuotas(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		a.logger.Error("Failed to fetch Antigravity quotas", "error", err)
		return
	}

	// Convert API response to snapshot
	now := time.Now().UTC()
	snapshot := resp.ToSnapshot(now)

	// Store snapshot
	if _, err := a.store.InsertAntigravitySnapshot(snapshot); err != nil {
		a.logger.Error("Failed to insert Antigravity snapshot", "error", err)
	}

	// Process with tracker
	if err := a.tracker.Process(snapshot); err != nil {
		a.logger.Error("Antigravity tracker processing failed", "error", err)
	}

	// Check notification thresholds for models
	if a.notifier != nil {
		for _, m := range snapshot.Models {
			if m.RemainingFraction == 1.0 {
				continue // Skip unused models
			}
			utilization := (1.0 - m.RemainingFraction) * 100
			a.notifier.Check(notify.QuotaStatus{
				Provider:    "antigravity",
				QuotaKey:    m.ModelID,
				Utilization: utilization,
				Limit:       100, // Percentage-based
			})
		}
	}

	// Report grouped values to session manager for stable session semantics
	if a.sm != nil {
		groups := api.GroupAntigravityModelsByLogicalQuota(snapshot.Models)
		valuesByKey := make(map[string]float64, len(groups))
		for _, g := range groups {
			valuesByKey[g.GroupKey] = g.UsagePercent
		}
		orderedValues := []float64{
			valuesByKey[api.AntigravityQuotaGroupClaudeGPT],
			valuesByKey[api.AntigravityQuotaGroupGeminiPro],
			valuesByKey[api.AntigravityQuotaGroupGeminiFlash],
		}
		a.sm.ReportPoll(orderedValues)
	}

	// Log poll completion
	for _, m := range snapshot.Models {
		if m.RemainingFraction < 1.0 {
			a.logger.Info("Antigravity poll complete",
				"model", m.ModelID,
				"label", m.Label,
				"remainingPercent", m.RemainingPercent,
				"exhausted", m.IsExhausted,
			)
		}
	}
}

// IsConnected returns true if the agent has a valid connection to the language server.
func (a *AntigravityAgent) IsConnected() bool {
	return a.client.IsConnected()
}

// GetAntigravityConfigFromEnv returns Antigravity configuration from environment variables.
// This is useful for Docker environments where the language server isn't local.
//
// Environment variables:
//   - ANTIGRAVITY_BASE_URL: Base URL of the Antigravity language server (e.g., "https://host.docker.internal:42100")
//   - ANTIGRAVITY_CSRF_TOKEN: CSRF token for authentication (extracted from process args on host)
func GetAntigravityConfigFromEnv() (baseURL, csrfToken string) {
	return os.Getenv("ANTIGRAVITY_BASE_URL"), os.Getenv("ANTIGRAVITY_CSRF_TOKEN")
}

// HasAntigravityEnvConfig returns true if Antigravity environment configuration is present.
func HasAntigravityEnvConfig() bool {
	return os.Getenv("ANTIGRAVITY_BASE_URL") != ""
}
