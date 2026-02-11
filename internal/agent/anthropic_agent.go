// Package agent provides the background polling agent for onWatch.
package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/notify"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
)

// TokenRefreshFunc is called before each poll to get a fresh token.
// Returns the new token, or empty string if refresh is not needed/available.
type TokenRefreshFunc func() string

// CredentialsRefreshFunc returns the full credentials for proactive OAuth refresh.
type CredentialsRefreshFunc func() *api.AnthropicCredentials

// maxAuthFailures is the number of consecutive auth failures before pausing polling.
const maxAuthFailures = 3

// tokenRefreshThreshold is how soon before expiry we proactively refresh the token.
const tokenRefreshThreshold = 10 * time.Minute

// AnthropicAgent manages the background polling loop for Anthropic quota tracking.
type AnthropicAgent struct {
	client       *api.AnthropicClient
	store        *store.Store
	tracker      *tracker.AnthropicTracker
	interval     time.Duration
	logger       *slog.Logger
	sm           *SessionManager
	tokenRefresh TokenRefreshFunc
	credsRefresh CredentialsRefreshFunc
	lastToken    string
	notifier     *notify.NotificationEngine
	pollingCheck func() bool

	// Auth failure rate limiting
	authFailCount   int    // consecutive auth failures (401 or 403)
	authPaused      bool   // true when polling is paused due to auth failures
	lastFailedToken string // token that caused the failures (to detect credential refresh)
}

// SetPollingCheck sets a function that is called before each poll.
// If it returns false, the poll is skipped (provider polling disabled).
func (a *AnthropicAgent) SetPollingCheck(fn func() bool) {
	a.pollingCheck = fn
}

// SetNotifier sets the notification engine for sending alerts.
func (a *AnthropicAgent) SetNotifier(n *notify.NotificationEngine) {
	a.notifier = n
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

// SetTokenRefresh sets a function that will be called before each poll to
// refresh the Anthropic OAuth token. This enables automatic token rotation
// when Claude Code refreshes credentials on disk.
func (a *AnthropicAgent) SetTokenRefresh(fn TokenRefreshFunc) {
	a.tokenRefresh = fn
}

// SetCredentialsRefresh sets a function that returns full credentials for
// proactive OAuth token refresh before expiry.
func (a *AnthropicAgent) SetCredentialsRefresh(fn CredentialsRefreshFunc) {
	a.credsRefresh = fn
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

// isAuthError returns true if the error is an authentication/authorization error.
func isAuthError(err error) bool {
	return errors.Is(err, api.ErrAnthropicUnauthorized) || errors.Is(err, api.ErrAnthropicForbidden)
}

// poll performs a single Anthropic poll cycle: fetch quotas, store snapshot, process with tracker.
func (a *AnthropicAgent) poll(ctx context.Context) {
	if a.pollingCheck != nil && !a.pollingCheck() {
		return // polling disabled for this provider
	}

	// Proactive OAuth refresh: check if token expires soon and refresh via OAuth API
	if a.credsRefresh != nil {
		if creds := a.credsRefresh(); creds != nil {
			// Check if token is expiring soon or already expired
			if creds.IsExpiringSoon(tokenRefreshThreshold) && creds.RefreshToken != "" {
				a.logger.Info("Token expiring soon, attempting proactive OAuth refresh",
					"expires_in", creds.ExpiresIn.Round(time.Second))

				newTokens, err := api.RefreshAnthropicToken(ctx, creds.RefreshToken)
				if err != nil {
					a.logger.Error("Proactive OAuth refresh failed", "error", err)
					// Continue with existing token - it might still work
				} else {
					// CRITICAL: Save new tokens to disk IMMEDIATELY
					if err := api.WriteAnthropicCredentials(newTokens.AccessToken, newTokens.RefreshToken, newTokens.ExpiresIn); err != nil {
						a.logger.Error("Failed to save refreshed credentials", "error", err)
					} else {
						a.client.SetToken(newTokens.AccessToken)
						a.lastToken = newTokens.AccessToken
						a.logger.Info("Proactively refreshed OAuth token",
							"expires_in_hours", newTokens.ExpiresIn/3600)

						// Reset auth failures since we have fresh credentials
						if a.authPaused {
							a.authPaused = false
							a.authFailCount = 0
							a.lastFailedToken = ""
							a.logger.Info("Auth failure pause lifted - token refreshed via OAuth")
						}
					}
				}
			}
		}
	}

	// Refresh token before each poll (picks up rotated credentials from disk)
	var newToken string
	if a.tokenRefresh != nil {
		newToken = a.tokenRefresh()
		if newToken != "" && newToken != a.lastToken {
			a.client.SetToken(newToken)
			a.lastToken = newToken
			a.logger.Info("Anthropic token refreshed from credentials")

			// If we were paused due to auth failures and credentials changed, resume
			if a.authPaused && newToken != a.lastFailedToken {
				a.authPaused = false
				a.authFailCount = 0
				a.lastFailedToken = ""
				a.logger.Info("Auth failure pause lifted - new credentials detected")
			}
		}
	}

	// If auth is paused, skip polling until credentials change
	if a.authPaused {
		// Only log periodically to avoid spamming logs
		return
	}

	resp, err := a.client.FetchQuotas(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		// On auth error (401 or 403), force token re-read and retry once
		if isAuthError(err) && a.tokenRefresh != nil {
			a.logger.Warn("Anthropic auth error, forcing credential re-read", "error", err)
			a.lastToken = "" // force re-read even if token hasn't changed on disk
			if retryToken := a.tokenRefresh(); retryToken != "" {
				a.client.SetToken(retryToken)
				a.lastToken = retryToken
				a.logger.Info("Retrying with refreshed token")
				resp, err = a.client.FetchQuotas(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					// Retry also failed - count this as an auth failure
					if isAuthError(err) {
						a.authFailCount++
						a.logger.Error("Anthropic auth retry failed",
							"error", err,
							"failure_count", a.authFailCount,
							"max_failures", maxAuthFailures)

						if a.authFailCount >= maxAuthFailures {
							a.authPaused = true
							a.lastFailedToken = retryToken
							a.logger.Error("Anthropic polling PAUSED due to repeated auth failures",
								"failure_count", a.authFailCount,
								"action", "Re-authenticate with 'claude auth' to resume polling")
						}
					} else {
						a.logger.Error("Anthropic retry failed with non-auth error", "error", err)
					}
					return
				}
				// Retry succeeded — reset auth failure count and fall through
				a.authFailCount = 0
			} else {
				a.logger.Error("No Anthropic token available after re-read")
				return
			}
		} else {
			a.logger.Error("Failed to fetch Anthropic quotas", "error", err)
			return
		}
	} else {
		// Success — reset auth failure count
		a.authFailCount = 0
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

	// Check notification thresholds
	if a.notifier != nil {
		for _, q := range snapshot.Quotas {
			a.notifier.Check(notify.QuotaStatus{
				Provider:    "anthropic",
				QuotaKey:    q.Name,
				Utilization: q.Utilization,
			})
		}
	}

	// Report to session manager — extract utilization values for change detection.
	// Use fixed order matching UI columns: five_hour, seven_day, seven_day_sonnet
	// (alphabetical sort would put monthly_limit between them, breaking the mapping).
	if a.sm != nil {
		// Build a map for O(1) lookup
		quotaMap := make(map[string]float64, len(snapshot.Quotas))
		for _, q := range snapshot.Quotas {
			quotaMap[q.Name] = q.Utilization
		}
		// Report in fixed order matching session columns (sub, search, tool)
		values := []float64{
			quotaMap["five_hour"],        // Column 0: 5-Hour %
			quotaMap["seven_day"],        // Column 1: Weekly %
			quotaMap["seven_day_sonnet"], // Column 2: Sonnet %
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
