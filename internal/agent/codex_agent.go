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

// maxCodexAuthFailures is the number of consecutive auth failures before pausing polling.
const maxCodexAuthFailures = 3

// CodexTokenRefreshFunc is called before each poll to get a fresh Codex token.
type CodexTokenRefreshFunc func() string

// isCodexAuthError returns true if the error is an authentication/authorization error.
func isCodexAuthError(err error) bool {
	return errors.Is(err, api.ErrCodexUnauthorized) || errors.Is(err, api.ErrCodexForbidden)
}

// CodexAgent manages the background polling loop for Codex quota tracking.
type CodexAgent struct {
	client       *api.CodexClient
	store        *store.Store
	tracker      *tracker.CodexTracker
	interval     time.Duration
	logger       *slog.Logger
	sm           *SessionManager
	notifier     *notify.NotificationEngine
	pollingCheck func() bool
	tokenRefresh CodexTokenRefreshFunc
	lastToken    string

	// Auth failure rate limiting
	authFailCount   int
	authPaused      bool
	lastFailedToken string
}

// NewCodexAgent creates a new CodexAgent with the given dependencies.
func NewCodexAgent(client *api.CodexClient, store *store.Store, tracker *tracker.CodexTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *CodexAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &CodexAgent{
		client:   client,
		store:    store,
		tracker:  tracker,
		interval: interval,
		logger:   logger,
		sm:       sm,
	}
}

// SetPollingCheck sets a function called before each poll.
func (a *CodexAgent) SetPollingCheck(fn func() bool) {
	a.pollingCheck = fn
}

// SetNotifier sets notification engine for sending alerts.
func (a *CodexAgent) SetNotifier(n *notify.NotificationEngine) {
	a.notifier = n
}

// SetTokenRefresh sets a function called before each poll to refresh Codex token from credentials.
func (a *CodexAgent) SetTokenRefresh(fn CodexTokenRefreshFunc) {
	a.tokenRefresh = fn
}

// Run starts the agent polling loop.
func (a *CodexAgent) Run(ctx context.Context) error {
	a.logger.Info("Codex agent started", "interval", a.interval)

	defer func() {
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("Codex agent stopped")
	}()

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

func (a *CodexAgent) poll(ctx context.Context) {
	if a.pollingCheck != nil && !a.pollingCheck() {
		return
	}

	// Refresh token before each poll (picks up rotated credentials from disk)
	if a.tokenRefresh != nil {
		newToken := a.tokenRefresh()
		if newToken != "" && newToken != a.lastToken {
			a.client.SetToken(newToken)
			a.lastToken = newToken
			a.logger.Info("Codex token refreshed from credentials")

			// If we were paused due to auth failures and credentials changed, resume.
			if a.authPaused && newToken != a.lastFailedToken {
				a.authPaused = false
				a.authFailCount = 0
				a.lastFailedToken = ""
				a.logger.Info("Codex auth failure pause lifted - new credentials detected")
			}
		}
	}

	// If auth is paused, skip polling until credentials change.
	if a.authPaused {
		return
	}

	resp, err := a.client.FetchUsage(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}

		// On auth error, force token re-read and retry once.
		if isCodexAuthError(err) && a.tokenRefresh != nil {
			a.logger.Warn("Codex auth error, forcing credential re-read", "error", err)
			a.lastToken = "" // force re-read even if unchanged on disk
			if retryToken := a.tokenRefresh(); retryToken != "" {
				a.client.SetToken(retryToken)
				a.lastToken = retryToken
				a.logger.Info("Retrying Codex poll with refreshed token")
				resp, err = a.client.FetchUsage(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					if isCodexAuthError(err) {
						a.authFailCount++
						a.logger.Error("Codex auth retry failed",
							"error", err,
							"failure_count", a.authFailCount,
							"max_failures", maxCodexAuthFailures)

						if a.authFailCount >= maxCodexAuthFailures {
							a.authPaused = true
							a.lastFailedToken = retryToken
							a.logger.Error("Codex polling PAUSED due to repeated auth failures",
								"failure_count", a.authFailCount,
								"action", "Re-authenticate Codex to resume polling")
						}
					} else {
						a.logger.Error("Codex retry failed with non-auth error", "error", err)
					}
					return
				}
				// Retry succeeded, reset auth failure count.
				a.authFailCount = 0
			} else {
				a.logger.Error("No Codex token available after re-read")
				return
			}
		} else {
			a.logger.Error("Failed to fetch Codex usage", "error", err)
			return
		}
	} else {
		// Success, reset auth failure count.
		a.authFailCount = 0
	}

	now := time.Now().UTC()
	snapshot := resp.ToSnapshot(now)

	if _, err := a.store.InsertCodexSnapshot(snapshot); err != nil {
		a.logger.Error("Failed to insert Codex snapshot", "error", err)
		return
	}

	if a.tracker != nil {
		if err := a.tracker.Process(snapshot); err != nil {
			a.logger.Error("Codex tracker processing failed", "error", err)
		}
	}

	if a.notifier != nil {
		for _, q := range snapshot.Quotas {
			a.notifier.Check(notify.QuotaStatus{
				Provider:    "codex",
				QuotaKey:    q.Name,
				Utilization: q.Utilization,
				Limit:       100,
			})
		}
	}

	if a.sm != nil {
		values := make([]float64, 0, len(snapshot.Quotas))
		for _, q := range snapshot.Quotas {
			values = append(values, q.Utilization)
		}
		a.sm.ReportPoll(values)
	}

	for _, q := range snapshot.Quotas {
		a.logger.Info("Codex poll complete", "quota", q.Name, "utilization", q.Utilization, "plan", resp.PlanType)
	}
}
