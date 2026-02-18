package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/config"
	"github.com/onllm-dev/onwatch/internal/notify"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
	"github.com/onllm-dev/onwatch/internal/update"
)

// Login error codes for whitelisting - prevents XSS and information leakage
const (
	LoginErrorInvalid   = "invalid"
	LoginErrorExpired   = "expired"
	LoginErrorRequired  = "required"
	LoginErrorRateLimit = "ratelimit"
)

// loginErrors maps whitelisted error codes to user-friendly messages
var loginErrors = map[string]string{
	LoginErrorInvalid:   "Invalid username or password",
	LoginErrorExpired:   "Session expired, please log in again",
	LoginErrorRequired:  "Authentication required",
	LoginErrorRateLimit: "Too many login attempts. Please try again later.",
}

// Notifier defines the interface for the notification engine.
// The concrete implementation lives in internal/notify.
type Notifier interface {
	Reload() error
	ConfigureSMTP() error
	ConfigurePush() error
	SendTestEmail() error
	SendTestPush() error
	SetEncryptionKey(key string)
	GetVAPIDPublicKey() string
}

// Handler handles HTTP requests for the web dashboard
type Handler struct {
	store            *store.Store
	tracker          *tracker.Tracker
	zaiTracker       *tracker.ZaiTracker
	anthropicTracker *tracker.AnthropicTracker
	copilotTracker   *tracker.CopilotTracker
	codexTracker     *tracker.CodexTracker
	updater          *update.Updater
	notifier         Notifier
	logger           *slog.Logger
	dashboardTmpl    *template.Template
	loginTmpl        *template.Template
	settingsTmpl     *template.Template
	sessions         *SessionStore
	config           *config.Config
	version          string
	smtpTestMu       sync.Mutex
	smtpTestLastSent time.Time
	pushTestMu       sync.Mutex
	pushTestLastSent time.Time
	rateLimiter      *LoginRateLimiter // Per-IP rate limiting for login attempts
}

// NewHandler creates a new Handler instance
func NewHandler(store *store.Store, tracker *tracker.Tracker, logger *slog.Logger, sessions *SessionStore, cfg *config.Config, zaiTracker ...*tracker.ZaiTracker) *Handler {
	if logger == nil {
		logger = slog.Default()
	}

	// Parse dashboard template (layout + dashboard)
	dashboardTmpl, err := template.New("").ParseFS(templatesFS, "templates/layout.html", "templates/dashboard.html")
	if err != nil {
		logger.Error("failed to parse dashboard template", "error", err)
		dashboardTmpl = template.New("empty")
	}

	// Parse login template (layout + login)
	loginTmpl, err := template.New("").ParseFS(templatesFS, "templates/layout.html", "templates/login.html")
	if err != nil {
		logger.Error("failed to parse login template", "error", err)
		loginTmpl = template.New("empty")
	}

	// Parse settings template (layout + settings)
	settingsTmpl, err := template.New("").ParseFS(templatesFS, "templates/layout.html", "templates/settings.html")
	if err != nil {
		logger.Error("failed to parse settings template", "error", err)
		settingsTmpl = template.New("empty")
	}

	h := &Handler{
		store:         store,
		tracker:       tracker,
		logger:        logger,
		dashboardTmpl: dashboardTmpl,
		loginTmpl:     loginTmpl,
		settingsTmpl:  settingsTmpl,
		sessions:      sessions,
		config:        cfg,
	}
	if len(zaiTracker) > 0 && zaiTracker[0] != nil {
		h.zaiTracker = zaiTracker[0]
	}
	return h
}

// SetVersion sets the version string for display in the dashboard.
func (h *Handler) SetVersion(v string) {
	h.version = v
}

// SetAnthropicTracker sets the Anthropic tracker for usage summary enrichment.
func (h *Handler) SetAnthropicTracker(t *tracker.AnthropicTracker) {
	h.anthropicTracker = t
}

// SetCopilotTracker sets the Copilot tracker for usage summary enrichment.
func (h *Handler) SetCopilotTracker(t *tracker.CopilotTracker) {
	h.copilotTracker = t
}

// SetCodexTracker sets the Codex tracker for usage summary enrichment.
func (h *Handler) SetCodexTracker(t *tracker.CodexTracker) {
	h.codexTracker = t
}

// SetUpdater sets the updater for self-update functionality.
func (h *Handler) SetUpdater(u *update.Updater) {
	h.updater = u
}

// SetNotifier sets the notification engine for alert management.
func (h *Handler) SetNotifier(n Notifier) {
	h.notifier = n
}

// GetSessionStore returns the session store for token eviction.
func (h *Handler) GetSessionStore() *SessionStore {
	return h.sessions
}

// SetRateLimiter sets the login rate limiter for brute force protection.
func (h *Handler) SetRateLimiter(l *LoginRateLimiter) {
	h.rateLimiter = l
}

// SettingsPage renders the settings page.
func (h *Handler) SettingsPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Title":   "Settings",
		"Version": h.version,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.settingsTmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		h.logger.Error("failed to render settings template", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// respondJSON sends a JSON response
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// respondError sends an error response
func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}

// isMaxBytesError checks if an error is from http.MaxBytesReader
func isMaxBytesError(err error) bool {
	if err == nil {
		return false
	}
	// MaxBytesReader returns an error with a specific message
	return strings.Contains(err.Error(), "http: request body too large")
}

// sanitizeSMTPError classifies SMTP errors into user-friendly categories
// to prevent information leakage about internal system details
func sanitizeSMTPError(err error) string {
	if err == nil {
		return "SMTP test failed"
	}
	errStr := strings.ToLower(err.Error())

	// Classify errors by type
	switch {
	case strings.Contains(errStr, "authentication") || strings.Contains(errStr, "auth") ||
		strings.Contains(errStr, "username") || strings.Contains(errStr, "password") ||
		strings.Contains(errStr, "535") || strings.Contains(errStr, "530"):
		return "Authentication failed: check username/password"
	case strings.Contains(errStr, "connection") || strings.Contains(errStr, "refused") ||
		strings.Contains(errStr, "timeout") || strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "i/o timeout"):
		return "Connection failed: unable to reach SMTP server"
	case strings.Contains(errStr, "tls") || strings.Contains(errStr, "ssl") ||
		strings.Contains(errStr, "certificate") || strings.Contains(errStr, "x509"):
		return "TLS error: check encryption settings"
	default:
		return "SMTP test failed"
	}
}

// parseTimeRange parses a time range string (1h, 6h, 24h, 1d, 7d, 30d)
func parseTimeRange(rangeStr string) (time.Duration, error) {
	if rangeStr == "" {
		return 6 * time.Hour, nil
	}

	switch rangeStr {
	case "1h":
		return time.Hour, nil
	case "6h":
		return 6 * time.Hour, nil
	case "24h", "1d":
		return 24 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	case "30d":
		return 30 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("invalid range: %s", rangeStr)
	}
}

// maxChartPoints is the target number of data points for chart responses.
// Charts beyond this density add no visual value on typical displays (~1000px wide)
// but increase JSON size and browser rendering time.
const maxChartPoints = 500

// downsampleStep returns the step size to reduce n items to at most max items.
// Returns 1 if no downsampling is needed.
func downsampleStep(n, max int) int {
	if n <= max || max <= 0 {
		return 1
	}
	return (n + max - 1) / max // ceil division
}

// parseInsightsRange parses the insights range param, defaulting to 7d.
func parseInsightsRange(rangeStr string) time.Duration {
	switch rangeStr {
	case "1d":
		return 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	default:
		return 7 * 24 * time.Hour // default "7d"
	}
}

// formatDuration formats a duration as a human-readable string (e.g., "4d 11h" or "3h 16m")
func formatDuration(d time.Duration) string {
	if d < 0 {
		return "Resetting..."
	}

	totalHours := int(d.Hours())
	days := totalHours / 24
	hours := totalHours % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 && hours > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	} else if days > 0 {
		return fmt.Sprintf("%dd %dm", days, minutes)
	} else if hours > 0 && minutes > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	} else if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	} else {
		return fmt.Sprintf("%dm", minutes)
	}
}

// getProviderFromRequest extracts and validates the provider from the request
func (h *Handler) getProviderFromRequest(r *http.Request) (string, error) {
	if h.config == nil {
		return "", fmt.Errorf("configuration not available")
	}

	providers := h.config.AvailableProviders()
	if len(providers) == 0 {
		return "", fmt.Errorf("no providers configured")
	}

	provider := r.URL.Query().Get("provider")
	if provider == "" {
		// Default to first available provider
		return providers[0], nil
	}

	// Normalize provider name
	provider = strings.ToLower(provider)

	// "both" is a virtual provider — allowed when multiple are configured
	if provider == "both" {
		if h.config.HasMultipleProviders() {
			return "both", nil
		}
		return "", fmt.Errorf("'both' requires multiple providers to be configured")
	}

	// Validate provider is available
	if !h.config.HasProvider(provider) {
		return "", fmt.Errorf("provider '%s' is not configured", provider)
	}

	return provider, nil
}

// Providers returns available providers configuration
func (h *Handler) Providers(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		respondError(w, http.StatusInternalServerError, "configuration not available")
		return
	}

	providers := h.config.AvailableProviders()

	// Filter by provider_visibility dashboard flag
	if h.store != nil {
		if visJSON, _ := h.store.GetSetting("provider_visibility"); visJSON != "" {
			var vis map[string]map[string]bool
			if json.Unmarshal([]byte(visJSON), &vis) == nil {
				filtered := make([]string, 0, len(providers))
				for _, p := range providers {
					if pv, ok := vis[p]; ok && !pv["dashboard"] {
						continue
					}
					filtered = append(filtered, p)
				}
				providers = filtered
			}
		}
	}

	if h.config.HasMultipleProviders() {
		providers = append(providers, "both")
	}
	current := ""
	if len(providers) > 0 {
		current = providers[0]
	}

	// Check if a specific provider was requested
	if reqProvider := r.URL.Query().Get("provider"); reqProvider != "" {
		reqProvider = strings.ToLower(reqProvider)
		for _, p := range providers {
			if p == reqProvider {
				current = p
				break
			}
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"providers": providers,
		"current":   current,
	})
}

// Dashboard renders the main dashboard page
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	providers := []string{}
	currentProvider := ""
	if h.config != nil {
		providers = h.config.AvailableProviders()

		// Filter by provider_visibility dashboard flag
		if h.store != nil {
			if visJSON, _ := h.store.GetSetting("provider_visibility"); visJSON != "" {
				var vis map[string]map[string]bool
				if json.Unmarshal([]byte(visJSON), &vis) == nil {
					filtered := make([]string, 0, len(providers))
					for _, p := range providers {
						if pv, ok := vis[p]; ok && !pv["dashboard"] {
							continue
						}
						filtered = append(filtered, p)
					}
					providers = filtered
				}
			}
		}

		// Always add "both" (All tab) when multiple providers configured
		if h.config.HasMultipleProviders() {
			providers = append(providers, "both")
		}
		if len(providers) > 0 {
			currentProvider = providers[0]
		}
		// Allow overriding via query param
		if reqProvider := r.URL.Query().Get("provider"); reqProvider != "" {
			reqProvider = strings.ToLower(reqProvider)
			for _, p := range providers {
				if p == reqProvider {
					currentProvider = reqProvider
					break
				}
			}
		}
	}

	hasAnthropic := h.config != nil && h.config.HasProvider("anthropic")
	hasCopilot := h.config != nil && h.config.HasProvider("copilot")
	hasCodex := h.config != nil && h.config.HasProvider("codex")
	data := map[string]interface{}{
		"Title":           "Dashboard",
		"Providers":       providers,
		"CurrentProvider": currentProvider,
		"Version":         h.version,
		"HasAnthropic":    hasAnthropic,
		"HasCopilot":      hasCopilot,
		"HasCodex":        hasCodex,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.dashboardTmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		h.logger.Error("failed to render dashboard template", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// Current returns current quota status (API endpoint)
func (h *Handler) Current(w http.ResponseWriter, r *http.Request) {
	provider, err := h.getProviderFromRequest(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	switch provider {
	case "both":
		h.currentBoth(w, r)
	case "zai":
		h.currentZai(w, r)
	case "synthetic":
		h.currentSynthetic(w, r)
	case "anthropic":
		h.currentAnthropic(w, r)
	case "copilot":
		h.currentCopilot(w, r)
	case "codex":
		h.currentCodex(w, r)
	default:
		respondError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider: %s", provider))
	}
}

// currentBoth returns combined quota status for all configured providers.
func (h *Handler) currentBoth(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{}
	if h.config.HasProvider("synthetic") {
		response["synthetic"] = h.buildSyntheticCurrent()
	}
	if h.config.HasProvider("zai") {
		response["zai"] = h.buildZaiCurrent()
	}
	if h.config.HasProvider("anthropic") {
		response["anthropic"] = h.buildAnthropicCurrent()
	}
	if h.config.HasProvider("copilot") {
		response["copilot"] = h.buildCopilotCurrent()
	}
	if h.config.HasProvider("codex") {
		response["codex"] = h.buildCodexCurrent()
	}
	respondJSON(w, http.StatusOK, response)
}

// currentSynthetic returns Synthetic quota status
func (h *Handler) currentSynthetic(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildSyntheticCurrent())
}

// buildSyntheticCurrent builds the Synthetic current quota response map.
func (h *Handler) buildSyntheticCurrent() map[string]interface{} {
	now := time.Now().UTC()
	response := map[string]interface{}{
		"capturedAt":   now.Format(time.RFC3339),
		"subscription": buildEmptyQuotaResponse("Subscription", "Main API request quota for your plan"),
		"search":       buildEmptyQuotaResponse("Search (Hourly)", "Search endpoint calls, resets every hour"),
		"toolCalls":    buildEmptyQuotaResponse("Tool Call Discounts", "Discounted tool call requests"),
	}

	if h.store != nil && h.tracker != nil {
		latest, err := h.store.QueryLatest()
		if err != nil {
			h.logger.Error("failed to query latest snapshot", "error", err)
			return response
		}

		if latest != nil {
			response["capturedAt"] = latest.CapturedAt.Format(time.RFC3339)
			response["subscription"] = buildQuotaResponse("Subscription", "Main API request quota for your plan", latest.Sub, h.tracker, "subscription")
			response["search"] = buildQuotaResponse("Search (Hourly)", "Search endpoint calls, resets every hour", latest.Search, h.tracker, "search")
			response["toolCalls"] = buildQuotaResponse("Tool Call Discounts", "Discounted tool call requests", latest.ToolCall, h.tracker, "toolcall")
		}
	}

	return response
}

// currentZai returns Z.ai quota status
func (h *Handler) currentZai(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildZaiCurrent())
}

// buildZaiCurrent builds the Z.ai current quota response map.
func (h *Handler) buildZaiCurrent() map[string]interface{} {
	now := time.Now().UTC()
	response := map[string]interface{}{
		"capturedAt":  now.Format(time.RFC3339),
		"tokensLimit": buildEmptyZaiQuotaResponse("Tokens Limit", "Token consumption budget"),
		"timeLimit":   buildEmptyZaiQuotaResponse("Time Limit", "Tool call time budget"),
		"toolCalls":   buildEmptyZaiQuotaResponse("Tool Calls", "Individual tool call breakdown"),
	}

	if h.store != nil {
		latest, err := h.store.QueryLatestZai()
		if err != nil {
			h.logger.Error("failed to query latest Z.ai snapshot", "error", err)
			return response
		}

		if latest != nil {
			response["capturedAt"] = latest.CapturedAt.Format(time.RFC3339)
			tokensResp := buildZaiTokensQuotaResponse(latest)
			timeResp := buildZaiTimeQuotaResponse(latest)

			// Enrich with tracker data (rate, projection)
			if h.zaiTracker != nil {
				if tokensSummary, err := h.zaiTracker.UsageSummary("tokens"); err == nil && tokensSummary != nil {
					tokensResp["currentRate"] = tokensSummary.CurrentRate
					tokensResp["projectedUsage"] = tokensSummary.ProjectedUsage
				}
				if timeSummary, err := h.zaiTracker.UsageSummary("time"); err == nil && timeSummary != nil {
					timeResp["currentRate"] = timeSummary.CurrentRate
					timeResp["projectedUsage"] = timeSummary.ProjectedUsage
				}
			}

			response["tokensLimit"] = tokensResp
			response["timeLimit"] = timeResp
			response["toolCalls"] = buildZaiToolCallsResponse(latest)
		}
	}

	return response
}

func buildEmptyQuotaResponse(name, description string) map[string]interface{} {
	return map[string]interface{}{
		"name":                  name,
		"description":           description,
		"usage":                 0.0,
		"limit":                 0.0,
		"percent":               0.0,
		"status":                "healthy",
		"renewsAt":              time.Now().UTC().Format(time.RFC3339),
		"timeUntilReset":        "0m",
		"timeUntilResetSeconds": 0,
		"currentRate":           0.0,
		"projectedUsage":        0.0,
		"insight":               "No data available.",
	}
}

func buildEmptyZaiQuotaResponse(name, description string) map[string]interface{} {
	return map[string]interface{}{
		"name":                  name,
		"description":           description,
		"usage":                 0.0,
		"limit":                 0.0,
		"percent":               0.0,
		"status":                "healthy",
		"renewsAt":              time.Now().UTC().Format(time.RFC3339),
		"timeUntilReset":        "0m",
		"timeUntilResetSeconds": 0,
	}
}

func buildZaiTokensQuotaResponse(snapshot *api.ZaiSnapshot) map[string]interface{} {
	// Z.ai API: "usage" = total budget/capacity, "currentValue" = actual usage
	budget := snapshot.TokensUsage              // API's "usage" = total budget
	currentUsage := snapshot.TokensCurrentValue // API's "currentValue" = actual usage
	percent := float64(snapshot.TokensPercentage)

	status := "healthy"
	if percent >= 95 {
		status = "critical"
	} else if percent >= 80 {
		status = "danger"
	} else if percent >= 50 {
		status = "warning"
	}

	result := map[string]interface{}{
		"name":        "Tokens Limit",
		"description": "Token consumption budget",
		"usage":       currentUsage,
		"limit":       budget,
		"percent":     percent,
		"status":      status,
	}

	if snapshot.TokensNextResetTime != nil {
		timeUntilReset := time.Until(*snapshot.TokensNextResetTime)
		result["renewsAt"] = snapshot.TokensNextResetTime.Format(time.RFC3339)
		result["timeUntilReset"] = formatDuration(timeUntilReset)
		result["timeUntilResetSeconds"] = int64(timeUntilReset.Seconds())
	} else {
		result["renewsAt"] = time.Now().UTC().Format(time.RFC3339)
		result["timeUntilReset"] = "N/A"
		result["timeUntilResetSeconds"] = 0
	}

	return result
}

func buildZaiTimeQuotaResponse(snapshot *api.ZaiSnapshot) map[string]interface{} {
	// Z.ai API: "usage" = total budget/capacity, "currentValue" = actual usage
	budget := snapshot.TimeUsage              // API's "usage" = total budget
	currentUsage := snapshot.TimeCurrentValue // API's "currentValue" = actual usage
	percent := float64(snapshot.TimePercentage)

	status := "healthy"
	if percent >= 95 {
		status = "critical"
	} else if percent >= 80 {
		status = "danger"
	} else if percent >= 50 {
		status = "warning"
	}

	return map[string]interface{}{
		"name":                  "Time Limit",
		"description":           "Tool call time budget",
		"usage":                 currentUsage,
		"limit":                 budget,
		"percent":               percent,
		"status":                status,
		"renewsAt":              time.Now().UTC().Format(time.RFC3339),
		"timeUntilReset":        "N/A",
		"timeUntilResetSeconds": 0,
	}
}

func buildZaiToolCallsResponse(snapshot *api.ZaiSnapshot) map[string]interface{} {
	var totalCalls float64
	var details []api.ZaiUsageDetail

	if snapshot.TimeUsageDetails != "" {
		if err := json.Unmarshal([]byte(snapshot.TimeUsageDetails), &details); err == nil {
			for _, d := range details {
				totalCalls += d.Usage
			}
		}
	}

	budget := snapshot.TimeUsage // tool calls draw from the time budget
	percent := 0.0
	if budget > 0 {
		percent = (totalCalls / budget) * 100
	}

	status := "healthy"
	if percent >= 95 {
		status = "critical"
	} else if percent >= 80 {
		status = "danger"
	} else if percent >= 50 {
		status = "warning"
	}

	result := map[string]interface{}{
		"name":                  "Tool Calls",
		"description":           "Individual tool call breakdown",
		"usage":                 totalCalls,
		"limit":                 budget,
		"percent":               percent,
		"status":                status,
		"renewsAt":              time.Now().UTC().Format(time.RFC3339),
		"timeUntilReset":        "N/A",
		"timeUntilResetSeconds": 0,
	}

	if len(details) > 0 {
		result["usageDetails"] = details
	}

	return result
}

// zaiToolCallsPercent computes the tool calls utilization from a Z.ai snapshot's time_usage_details.
func zaiToolCallsPercent(snapshot *api.ZaiSnapshot) float64 {
	if snapshot.TimeUsageDetails == "" || snapshot.TimeUsage <= 0 {
		return 0
	}
	var details []api.ZaiUsageDetail
	if err := json.Unmarshal([]byte(snapshot.TimeUsageDetails), &details); err != nil {
		return 0
	}
	var totalCalls float64
	for _, d := range details {
		totalCalls += d.Usage
	}
	return (totalCalls / snapshot.TimeUsage) * 100
}

func buildQuotaResponse(name, description string, info api.QuotaInfo, tr *tracker.Tracker, quotaType string) map[string]interface{} {
	timeUntilReset := time.Until(info.RenewsAt)

	percent := 0.0
	if info.Limit > 0 {
		percent = (info.Requests / info.Limit) * 100
	}

	status := "healthy"
	if percent >= 95 {
		status = "critical"
	} else if percent >= 80 {
		status = "danger"
	} else if percent >= 50 {
		status = "warning"
	}

	result := map[string]interface{}{
		"name":                  name,
		"description":           description,
		"usage":                 info.Requests,
		"limit":                 info.Limit,
		"percent":               percent,
		"status":                status,
		"renewsAt":              info.RenewsAt.Format(time.RFC3339),
		"timeUntilReset":        formatDuration(timeUntilReset),
		"timeUntilResetSeconds": int64(timeUntilReset.Seconds()),
	}

	// Get summary for rate and projection
	if tr != nil {
		summary, err := tr.UsageSummary(quotaType)
		if err == nil && summary != nil {
			result["currentRate"] = summary.CurrentRate
			result["projectedUsage"] = summary.ProjectedUsage
			result["insight"] = buildInsight(name, info, percent, summary)
		}
	}

	// Ensure defaults if summary failed
	if _, ok := result["currentRate"]; !ok {
		result["currentRate"] = 0.0
		result["projectedUsage"] = 0.0
		result["insight"] = buildInsight(name, info, percent, nil)
	}

	return result
}

func buildInsight(name string, info api.QuotaInfo, percent float64, summary *tracker.Summary) string {
	if info.Limit == 0 {
		return "No data available."
	}

	if percent == 0 {
		return fmt.Sprintf("No %s requests in this cycle.", strings.ToLower(name))
	}

	if summary != nil && summary.ProjectedUsage > 0 {
		return fmt.Sprintf("You've used %.1f%% of your %.0f request quota. At current rate, projected %.0f before reset (%.1f%% of limit).",
			percent, info.Limit, summary.ProjectedUsage, (summary.ProjectedUsage/info.Limit)*100)
	}

	return fmt.Sprintf("You've used %.1f%% of your %.0f request quota.", percent, info.Limit)
}

// History returns usage history (API endpoint)
func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	provider, err := h.getProviderFromRequest(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	switch provider {
	case "both":
		h.historyBoth(w, r)
	case "zai":
		h.historyZai(w, r)
	case "synthetic":
		h.historySynthetic(w, r)
	case "anthropic":
		h.historyAnthropic(w, r)
	case "copilot":
		h.historyCopilot(w, r)
	case "codex":
		h.historyCodex(w, r)
	default:
		respondError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider: %s", provider))
	}
}

// historyBoth returns both providers' history.
func (h *Handler) historyBoth(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{}

	rangeStr := r.URL.Query().Get("range")
	duration, err := parseTimeRange(rangeStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now().UTC()
	start := now.Add(-duration)

	if h.config.HasProvider("synthetic") && h.store != nil {
		snapshots, err := h.store.QueryRange(start, now)
		if err == nil {
			step := downsampleStep(len(snapshots), maxChartPoints)
			last := len(snapshots) - 1
			synData := make([]map[string]interface{}, 0, min(len(snapshots), maxChartPoints))
			for i, s := range snapshots {
				if step > 1 && i != 0 && i != last && i%step != 0 {
					continue
				}
				subPct, searchPct, toolPct := 0.0, 0.0, 0.0
				if s.Sub.Limit > 0 {
					subPct = (s.Sub.Requests / s.Sub.Limit) * 100
				}
				if s.Search.Limit > 0 {
					searchPct = (s.Search.Requests / s.Search.Limit) * 100
				}
				if s.ToolCall.Limit > 0 {
					toolPct = (s.ToolCall.Requests / s.ToolCall.Limit) * 100
				}
				synData = append(synData, map[string]interface{}{
					"capturedAt":          s.CapturedAt.Format(time.RFC3339),
					"subscription":        s.Sub.Requests,
					"subscriptionLimit":   s.Sub.Limit,
					"subscriptionPercent": subPct,
					"search":              s.Search.Requests,
					"searchLimit":         s.Search.Limit,
					"searchPercent":       searchPct,
					"toolCalls":           s.ToolCall.Requests,
					"toolCallsLimit":      s.ToolCall.Limit,
					"toolCallsPercent":    toolPct,
				})
			}
			response["synthetic"] = synData
		}
	}

	if h.config.HasProvider("zai") && h.store != nil {
		snapshots, err := h.store.QueryZaiRange(start, now)
		if err == nil {
			step := downsampleStep(len(snapshots), maxChartPoints)
			last := len(snapshots) - 1
			zaiData := make([]map[string]interface{}, 0, min(len(snapshots), maxChartPoints))
			for i, s := range snapshots {
				if step > 1 && i != 0 && i != last && i%step != 0 {
					continue
				}
				zaiData = append(zaiData, map[string]interface{}{
					"capturedAt":       s.CapturedAt.Format(time.RFC3339),
					"tokensLimit":      s.TokensUsage,
					"tokensUsage":      s.TokensCurrentValue,
					"tokensPercent":    float64(s.TokensPercentage),
					"timeLimit":        s.TimeUsage,
					"timeUsage":        s.TimeCurrentValue,
					"timePercent":      float64(s.TimePercentage),
					"toolCallsPercent": zaiToolCallsPercent(s),
				})
			}
			response["zai"] = zaiData
		}
	}

	if h.config.HasProvider("anthropic") && h.store != nil {
		snapshots, err := h.store.QueryAnthropicRange(start, now)
		if err == nil {
			step := downsampleStep(len(snapshots), maxChartPoints)
			last := len(snapshots) - 1
			anthData := make([]map[string]interface{}, 0, min(len(snapshots), maxChartPoints))
			for i, snap := range snapshots {
				if step > 1 && i != 0 && i != last && i%step != 0 {
					continue
				}
				entry := map[string]interface{}{
					"capturedAt": snap.CapturedAt.Format(time.RFC3339),
				}
				for _, q := range snap.Quotas {
					entry[q.Name] = q.Utilization
				}
				anthData = append(anthData, entry)
			}
			response["anthropic"] = anthData
		}
	}

	if h.config.HasProvider("copilot") && h.store != nil {
		snapshots, err := h.store.QueryCopilotRange(start, now)
		if err == nil {
			step := downsampleStep(len(snapshots), maxChartPoints)
			last := len(snapshots) - 1
			copData := make([]map[string]interface{}, 0, min(len(snapshots), maxChartPoints))
			for i, snap := range snapshots {
				if step > 1 && i != 0 && i != last && i%step != 0 {
					continue
				}
				entry := map[string]interface{}{
					"capturedAt": snap.CapturedAt.Format(time.RFC3339),
				}
				for _, q := range snap.Quotas {
					if q.Entitlement > 0 {
						entry[q.Name] = float64(q.Entitlement-q.Remaining) / float64(q.Entitlement) * 100
					}
				}
				copData = append(copData, entry)
			}
			response["copilot"] = copData
		}
	}

	if h.config.HasProvider("codex") && h.store != nil {
		snapshots, err := h.store.QueryCodexRange(start, now)
		if err == nil {
			step := downsampleStep(len(snapshots), maxChartPoints)
			last := len(snapshots) - 1
			codexData := make([]map[string]interface{}, 0, min(len(snapshots), maxChartPoints))
			for i, snap := range snapshots {
				if step > 1 && i != 0 && i != last && i%step != 0 {
					continue
				}
				entry := map[string]interface{}{
					"capturedAt": snap.CapturedAt.Format(time.RFC3339),
				}
				for _, q := range snap.Quotas {
					entry[q.Name] = q.Utilization
				}
				codexData = append(codexData, entry)
			}
			response["codex"] = codexData
		}
	}

	respondJSON(w, http.StatusOK, response)
}

// historySynthetic returns Synthetic usage history
func (h *Handler) historySynthetic(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	rangeStr := r.URL.Query().Get("range")
	duration, err := parseTimeRange(rangeStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now().UTC()
	start := now.Add(-duration)
	end := now

	snapshots, err := h.store.QueryRange(start, end)
	if err != nil {
		h.logger.Error("failed to query history", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query history")
		return
	}

	step := downsampleStep(len(snapshots), maxChartPoints)
	last := len(snapshots) - 1
	response := make([]map[string]interface{}, 0, min(len(snapshots), maxChartPoints))
	for i, snapshot := range snapshots {
		if step > 1 && i != 0 && i != last && i%step != 0 {
			continue
		}

		subPercent := 0.0
		if snapshot.Sub.Limit > 0 {
			subPercent = (snapshot.Sub.Requests / snapshot.Sub.Limit) * 100
		}

		searchPercent := 0.0
		if snapshot.Search.Limit > 0 {
			searchPercent = (snapshot.Search.Requests / snapshot.Search.Limit) * 100
		}

		toolPercent := 0.0
		if snapshot.ToolCall.Limit > 0 {
			toolPercent = (snapshot.ToolCall.Requests / snapshot.ToolCall.Limit) * 100
		}

		response = append(response, map[string]interface{}{
			"capturedAt":          snapshot.CapturedAt.Format(time.RFC3339),
			"subscription":        snapshot.Sub.Requests,
			"subscriptionLimit":   snapshot.Sub.Limit,
			"subscriptionPercent": subPercent,
			"search":              snapshot.Search.Requests,
			"searchLimit":         snapshot.Search.Limit,
			"searchPercent":       searchPercent,
			"toolCalls":           snapshot.ToolCall.Requests,
			"toolCallsLimit":      snapshot.ToolCall.Limit,
			"toolCallsPercent":    toolPercent,
		})
	}

	respondJSON(w, http.StatusOK, response)
}

// historyZai returns Z.ai usage history
func (h *Handler) historyZai(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	rangeStr := r.URL.Query().Get("range")
	duration, err := parseTimeRange(rangeStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now().UTC()
	start := now.Add(-duration)
	end := now

	snapshots, err := h.store.QueryZaiRange(start, end)
	if err != nil {
		h.logger.Error("failed to query Z.ai history", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query history")
		return
	}

	step := downsampleStep(len(snapshots), maxChartPoints)
	last := len(snapshots) - 1
	response := make([]map[string]interface{}, 0, min(len(snapshots), maxChartPoints))
	for i, snapshot := range snapshots {
		if step > 1 && i != 0 && i != last && i%step != 0 {
			continue
		}
		// Z.ai API: "usage" = budget, "currentValue" = actual usage, "percentage" = server %
		response = append(response, map[string]interface{}{
			"capturedAt":       snapshot.CapturedAt.Format(time.RFC3339),
			"tokensLimit":      snapshot.TokensUsage,        // budget
			"tokensUsage":      snapshot.TokensCurrentValue, // actual usage
			"tokensPercent":    float64(snapshot.TokensPercentage),
			"timeLimit":        snapshot.TimeUsage,        // budget
			"timeUsage":        snapshot.TimeCurrentValue, // actual usage
			"timePercent":      float64(snapshot.TimePercentage),
			"toolCallsPercent": zaiToolCallsPercent(snapshot),
		})
	}

	respondJSON(w, http.StatusOK, response)
}

// Cycles returns reset cycle data (API endpoint)
func (h *Handler) Cycles(w http.ResponseWriter, r *http.Request) {
	provider, err := h.getProviderFromRequest(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	switch provider {
	case "both":
		h.cyclesBoth(w, r)
	case "zai":
		h.cyclesZai(w, r)
	case "synthetic":
		h.cyclesSynthetic(w, r)
	case "anthropic":
		h.cyclesAnthropic(w, r)
	case "copilot":
		h.cyclesCopilot(w, r)
	case "codex":
		h.cyclesCodex(w, r)
	default:
		respondError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider: %s", provider))
	}
}

// cyclesBoth returns combined cycles from all configured providers.
func (h *Handler) cyclesBoth(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{}
	if h.store == nil {
		respondJSON(w, http.StatusOK, response)
		return
	}

	if h.config.HasProvider("synthetic") {
		quotaType := r.URL.Query().Get("type")
		if quotaType == "" {
			quotaType = "subscription"
		}
		var synCycles []map[string]interface{}
		if active, err := h.store.QueryActiveCycle(quotaType); err == nil && active != nil {
			synCycles = append(synCycles, cycleToMap(active))
		}
		if history, err := h.store.QueryCycleHistory(quotaType, 50); err == nil {
			for _, c := range history {
				synCycles = append(synCycles, cycleToMap(c))
			}
		}
		response["synthetic"] = synCycles
	}

	if h.config.HasProvider("zai") {
		zaiType := r.URL.Query().Get("zaiType")
		if zaiType == "" {
			zaiType = "tokens"
		}
		var zaiCycles []map[string]interface{}
		if active, err := h.store.QueryActiveZaiCycle(zaiType); err == nil && active != nil {
			zaiCycles = append(zaiCycles, zaiCycleToMap(active))
		}
		if history, err := h.store.QueryZaiCycleHistory(zaiType, 50); err == nil {
			for _, c := range history {
				zaiCycles = append(zaiCycles, zaiCycleToMap(c))
			}
		}
		response["zai"] = zaiCycles
	}

	if h.config.HasProvider("anthropic") {
		anthType := r.URL.Query().Get("anthropicType")
		if anthType == "" {
			anthType = "five_hour"
		}
		var anthCycles []map[string]interface{}
		if active, err := h.store.QueryActiveAnthropicCycle(anthType); err == nil && active != nil {
			anthCycles = append(anthCycles, anthropicCycleToMap(active))
		}
		if history, err := h.store.QueryAnthropicCycleHistory(anthType, 200); err == nil {
			for _, c := range history {
				anthCycles = append(anthCycles, anthropicCycleToMap(c))
			}
		}
		response["anthropic"] = anthCycles
	}

	if h.config.HasProvider("codex") {
		codexType := r.URL.Query().Get("codexType")
		if codexType == "" {
			codexType = r.URL.Query().Get("type")
		}
		if codexType == "" {
			codexType = "five_hour"
		}
		var codexCycles []map[string]interface{}
		if active, err := h.store.QueryActiveCodexCycle(codexType); err == nil && active != nil {
			codexCycles = append(codexCycles, codexCycleToMap(active))
		}
		if history, err := h.store.QueryCodexCycleHistory(codexType, 200); err == nil {
			for _, c := range history {
				codexCycles = append(codexCycles, codexCycleToMap(c))
			}
		}
		response["codex"] = codexCycles
	}

	respondJSON(w, http.StatusOK, response)
}

// cyclesSynthetic returns Synthetic reset cycles
func (h *Handler) cyclesSynthetic(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	quotaType := r.URL.Query().Get("type")
	if quotaType == "" {
		quotaType = "subscription"
	}

	validTypes := map[string]bool{
		"subscription": true,
		"search":       true,
		"toolcall":     true,
	}

	if !validTypes[quotaType] {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid quota type: %s", quotaType))
		return
	}

	// Get both active and completed cycles
	response := []map[string]interface{}{}

	active, err := h.store.QueryActiveCycle(quotaType)
	if err != nil {
		h.logger.Error("failed to query active cycle", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycles")
		return
	}

	if active != nil {
		response = append(response, cycleToMap(active))
	}

	history, err := h.store.QueryCycleHistory(quotaType, 200)
	if err != nil {
		h.logger.Error("failed to query cycle history", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycles")
		return
	}

	for _, cycle := range history {
		response = append(response, cycleToMap(cycle))
	}

	respondJSON(w, http.StatusOK, response)
}

// cyclesZai returns Z.ai reset cycles
func (h *Handler) cyclesZai(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	quotaType := r.URL.Query().Get("type")
	if quotaType == "" {
		quotaType = "tokens"
	}

	validTypes := map[string]bool{
		"tokens": true,
		"time":   true,
	}

	if !validTypes[quotaType] {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid quota type: %s", quotaType))
		return
	}

	// Get both active and completed cycles
	response := []map[string]interface{}{}

	active, err := h.store.QueryActiveZaiCycle(quotaType)
	if err != nil {
		h.logger.Error("failed to query active Z.ai cycle", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycles")
		return
	}

	if active != nil {
		response = append(response, zaiCycleToMap(active))
	}

	history, err := h.store.QueryZaiCycleHistory(quotaType, 200)
	if err != nil {
		h.logger.Error("failed to query Z.ai cycle history", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycles")
		return
	}

	for _, cycle := range history {
		response = append(response, zaiCycleToMap(cycle))
	}

	respondJSON(w, http.StatusOK, response)
}

func cycleToMap(cycle *store.ResetCycle) map[string]interface{} {
	result := map[string]interface{}{
		"id":           cycle.ID,
		"quotaType":    cycle.QuotaType,
		"cycleStart":   cycle.CycleStart.Format(time.RFC3339),
		"cycleEnd":     nil,
		"renewsAt":     cycle.RenewsAt.Format(time.RFC3339),
		"peakRequests": cycle.PeakRequests,
		"totalDelta":   cycle.TotalDelta,
	}

	if cycle.CycleEnd != nil {
		result["cycleEnd"] = cycle.CycleEnd.Format(time.RFC3339)
	}

	return result
}

func zaiCycleToMap(cycle *store.ZaiResetCycle) map[string]interface{} {
	result := map[string]interface{}{
		"id":           cycle.ID,
		"quotaType":    cycle.QuotaType,
		"cycleStart":   cycle.CycleStart.Format(time.RFC3339),
		"cycleEnd":     nil,
		"peakRequests": cycle.PeakValue, // normalized to match Synthetic field name for frontend
		"totalDelta":   cycle.TotalDelta,
	}

	if cycle.CycleEnd != nil {
		result["cycleEnd"] = cycle.CycleEnd.Format(time.RFC3339)
	}

	if cycle.NextReset != nil {
		result["renewsAt"] = cycle.NextReset.Format(time.RFC3339)
	}

	return result
}

// Summary returns usage summary (API endpoint)
func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
	provider, err := h.getProviderFromRequest(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	switch provider {
	case "both":
		h.summaryBoth(w, r)
	case "zai":
		h.summaryZai(w, r)
	case "synthetic":
		h.summarySynthetic(w, r)
	case "anthropic":
		h.summaryAnthropic(w, r)
	case "copilot":
		h.summaryCopilot(w, r)
	case "codex":
		h.summaryCodex(w, r)
	default:
		respondError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider: %s", provider))
	}
}

// summaryBoth returns combined summaries from all configured providers.
func (h *Handler) summaryBoth(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{}
	if h.config.HasProvider("synthetic") {
		synResp := map[string]interface{}{
			"subscription": buildEmptySummaryResponse("subscription"),
			"search":       buildEmptySummaryResponse("search"),
			"toolCalls":    buildEmptySummaryResponse("toolcall"),
		}
		if h.store != nil && h.tracker != nil {
			for _, qt := range []string{"subscription", "search", "toolcall"} {
				if s, err := h.tracker.UsageSummary(qt); err == nil && s != nil {
					key := qt
					if qt == "toolcall" {
						key = "toolCalls"
					}
					synResp[key] = buildSummaryResponse(s)
				}
			}
		}
		response["synthetic"] = synResp
	}
	if h.config.HasProvider("zai") {
		response["zai"] = h.buildZaiSummaryMap()
	}
	if h.config.HasProvider("anthropic") {
		response["anthropic"] = h.buildAnthropicSummaryMap()
	}
	if h.config.HasProvider("copilot") {
		response["copilot"] = h.buildCopilotSummaryMap()
	}
	if h.config.HasProvider("codex") {
		response["codex"] = h.buildCodexSummaryMap()
	}
	respondJSON(w, http.StatusOK, response)
}

// summarySynthetic returns Synthetic usage summary
func (h *Handler) summarySynthetic(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"subscription": buildEmptySummaryResponse("subscription"),
		"search":       buildEmptySummaryResponse("search"),
		"toolCalls":    buildEmptySummaryResponse("toolcall"),
	}

	if h.store != nil && h.tracker != nil {
		for _, quotaType := range []string{"subscription", "search", "toolcall"} {
			summary, err := h.tracker.UsageSummary(quotaType)
			if err == nil && summary != nil {
				key := quotaType
				if quotaType == "toolcall" {
					key = "toolCalls"
				}
				response[key] = buildSummaryResponse(summary)
			}
		}
	}

	respondJSON(w, http.StatusOK, response)
}

// summaryZai returns Z.ai usage summary
func (h *Handler) summaryZai(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildZaiSummaryMap())
}

// buildZaiSummaryMap builds the Z.ai summary response.
func (h *Handler) buildZaiSummaryMap() map[string]interface{} {
	response := map[string]interface{}{
		"tokensLimit": buildEmptyZaiSummaryResponse("tokens"),
		"timeLimit":   buildEmptyZaiSummaryResponse("time"),
	}

	// Try tracker-based summary first (has cycle data)
	if h.zaiTracker != nil {
		if tokensSummary, err := h.zaiTracker.UsageSummary("tokens"); err == nil && tokensSummary != nil {
			response["tokensLimit"] = buildZaiTrackerSummaryResponse(tokensSummary)
		}
		if timeSummary, err := h.zaiTracker.UsageSummary("time"); err == nil && timeSummary != nil {
			response["timeLimit"] = buildZaiTrackerSummaryResponse(timeSummary)
		}
		return response
	}

	// Fallback to snapshot-only summary
	if h.store != nil {
		latest, err := h.store.QueryLatestZai()
		if err != nil {
			h.logger.Error("failed to query latest Z.ai snapshot", "error", err)
			return response
		}
		if latest != nil {
			response["tokensLimit"] = buildZaiTokensSummary(latest)
			response["timeLimit"] = buildZaiTimeSummary(latest)
		}
	}

	return response
}

func buildEmptySummaryResponse(quotaType string) map[string]interface{} {
	return map[string]interface{}{
		"quotaType":       quotaType,
		"currentUsage":    0.0,
		"currentLimit":    0.0,
		"usagePercent":    0.0,
		"renewsAt":        time.Now().UTC().Format(time.RFC3339),
		"timeUntilReset":  "0m",
		"currentRate":     0.0,
		"projectedUsage":  0.0,
		"completedCycles": 0,
		"avgPerCycle":     0.0,
		"peakCycle":       0.0,
		"totalTracked":    0.0,
		"trackingSince":   nil,
	}
}

func buildSummaryResponse(summary *tracker.Summary) map[string]interface{} {
	result := map[string]interface{}{
		"quotaType":       summary.QuotaType,
		"currentUsage":    summary.CurrentUsage,
		"currentLimit":    summary.CurrentLimit,
		"usagePercent":    summary.UsagePercent,
		"renewsAt":        summary.RenewsAt.Format(time.RFC3339),
		"timeUntilReset":  formatDuration(summary.TimeUntilReset),
		"currentRate":     summary.CurrentRate,
		"projectedUsage":  summary.ProjectedUsage,
		"completedCycles": summary.CompletedCycles,
		"avgPerCycle":     summary.AvgPerCycle,
		"peakCycle":       summary.PeakCycle,
		"totalTracked":    summary.TotalTracked,
		"trackingSince":   nil,
	}

	if !summary.TrackingSince.IsZero() {
		result["trackingSince"] = summary.TrackingSince.Format(time.RFC3339)
	}

	return result
}

func buildEmptyZaiSummaryResponse(quotaType string) map[string]interface{} {
	return map[string]interface{}{
		"quotaType":       quotaType,
		"currentUsage":    0.0,
		"currentLimit":    0.0,
		"usagePercent":    0.0,
		"renewsAt":        time.Now().UTC().Format(time.RFC3339),
		"timeUntilReset":  "0m",
		"completedCycles": 0,
		"avgPerCycle":     0.0,
		"peakCycle":       0.0,
		"totalTracked":    0.0,
		"trackingSince":   nil,
	}
}

func buildZaiTokensSummary(snapshot *api.ZaiSnapshot) map[string]interface{} {
	// Z.ai API: "usage" = total budget, "currentValue" = actual usage
	budget := snapshot.TokensUsage
	currentUsage := snapshot.TokensCurrentValue

	result := map[string]interface{}{
		"quotaType":       "tokens",
		"currentUsage":    currentUsage,
		"currentLimit":    budget,
		"usagePercent":    float64(snapshot.TokensPercentage),
		"currentRate":     0.0,
		"projectedUsage":  0.0,
		"completedCycles": 0,
		"avgPerCycle":     0.0,
		"peakCycle":       0.0,
		"totalTracked":    0.0,
		"trackingSince":   nil,
	}

	if snapshot.TokensNextResetTime != nil {
		timeUntilReset := time.Until(*snapshot.TokensNextResetTime)
		result["renewsAt"] = snapshot.TokensNextResetTime.Format(time.RFC3339)
		result["timeUntilReset"] = formatDuration(timeUntilReset)
	} else {
		result["renewsAt"] = time.Now().UTC().Format(time.RFC3339)
		result["timeUntilReset"] = "N/A"
	}

	return result
}

func buildZaiTimeSummary(snapshot *api.ZaiSnapshot) map[string]interface{} {
	// Z.ai API: "usage" = total budget, "currentValue" = actual usage
	budget := snapshot.TimeUsage
	currentUsage := snapshot.TimeCurrentValue

	return map[string]interface{}{
		"quotaType":       "time",
		"currentUsage":    currentUsage,
		"currentLimit":    budget,
		"usagePercent":    float64(snapshot.TimePercentage),
		"renewsAt":        time.Now().UTC().Format(time.RFC3339),
		"timeUntilReset":  "N/A",
		"currentRate":     0.0,
		"projectedUsage":  0.0,
		"completedCycles": 0,
		"avgPerCycle":     0.0,
		"peakCycle":       0.0,
		"totalTracked":    0.0,
		"trackingSince":   nil,
	}
}

// buildZaiTrackerSummaryResponse builds a summary response from ZaiTracker data.
func buildZaiTrackerSummaryResponse(summary *tracker.ZaiSummary) map[string]interface{} {
	result := map[string]interface{}{
		"quotaType":       summary.QuotaType,
		"currentUsage":    summary.CurrentUsage,
		"currentLimit":    summary.CurrentLimit,
		"usagePercent":    summary.UsagePercent,
		"currentRate":     summary.CurrentRate,
		"projectedUsage":  summary.ProjectedUsage,
		"completedCycles": summary.CompletedCycles,
		"avgPerCycle":     summary.AvgPerCycle,
		"peakCycle":       summary.PeakCycle,
		"totalTracked":    summary.TotalTracked,
		"trackingSince":   nil,
	}

	if summary.RenewsAt != nil {
		result["renewsAt"] = summary.RenewsAt.Format(time.RFC3339)
		result["timeUntilReset"] = formatDuration(summary.TimeUntilReset)
	} else {
		result["renewsAt"] = time.Now().UTC().Format(time.RFC3339)
		result["timeUntilReset"] = "N/A"
	}

	if !summary.TrackingSince.IsZero() {
		result["trackingSince"] = summary.TrackingSince.Format(time.RFC3339)
	}

	return result
}

// Sessions returns session data (API endpoint)
func (h *Handler) Sessions(w http.ResponseWriter, r *http.Request) {
	provider, err := h.getProviderFromRequest(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	if provider == "both" {
		h.sessionsBoth(w, r)
		return
	}

	sessions, err := h.store.QuerySessionHistory(provider)
	if err != nil {
		h.logger.Error("failed to query sessions", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query sessions")
		return
	}

	response := []map[string]interface{}{}
	for _, session := range sessions {
		sessionMap := map[string]interface{}{
			"id":                  session.ID,
			"startedAt":           session.StartedAt.Format(time.RFC3339),
			"endedAt":             nil,
			"pollInterval":        session.PollInterval,
			"maxSubRequests":      session.MaxSubRequests,
			"maxSearchRequests":   session.MaxSearchRequests,
			"maxToolRequests":     session.MaxToolRequests,
			"startSubRequests":    session.StartSubRequests,
			"startSearchRequests": session.StartSearchRequests,
			"startToolRequests":   session.StartToolRequests,
			"snapshotCount":       session.SnapshotCount,
		}

		if session.EndedAt != nil {
			sessionMap["endedAt"] = session.EndedAt.Format(time.RFC3339)
		}

		response = append(response, sessionMap)
	}

	respondJSON(w, http.StatusOK, response)
}

// sessionsBoth returns sessions from both providers.
func (h *Handler) sessionsBoth(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{}

	buildSessionList := func(provider string) []map[string]interface{} {
		sessions, err := h.store.QuerySessionHistory(provider)
		if err != nil {
			return nil
		}
		var list []map[string]interface{}
		for _, s := range sessions {
			m := map[string]interface{}{
				"id":                  s.ID,
				"startedAt":           s.StartedAt.Format(time.RFC3339),
				"endedAt":             nil,
				"pollInterval":        s.PollInterval,
				"maxSubRequests":      s.MaxSubRequests,
				"maxSearchRequests":   s.MaxSearchRequests,
				"maxToolRequests":     s.MaxToolRequests,
				"startSubRequests":    s.StartSubRequests,
				"startSearchRequests": s.StartSearchRequests,
				"startToolRequests":   s.StartToolRequests,
				"snapshotCount":       s.SnapshotCount,
			}
			if s.EndedAt != nil {
				m["endedAt"] = s.EndedAt.Format(time.RFC3339)
			}
			list = append(list, m)
		}
		return list
	}

	if h.config.HasProvider("synthetic") {
		response["synthetic"] = buildSessionList("synthetic")
	}
	if h.config.HasProvider("zai") {
		response["zai"] = buildSessionList("zai")
	}
	if h.config.HasProvider("anthropic") {
		response["anthropic"] = buildSessionList("anthropic")
	}
	if h.config.HasProvider("copilot") {
		response["copilot"] = buildSessionList("copilot")
	}
	if h.config.HasProvider("codex") {
		response["codex"] = buildSessionList("codex")
	}

	respondJSON(w, http.StatusOK, response)
}

// ── Deep Insights ──

type insightStat struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Sublabel string `json:"sublabel,omitempty"`
}

type insightItem struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Metric   string `json:"metric,omitempty"`
	Sublabel string `json:"sublabel,omitempty"`
	Desc     string `json:"description"`
}

// insightCorrelations maps analogous insight keys across providers.
// Hiding one key in a group hides all keys in that group.
var insightCorrelations = [][]string{
	{"cycle_utilization", "token_budget"},
	{"tool_share", "tool_breakdown"},
	{"trend", "trend_24h"},
	{"weekly_pace", "usage_7d"},
	// "coverage" uses the same key for both providers — auto-correlated
}

// getHiddenInsightKeys loads hidden insight keys from DB and expands correlations.
func (h *Handler) getHiddenInsightKeys() map[string]bool {
	hidden := map[string]bool{}
	if h.store == nil {
		return hidden
	}
	val, err := h.store.GetSetting("hidden_insights")
	if err != nil || val == "" {
		return hidden
	}
	var keys []string
	if err := json.Unmarshal([]byte(val), &keys); err != nil {
		return hidden
	}
	for _, k := range keys {
		hidden[k] = true
	}
	// Expand correlated keys
	for _, group := range insightCorrelations {
		groupHidden := false
		for _, k := range group {
			if hidden[k] {
				groupHidden = true
				break
			}
		}
		if groupHidden {
			for _, k := range group {
				hidden[k] = true
			}
		}
	}
	return hidden
}

type insightsResponse struct {
	Stats    []insightStat `json:"stats"`
	Insights []insightItem `json:"insights"`
}

// Insights returns computed deep analytics (API endpoint)
func (h *Handler) Insights(w http.ResponseWriter, r *http.Request) {
	provider, err := h.getProviderFromRequest(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	rangeDur := parseInsightsRange(r.URL.Query().Get("range"))

	switch provider {
	case "both":
		h.insightsBoth(w, r, rangeDur)
	case "zai":
		h.insightsZai(w, r, rangeDur)
	case "synthetic":
		h.insightsSynthetic(w, r, rangeDur)
	case "anthropic":
		h.insightsAnthropic(w, r, rangeDur)
	case "copilot":
		h.insightsCopilot(w, r, rangeDur)
	case "codex":
		h.insightsCodex(w, r, rangeDur)
	default:
	}
}

// insightsBoth returns combined insights from all configured providers.
func (h *Handler) insightsBoth(w http.ResponseWriter, r *http.Request, rangeDur time.Duration) {
	hidden := h.getHiddenInsightKeys()
	response := map[string]interface{}{}

	if h.config.HasProvider("synthetic") {
		response["synthetic"] = h.buildSyntheticInsights(hidden, rangeDur)
	}
	if h.config.HasProvider("zai") {
		response["zai"] = h.buildZaiInsights(hidden)
	}
	if h.config.HasProvider("anthropic") {
		response["anthropic"] = h.buildAnthropicInsights(hidden, rangeDur)
	}
	if h.config.HasProvider("copilot") {
		response["copilot"] = h.buildCopilotInsights(hidden, rangeDur)
	}
	if h.config.HasProvider("codex") {
		response["codex"] = h.buildCodexInsights(hidden, rangeDur)
	}

	respondJSON(w, http.StatusOK, response)
}

// insightsSynthetic returns Synthetic deep analytics
func (h *Handler) insightsSynthetic(w http.ResponseWriter, r *http.Request, rangeDur time.Duration) {
	hidden := h.getHiddenInsightKeys()
	respondJSON(w, http.StatusOK, h.buildSyntheticInsights(hidden, rangeDur))
}

// buildSyntheticInsights builds the Synthetic insights response.
// rangeDur controls the time window for the 4 stat cards.
func (h *Handler) buildSyntheticInsights(hidden map[string]bool, rangeDur time.Duration) insightsResponse {
	resp := insightsResponse{Stats: []insightStat{}, Insights: []insightItem{}}

	if h.store == nil {
		return resp
	}

	now := time.Now().UTC()
	rangeStart := now.Add(-rangeDur)
	d30 := now.Add(-30 * 24 * time.Hour)
	d7 := now.Add(-7 * 24 * time.Hour)

	// Fetch cycle data for all quota types (last 30 days for insights, rangeDur for stats)
	subCycles, _ := h.store.QueryCyclesSince("subscription", d30)
	searchCycles, _ := h.store.QueryCyclesSince("search", d30)
	toolCycles, _ := h.store.QueryCyclesSince("toolcall", d30)

	sessions, _ := h.store.QuerySessionHistory()
	latest, _ := h.store.QueryLatest()

	var subLimit float64
	if latest != nil {
		subLimit = latest.Sub.Limit
	}

	// Compute range-specific totals for stat cards
	rangeDays := int(rangeDur.Hours() / 24)
	if rangeDays == 0 {
		rangeDays = 1
	}
	rangeLabel := fmt.Sprintf("%dd", rangeDays)

	subRange := cycleSumConsumptionSince(subCycles, rangeStart)
	searchRange := cycleSumConsumptionSince(searchCycles, rangeStart)
	toolRange := cycleSumConsumptionSince(toolCycles, rangeStart)
	totalRange := subRange + searchRange + toolRange

	// Count sessions in range
	var sessionsInRange int
	for _, s := range sessions {
		if !s.StartedAt.Before(rangeStart) {
			sessionsInRange++
		}
	}

	// 30-day totals for insights (always based on 30d regardless of range)
	sub30 := cycleSumConsumption(subCycles)
	sub7 := cycleSumConsumptionSince(subCycles, d7)

	subAvg := billingPeriodAvg(subCycles)
	subPeak := billingPeriodPeak(subCycles)

	// ═══ Stats Cards (exactly 4, range-aware) ═══
	resp.Stats = append(resp.Stats, insightStat{Value: compactNum(subRange), Label: fmt.Sprintf("Requests (%s)", rangeLabel)})
	resp.Stats = append(resp.Stats, insightStat{Value: compactNum(totalRange), Label: fmt.Sprintf("Total API Calls (%s)", rangeLabel)})
	resp.Stats = append(resp.Stats, insightStat{Value: compactNum(toolRange), Label: fmt.Sprintf("Tool Calls (%s)", rangeLabel)})
	resp.Stats = append(resp.Stats, insightStat{Value: fmt.Sprintf("%d", sessionsInRange), Label: "Sessions"})

	// ═══ Deep Insights (analytical cards only — no session avg, no live quota duplicates) ═══

	// 1. Avg Cycle Utilization %
	if !hidden["cycle_utilization"] && subAvg > 0 && subLimit > 0 {
		util := (subAvg / subLimit) * 100
		var desc, sev string
		switch {
		case util < 25:
			desc = fmt.Sprintf("You average ~%.0f%% of your %.0f quota per cycle. Significantly under-utilizing — a lower tier could save costs.", util, subLimit)
			sev = "warning"
		case util < 50:
			desc = fmt.Sprintf("You average ~%.0f%% of your %.0f quota per cycle. Comfortable headroom — consider downgrading if optimizing costs.", util, subLimit)
			sev = "info"
		case util < 80:
			desc = fmt.Sprintf("You average ~%.0f%% of your %.0f quota per cycle. Plan fits your usage well.", util, subLimit)
			sev = "positive"
		case util < 95:
			desc = fmt.Sprintf("You average ~%.0f%% of your %.0f quota per cycle. Approaching your limit frequently — monitor closely.", util, subLimit)
			sev = "warning"
		default:
			desc = fmt.Sprintf("You average ~%.0f%% of your %.0f quota per cycle. Consistently near limit — consider upgrading.", util, subLimit)
			sev = "negative"
		}
		resp.Insights = append(resp.Insights, insightItem{
			Key:  "cycle_utilization",
			Type: "recommendation", Severity: sev,
			Title:    "Avg Cycle Utilization",
			Metric:   fmt.Sprintf("%.0f%%", util),
			Sublabel: fmt.Sprintf("of %.0f limit/cycle", subLimit),
			Desc:     desc,
		})
	}

	subBillingCount := billingPeriodCount(subCycles)

	// 2. Weekly Pace
	if !hidden["weekly_pace"] && sub7 > 0 {
		proj := sub7 * (30.0 / 7.0)
		weeklyPct := float64(0)
		if sub30 > 0 {
			weeklyPct = (sub7 / sub30) * 100
		}
		sev := "info"
		if subLimit > 0 {
			cyclesPerMonth := float64(len(subCycles))
			if cyclesPerMonth > 0 && proj > subLimit*cyclesPerMonth*0.8 {
				sev = "warning"
			}
		}
		desc := fmt.Sprintf("%.0f requests this week", sub7)
		if sub30 > 0 {
			desc += fmt.Sprintf(" (%.0f%% of 30-day total). Monthly projection: ~%s.", weeklyPct, compactNum(proj))
		}
		resp.Insights = append(resp.Insights, insightItem{
			Key:  "weekly_pace",
			Type: "trend", Severity: sev,
			Title:    "Weekly Pace",
			Metric:   compactNum(sub7),
			Sublabel: "last 7 days",
			Desc:     desc,
		})
	}

	// 3. Peak vs Average Variance
	if !hidden["variance"] && subPeak > 0 && subAvg > 0 && subBillingCount > 1 {
		diff := ((subPeak - subAvg) / subAvg) * 100
		var item insightItem
		peakPct := float64(0)
		if subLimit > 0 {
			peakPct = (subPeak / subLimit) * 100
		}
		switch {
		case diff > 50:
			item = insightItem{Key: "variance", Type: "factual", Severity: "warning",
				Title:    "High Variance",
				Metric:   fmt.Sprintf("+%.0f%%", diff),
				Sublabel: "peak above avg",
				Desc:     fmt.Sprintf("Peak cycle hit %.0f%% of limit (%.0f requests) — %.0f%% above your average of %.0f. Usage varies significantly.", peakPct, subPeak, diff, subAvg),
			}
		case diff > 10:
			item = insightItem{Key: "variance", Type: "factual", Severity: "info",
				Title:    "Usage Spread",
				Metric:   fmt.Sprintf("+%.0f%%", diff),
				Sublabel: "peak above avg",
				Desc:     fmt.Sprintf("Peak: %.0f%% of limit (%.0f req), average: %.0f. Moderately consistent.", peakPct, subPeak, subAvg),
			}
		default:
			item = insightItem{Key: "variance", Type: "factual", Severity: "positive",
				Title:    "Consistent",
				Metric:   fmt.Sprintf("~%.0f%%", (subAvg/subLimit)*100),
				Sublabel: "steady usage",
				Desc:     fmt.Sprintf("Peak (%.0f) is close to average (%.0f). Predictable consumption.", subPeak, subAvg),
			}
		}
		resp.Insights = append(resp.Insights, item)
	}

	// 4. Consumption Trend (needs at least 4 billing periods to be meaningful)
	if !hidden["trend"] && subBillingCount >= 4 {
		mid := len(subCycles) / 2
		recentAvg := billingPeriodAvg(subCycles[:mid])
		olderAvg := billingPeriodAvg(subCycles[mid:])
		if olderAvg > 0 {
			change := ((recentAvg - olderAvg) / olderAvg) * 100
			var desc, sev, metric string
			switch {
			case change > 15:
				metric = fmt.Sprintf("+%.0f%%", change)
				desc = fmt.Sprintf("Recent cycles avg %.0f vs earlier %.0f — usage is increasing.", recentAvg, olderAvg)
				sev = "warning"
			case change < -15:
				metric = fmt.Sprintf("%.0f%%", change)
				desc = fmt.Sprintf("Recent cycles avg %.0f vs earlier %.0f — usage is decreasing.", recentAvg, olderAvg)
				sev = "positive"
			default:
				metric = "Stable"
				desc = fmt.Sprintf("Recent avg %.0f vs earlier %.0f — steady usage pattern.", recentAvg, olderAvg)
				sev = "positive"
			}
			resp.Insights = append(resp.Insights, insightItem{
				Key:  "trend",
				Type: "trend", Severity: sev,
				Title:    "Trend",
				Metric:   metric,
				Sublabel: "recent vs earlier",
				Desc:     desc,
			})
		}
	}

	// If no insights at all, add a getting-started message
	if len(resp.Insights) == 0 {
		resp.Insights = append(resp.Insights, insightItem{
			Type: "info", Severity: "info",
			Title: "Getting Started",
			Desc:  "Keep onWatch running to build up usage data. Deep insights will appear after a few cycles.",
		})
	}

	return resp
}

// insightsZai returns Z.ai deep analytics with historical data
func (h *Handler) insightsZai(w http.ResponseWriter, r *http.Request, rangeDur time.Duration) {
	hidden := h.getHiddenInsightKeys()
	respondJSON(w, http.StatusOK, h.buildZaiInsights(hidden))
}

// buildZaiInsights builds the Z.ai insights response.
func (h *Handler) buildZaiInsights(hidden map[string]bool) insightsResponse {
	resp := insightsResponse{Stats: []insightStat{}, Insights: []insightItem{}}

	if h.store == nil {
		return resp
	}

	latest, err := h.store.QueryLatestZai()
	if err != nil {
		h.logger.Error("failed to query Z.ai data for insights", "error", err)
		return resp
	}

	if latest == nil {
		resp.Insights = append(resp.Insights, insightItem{
			Type: "info", Severity: "info",
			Title: "Getting Started",
			Desc:  "Keep onWatch running to collect Z.ai usage data. Insights appear after a few snapshots.",
		})
		return resp
	}

	now := time.Now().UTC()

	// Z.ai API: "usage" = budget, "currentValue" = actual consumption
	tokensBudget := latest.TokensUsage
	tokensUsed := latest.TokensCurrentValue
	tokensRemaining := latest.TokensRemaining

	timeBudget := latest.TimeUsage
	timeUsed := latest.TimeCurrentValue
	timePercent := float64(latest.TimePercentage)
	timeRemaining := latest.TimeRemaining

	// Compute total tool calls from usageDetails
	var totalToolCalls float64
	if latest.TimeUsageDetails != "" {
		var details []api.ZaiUsageDetail
		if err := json.Unmarshal([]byte(latest.TimeUsageDetails), &details); err == nil {
			for _, d := range details {
				totalToolCalls += d.Usage
			}
		}
	}

	// Historical snapshots for rate/trend computation
	d24h := now.Add(-24 * time.Hour)
	d7d := now.Add(-7 * 24 * time.Hour)
	snapshots24h, _ := h.store.QueryZaiRange(d24h, now)
	snapshots7d, _ := h.store.QueryZaiRange(d7d, now)

	// Plan capacity: "usage" field IS the daily budget (resets daily)
	dailyTokenBudget := tokensBudget // e.g., 200,000,000 tokens/day
	monthlyTokenCapacity := dailyTokenBudget * 30
	dailyTimeBudget := timeBudget // e.g., 1000 time units/day
	monthlyTimeCapacity := dailyTimeBudget * 30

	// Avg tokens per tool call
	var avgTokensPerCall float64
	if totalToolCalls > 0 {
		avgTokensPerCall = tokensUsed / totalToolCalls
	}

	// ═══ Stats Cards (quick KPI numbers — no duplicates with insights below) ═══
	resp.Stats = append(resp.Stats, insightStat{
		Value: fmt.Sprintf("%d%%", latest.TokensPercentage),
		Label: "Tokens Used",
	})
	resp.Stats = append(resp.Stats, insightStat{
		Value: compactNum(tokensRemaining),
		Label: "Tokens Left",
	})
	resp.Stats = append(resp.Stats, insightStat{
		Value: fmt.Sprintf("%.0f", totalToolCalls),
		Label: "Tool Calls",
	})
	resp.Stats = append(resp.Stats, insightStat{
		Value: fmt.Sprintf("%.0f / %.0f", timeUsed, timeBudget),
		Label: "Time Budget",
	})

	// ═══ Deep Insights ═══

	// 1. Token Consumption Rate (computed from historical snapshots)
	if !hidden["token_rate"] && len(snapshots24h) >= 2 {
		oldest := snapshots24h[0]
		newest := snapshots24h[len(snapshots24h)-1]
		elapsed := newest.CapturedAt.Sub(oldest.CapturedAt)
		tokenDelta := newest.TokensCurrentValue - oldest.TokensCurrentValue

		if elapsed.Hours() > 0 && tokenDelta > 0 {
			ratePerHour := tokenDelta / elapsed.Hours()
			resp.Insights = append(resp.Insights, insightItem{
				Key:  "token_rate",
				Type: "trend", Severity: "info",
				Title:    "Token Rate",
				Metric:   fmt.Sprintf("%s/hr", compactNum(ratePerHour)),
				Sublabel: fmt.Sprintf("last %.0fh", elapsed.Hours()),
				Desc: fmt.Sprintf("Consuming ~%s tokens/hour over the last %.1f hours (%s total in this period).",
					compactNum(ratePerHour), elapsed.Hours(), compactNum(tokenDelta)),
			})

			// 3. Projected Token Usage (only if we have a reset time)
			if !hidden["projected_usage"] && latest.TokensNextResetTime != nil {
				hoursLeft := time.Until(*latest.TokensNextResetTime).Hours()
				if hoursLeft > 0 {
					projected := tokensUsed + (ratePerHour * hoursLeft)
					projectedPct := (projected / tokensBudget) * 100

					projSev := severityFromPercent(projectedPct)
					projDesc := fmt.Sprintf("At current rate (~%s/hr), projected %s tokens (%s%%) by reset.",
						compactNum(ratePerHour), compactNum(projected), compactNum(projectedPct))
					if projectedPct >= 100 {
						projDesc += " Likely to exhaust budget before reset."
					} else if projectedPct >= 80 {
						projDesc += " Approaching limit — monitor closely."
					} else {
						projDesc += " Comfortable headroom."
					}
					resp.Insights = append(resp.Insights, insightItem{
						Key:  "projected_usage",
						Type: "recommendation", Severity: projSev,
						Title:    "Projected Usage",
						Metric:   fmt.Sprintf("%.0f%%", projectedPct),
						Sublabel: fmt.Sprintf("~%s by reset", compactNum(projected)),
						Desc:     projDesc,
					})
				}
			}
		}
	}

	// 4. Time Budget (only when no per-tool details — Top Tool insight covers breakdown)
	if !hidden["time_budget"] && latest.TimeUsageDetails == "" {
		// No per-tool details — show basic time budget insight
		timeSev := severityFromPercent(timePercent)
		resp.Insights = append(resp.Insights, insightItem{
			Key:  "time_budget",
			Type: "factual", Severity: timeSev,
			Title:    "Time Budget",
			Metric:   fmt.Sprintf("%d%%", latest.TimePercentage),
			Sublabel: fmt.Sprintf("%.0f of %.0f used", timeUsed, timeBudget),
			Desc:     fmt.Sprintf("%.0f of %.0f time budget used (%d%%), %.0f remaining.", timeUsed, timeBudget, latest.TimePercentage, timeRemaining),
		})
	}

	// 5. 24h Token Trend (compare first half vs second half of snapshots)
	if !hidden["trend_24h"] && len(snapshots24h) >= 4 {
		mid := len(snapshots24h) / 2
		firstHalf := snapshots24h[:mid]
		secondHalf := snapshots24h[mid:]

		firstDelta := firstHalf[len(firstHalf)-1].TokensCurrentValue - firstHalf[0].TokensCurrentValue
		secondDelta := secondHalf[len(secondHalf)-1].TokensCurrentValue - secondHalf[0].TokensCurrentValue

		firstElapsed := firstHalf[len(firstHalf)-1].CapturedAt.Sub(firstHalf[0].CapturedAt).Hours()
		secondElapsed := secondHalf[len(secondHalf)-1].CapturedAt.Sub(secondHalf[0].CapturedAt).Hours()

		if firstElapsed > 0 && secondElapsed > 0 {
			firstRate := firstDelta / firstElapsed
			secondRate := secondDelta / secondElapsed

			if firstRate > 0 {
				change := ((secondRate - firstRate) / firstRate) * 100
				var trendSev, trendMetric, trendDesc string
				switch {
				case change > 25:
					trendSev = "warning"
					trendMetric = fmt.Sprintf("+%.0f%%", change)
					trendDesc = fmt.Sprintf("Token consumption accelerating: recent rate ~%s/hr vs earlier ~%s/hr.", compactNum(secondRate), compactNum(firstRate))
				case change < -25:
					trendSev = "positive"
					trendMetric = fmt.Sprintf("%.0f%%", change)
					trendDesc = fmt.Sprintf("Token consumption slowing: recent rate ~%s/hr vs earlier ~%s/hr.", compactNum(secondRate), compactNum(firstRate))
				default:
					trendSev = "positive"
					trendMetric = "Stable"
					trendDesc = fmt.Sprintf("Steady consumption: ~%s/hr over the observation period.", compactNum((firstRate+secondRate)/2))
				}
				resp.Insights = append(resp.Insights, insightItem{
					Key:  "trend_24h",
					Type: "trend", Severity: trendSev,
					Title:    "24h Trend",
					Metric:   trendMetric,
					Sublabel: "recent vs earlier",
					Desc:     trendDesc,
				})
			}
		}
	}

	// 6. 7-Day Token Summary
	if !hidden["usage_7d"] && len(snapshots7d) >= 2 {
		oldest7d := snapshots7d[0]
		newest7d := snapshots7d[len(snapshots7d)-1]
		totalDelta7d := newest7d.TokensCurrentValue - oldest7d.TokensCurrentValue
		elapsed7d := newest7d.CapturedAt.Sub(oldest7d.CapturedAt)

		if totalDelta7d > 0 && elapsed7d.Hours() > 0 {
			dailyRate := totalDelta7d / (elapsed7d.Hours() / 24)
			resp.Insights = append(resp.Insights, insightItem{
				Key:  "usage_7d",
				Type: "factual", Severity: "info",
				Title:    "7-Day Usage",
				Metric:   compactNum(totalDelta7d),
				Sublabel: fmt.Sprintf("~%s/day", compactNum(dailyRate)),
				Desc: fmt.Sprintf("%s tokens consumed over %.1f days (%d snapshots). Daily average: ~%s tokens.",
					compactNum(totalDelta7d), elapsed7d.Hours()/24, len(snapshots7d), compactNum(dailyRate)),
			})
		}
	}

	// 7. Plan Capacity (daily vs monthly context)
	if !hidden["plan_capacity"] && dailyTokenBudget > 0 {
		dailyUsedPct := (tokensUsed / dailyTokenBudget) * 100
		desc := fmt.Sprintf("Daily token limit: %s. Monthly capacity: %s (30 × daily).", compactNum(dailyTokenBudget), compactNum(monthlyTokenCapacity))
		if dailyUsedPct >= 80 {
			desc += fmt.Sprintf(" You've consumed %.0f%% of today's budget.", dailyUsedPct)
		}
		if dailyTimeBudget > 0 {
			desc += fmt.Sprintf(" Daily time limit: %.0f units (monthly: %s).", dailyTimeBudget, compactNum(monthlyTimeCapacity))
		}
		resp.Insights = append(resp.Insights, insightItem{
			Key:  "plan_capacity",
			Type: "factual", Severity: "info",
			Title:    "Plan Capacity",
			Metric:   compactNum(monthlyTokenCapacity),
			Sublabel: fmt.Sprintf("%s tokens/day", compactNum(dailyTokenBudget)),
			Desc:     desc,
		})
	}

	// 8. Tokens Per Call (efficiency metric)
	if !hidden["tokens_per_call"] && totalToolCalls > 0 && avgTokensPerCall > 0 {
		sev := "info"
		desc := fmt.Sprintf("Each tool call consumes ~%s tokens on average (%s tokens across %.0f calls).", compactNum(avgTokensPerCall), compactNum(tokensUsed), totalToolCalls)
		if dailyTokenBudget > 0 {
			callsPerDay := dailyTokenBudget / avgTokensPerCall
			desc += fmt.Sprintf(" At this rate, your daily budget supports ~%.0f calls.", callsPerDay)
			if callsPerDay < totalToolCalls*2 {
				sev = "warning"
			}
		}
		resp.Insights = append(resp.Insights, insightItem{
			Key:  "tokens_per_call",
			Type: "factual", Severity: sev,
			Title:    "Tokens Per Call",
			Metric:   compactNum(avgTokensPerCall),
			Sublabel: "avg tokens/call",
			Desc:     desc,
		})
	}

	// 9. Top Tool (dominant tool analysis)
	if !hidden["top_tool"] && latest.TimeUsageDetails != "" {
		var details []api.ZaiUsageDetail
		if err := json.Unmarshal([]byte(latest.TimeUsageDetails), &details); err == nil && len(details) > 1 {
			var topTool string
			var topUsage, totalUsage float64
			for _, d := range details {
				totalUsage += d.Usage
				if d.Usage > topUsage {
					topUsage = d.Usage
					topTool = d.ModelCode
				}
			}
			if totalUsage > 0 {
				topPct := (topUsage / totalUsage) * 100
				sev := "info"
				if topPct > 70 {
					sev = "warning"
				}
				desc := fmt.Sprintf("%s leads with %.0f calls (%.0f%% of %.0f total).", topTool, topUsage, topPct, totalUsage)
				// Find second-highest for comparison
				var secondTool string
				var secondUsage float64
				for _, d := range details {
					if d.ModelCode != topTool && d.Usage > secondUsage {
						secondUsage = d.Usage
						secondTool = d.ModelCode
					}
				}
				if secondTool != "" {
					ratio := topUsage / secondUsage
					desc += fmt.Sprintf(" %.1fx more than %s (%.0f calls).", ratio, secondTool, secondUsage)
				}
				resp.Insights = append(resp.Insights, insightItem{
					Key:  "top_tool",
					Type: "factual", Severity: sev,
					Title:    "Top Tool",
					Metric:   topTool,
					Sublabel: fmt.Sprintf("%.0f%% of calls", topPct),
					Desc:     desc,
				})
			}
		}
	}

	return resp
}

// ── Anthropic Provider Handlers ──

// currentAnthropic returns Anthropic quota status.
func (h *Handler) currentAnthropic(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildAnthropicCurrent())
}

// buildAnthropicCurrent builds the Anthropic current quota response map.
func (h *Handler) buildAnthropicCurrent() map[string]interface{} {
	now := time.Now().UTC()
	response := map[string]interface{}{
		"capturedAt": now.Format(time.RFC3339),
		"quotas":     []interface{}{},
	}

	if h.store == nil {
		return response
	}

	latest, err := h.store.QueryLatestAnthropic()
	if err != nil {
		h.logger.Error("failed to query latest Anthropic snapshot", "error", err)
		return response
	}

	if latest == nil {
		return response
	}

	response["capturedAt"] = latest.CapturedAt.Format(time.RFC3339)
	var quotas []map[string]interface{}
	for _, q := range latest.Quotas {
		qMap := map[string]interface{}{
			"name":        q.Name,
			"displayName": api.AnthropicDisplayName(q.Name),
			"utilization": q.Utilization,
			"status":      anthropicUtilStatus(q.Utilization),
		}
		if q.ResetsAt != nil {
			timeUntilReset := time.Until(*q.ResetsAt)
			qMap["resetsAt"] = q.ResetsAt.Format(time.RFC3339)
			qMap["timeUntilReset"] = formatDuration(timeUntilReset)
			qMap["timeUntilResetSeconds"] = int64(timeUntilReset.Seconds())
		}
		// Enrich with tracker data
		if h.anthropicTracker != nil {
			if summary, err := h.anthropicTracker.UsageSummary(q.Name); err == nil && summary != nil {
				qMap["currentRate"] = summary.CurrentRate
				qMap["projectedUtil"] = summary.ProjectedUtil
			}
		}
		quotas = append(quotas, qMap)
	}
	response["quotas"] = quotas
	return response
}

// anthropicUtilStatus returns a status string based on utilization percentage.
func anthropicUtilStatus(util float64) string {
	switch {
	case util >= 95:
		return "critical"
	case util >= 80:
		return "danger"
	case util >= 50:
		return "warning"
	default:
		return "healthy"
	}
}

// historyAnthropic returns Anthropic usage history.
func (h *Handler) historyAnthropic(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}
	rangeStr := r.URL.Query().Get("range")
	duration, err := parseTimeRange(rangeStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	start := now.Add(-duration)
	snapshots, err := h.store.QueryAnthropicRange(start, now)
	if err != nil {
		h.logger.Error("failed to query Anthropic history", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query history")
		return
	}
	step := downsampleStep(len(snapshots), maxChartPoints)
	last := len(snapshots) - 1
	response := make([]map[string]interface{}, 0, min(len(snapshots), maxChartPoints))
	for i, snap := range snapshots {
		if step > 1 && i != 0 && i != last && i%step != 0 {
			continue
		}
		entry := map[string]interface{}{
			"capturedAt": snap.CapturedAt.Format(time.RFC3339),
		}
		for _, q := range snap.Quotas {
			entry[q.Name] = q.Utilization
		}
		response = append(response, entry)
	}
	respondJSON(w, http.StatusOK, response)
}

// cyclesAnthropic returns per-minute Anthropic snapshot data as cycle-shaped rows.
// Each polled snapshot becomes a row, enabling 1m/5m/30m/1h grouping in the frontend.
func (h *Handler) cyclesAnthropic(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}
	quotaName := r.URL.Query().Get("type")
	if quotaName == "" {
		quotaName = "five_hour"
	}

	rangeDur := parseInsightsRange(r.URL.Query().Get("range"))
	since := time.Now().UTC().Add(-rangeDur)

	points, err := h.store.QueryAnthropicUtilizationSeries(quotaName, since)
	if err != nil {
		h.logger.Error("failed to query Anthropic utilization series", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycles")
		return
	}

	response := make([]map[string]interface{}, 0, len(points))
	for i, pt := range points {
		var delta float64
		if i > 0 {
			d := pt.Utilization - points[i-1].Utilization
			if d > 0 {
				delta = d
			}
		}
		var cycleEnd interface{}
		if i < len(points)-1 {
			cycleEnd = points[i+1].CapturedAt.Format(time.RFC3339)
		}
		response = append(response, map[string]interface{}{
			"id":              i + 1,
			"quotaName":       quotaName,
			"cycleStart":      pt.CapturedAt.Format(time.RFC3339),
			"cycleEnd":        cycleEnd,
			"peakUtilization": pt.Utilization,
			"totalDelta":      delta,
		})
	}

	// Reverse to DESC order (newest first) to match frontend expectations
	for i, j := 0, len(response)-1; i < j; i, j = i+1, j-1 {
		response[i], response[j] = response[j], response[i]
	}

	respondJSON(w, http.StatusOK, response)
}

// anthropicCycleToMap converts an AnthropicResetCycle to a JSON-friendly map.
func anthropicCycleToMap(cycle *store.AnthropicResetCycle) map[string]interface{} {
	result := map[string]interface{}{
		"id":              cycle.ID,
		"quotaName":       cycle.QuotaName,
		"cycleStart":      cycle.CycleStart.Format(time.RFC3339),
		"cycleEnd":        nil,
		"peakUtilization": cycle.PeakUtilization,
		"totalDelta":      cycle.TotalDelta,
	}
	if cycle.CycleEnd != nil {
		result["cycleEnd"] = cycle.CycleEnd.Format(time.RFC3339)
	}
	if cycle.ResetsAt != nil {
		result["renewsAt"] = cycle.ResetsAt.Format(time.RFC3339)
	}
	return result
}

// summaryAnthropic returns Anthropic usage summary.
func (h *Handler) summaryAnthropic(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildAnthropicSummaryMap())
}

// buildAnthropicSummaryMap builds the Anthropic summary response.
func (h *Handler) buildAnthropicSummaryMap() map[string]interface{} {
	response := map[string]interface{}{}
	if h.anthropicTracker != nil && h.store != nil {
		latest, err := h.store.QueryLatestAnthropic()
		if err == nil && latest != nil {
			for _, q := range latest.Quotas {
				if summary, err := h.anthropicTracker.UsageSummary(q.Name); err == nil && summary != nil {
					response[q.Name] = buildAnthropicSummaryResponse(summary)
				}
			}
		}
	}
	return response
}

// buildAnthropicSummaryResponse builds a summary response from AnthropicTracker data.
func buildAnthropicSummaryResponse(summary *tracker.AnthropicSummary) map[string]interface{} {
	result := map[string]interface{}{
		"quotaName":       summary.QuotaName,
		"currentUtil":     summary.CurrentUtil,
		"currentRate":     summary.CurrentRate,
		"projectedUtil":   summary.ProjectedUtil,
		"completedCycles": summary.CompletedCycles,
		"avgPerCycle":     summary.AvgPerCycle,
		"peakCycle":       summary.PeakCycle,
		"totalTracked":    summary.TotalTracked,
		"trackingSince":   nil,
	}
	if summary.ResetsAt != nil {
		result["resetsAt"] = summary.ResetsAt.Format(time.RFC3339)
		result["timeUntilReset"] = formatDuration(summary.TimeUntilReset)
	}
	if !summary.TrackingSince.IsZero() {
		result["trackingSince"] = summary.TrackingSince.Format(time.RFC3339)
	}
	return result
}

// insightsAnthropic returns Anthropic deep analytics.
func (h *Handler) insightsAnthropic(w http.ResponseWriter, r *http.Request, rangeDur time.Duration) {
	hidden := h.getHiddenInsightKeys()
	respondJSON(w, http.StatusOK, h.buildAnthropicInsights(hidden, rangeDur))
}

// buildAnthropicInsights builds the Anthropic insights response with per-quota analytics.
func (h *Handler) buildAnthropicInsights(hidden map[string]bool, rangeDur time.Duration) insightsResponse {
	resp := insightsResponse{Stats: []insightStat{}, Insights: []insightItem{}}
	if h.store == nil {
		return resp
	}
	latest, err := h.store.QueryLatestAnthropic()
	if err != nil || latest == nil {
		resp.Insights = append(resp.Insights, insightItem{
			Type: "info", Severity: "info",
			Title: "Getting Started",
			Desc:  "Keep onWatch running to collect Anthropic usage data. Insights will appear after a few snapshots.",
		})
		return resp
	}

	// Collect summaries for all quotas
	quotaNames, _ := h.store.QueryAllAnthropicQuotaNames()
	summaries := map[string]*tracker.AnthropicSummary{}
	if h.anthropicTracker != nil {
		for _, name := range quotaNames {
			if s, err := h.anthropicTracker.UsageSummary(name); err == nil && s != nil {
				summaries[name] = s
			}
		}
	}

	// Fetch completed cycles per quota and group into real billing periods
	quotaCycles := map[string][]*store.AnthropicResetCycle{}
	quotaBillingCount := map[string]int{}
	quotaBillingAvg := map[string]float64{}
	quotaBillingPeak := map[string]float64{}
	for _, name := range quotaNames {
		cycles, err := h.store.QueryAnthropicCycleHistory(name, 50)
		if err == nil && len(cycles) > 0 {
			quotaCycles[name] = cycles
			quotaBillingCount[name] = anthropicBillingPeriodCount(cycles)
			quotaBillingAvg[name] = anthropicBillingPeriodAvg(cycles)
			quotaBillingPeak[name] = anthropicBillingPeriodPeak(cycles)
		}
	}

	// ═══ Stats Cards ═══
	// Show avg window utilization per quota (current % already shown in KPI cards)
	for _, q := range latest.Quotas {
		if avg, ok := quotaBillingAvg[q.Name]; ok && quotaBillingCount[q.Name] > 0 {
			count := quotaBillingCount[q.Name]
			periodWord := "window"
			if count > 1 {
				periodWord = "windows"
			}
			resp.Stats = append(resp.Stats, insightStat{
				Value:    fmt.Sprintf("%.0f%%", avg),
				Label:    fmt.Sprintf("Avg %s", api.AnthropicDisplayName(q.Name)),
				Sublabel: fmt.Sprintf("across %d %s", count, periodWord),
			})
		} else {
			// No completed cycles yet — show current with "Now" label
			resp.Stats = append(resp.Stats, insightStat{
				Value: fmt.Sprintf("%.0f%%", q.Utilization),
				Label: fmt.Sprintf("%s (now)", api.AnthropicDisplayName(q.Name)),
			})
		}
	}

	// ═══ Deep Insights ═══

	// Collect rates for cross-quota analysis
	quotaRates := map[string]anthropicQuotaRate{}

	// 1. Burn Rate & Forecast per quota (replaces redundant current_* cards)
	for _, q := range latest.Quotas {
		key := fmt.Sprintf("forecast_%s", q.Name)
		if hidden[key] {
			continue
		}
		s := summaries[q.Name]
		rate := h.computeAnthropicRate(q.Name, q.Utilization, s)
		quotaRates[q.Name] = rate

		var item insightItem
		item.Key = key
		item.Title = api.AnthropicDisplayName(q.Name)

		// Build reset time string (reused across scenarios)
		resetStr := ""
		if s != nil && s.ResetsAt != nil {
			resetStr = formatDuration(s.TimeUntilReset)
		}

		if !rate.HasRate {
			// Insufficient data — show analyzing state with preview
			item.Type = "factual"
			item.Severity = "info"
			item.Metric = "Analyzing..."
			item.Sublabel = "burn rate & forecast"
			item.Desc = fmt.Sprintf("Collecting usage patterns to calculate burn rate and exhaustion forecasts. Currently at %.0f%%. This typically requires ~10 minutes of data.", q.Utilization)
		} else if rate.Rate < 0.01 {
			// Idle — truly zero consumption
			item.Type = "factual"
			item.Severity = "info"
			item.Metric = "Idle"
			if resetStr != "" {
				item.Sublabel = fmt.Sprintf("resets in %s", resetStr)
			} else {
				item.Sublabel = "no activity"
			}
			item.Desc = fmt.Sprintf("No consumption detected recently. Currently at %.0f%%.", q.Utilization)
		} else if rate.ExhaustsFirst {
			// Exhausts before reset — danger
			item.Type = "recommendation"
			item.Severity = "negative"
			item.Metric = fmt.Sprintf("%.1f%%/hr", rate.Rate)
			exhaustStr := formatDuration(rate.TimeToExhaust)
			item.Sublabel = fmt.Sprintf("exhausts in %s", exhaustStr)
			desc := fmt.Sprintf("At this rate, quota exhausts in %s.", exhaustStr)
			if resetStr != "" {
				desc += fmt.Sprintf(" Resets in %s. May hit limit before reset.", resetStr)
			}
			item.Desc = desc
		} else if rate.ProjectedPct > 80 {
			// High projected usage at reset — warning
			item.Type = "recommendation"
			item.Severity = "warning"
			item.Metric = fmt.Sprintf("%.1f%%/hr", rate.Rate)
			if resetStr != "" {
				item.Sublabel = fmt.Sprintf("~%.0f%% at reset in %s", rate.ProjectedPct, resetStr)
			} else {
				item.Sublabel = fmt.Sprintf("projected ~%.0f%%", rate.ProjectedPct)
			}
			item.Desc = fmt.Sprintf("Consuming at %.1f%%/hr. Projected ~%.0f%% at reset.", rate.Rate, rate.ProjectedPct)
		} else {
			// Safe — comfortable headroom
			item.Type = "factual"
			item.Severity = "positive"
			item.Metric = fmt.Sprintf("%.1f%%/hr", rate.Rate)
			if resetStr != "" {
				item.Sublabel = fmt.Sprintf("resets in %s", resetStr)
			} else {
				item.Sublabel = "comfortable headroom"
			}
			item.Desc = fmt.Sprintf("Consuming at %.1f%%/hr with comfortable headroom.", rate.Rate)
		}

		resp.Insights = append(resp.Insights, item)
	}

	// 2. Variance (per quota, ≥3 real billing periods)
	for _, name := range quotaNames {
		count := quotaBillingCount[name]
		avg := quotaBillingAvg[name]
		peak := quotaBillingPeak[name]
		if count < 3 || avg <= 1 {
			continue
		}
		key := fmt.Sprintf("variance_%s", name)
		if hidden[key] {
			continue
		}
		diff := ((peak - avg) / avg) * 100
		var item insightItem
		switch {
		case diff > 50:
			item = insightItem{Key: key, Type: "factual", Severity: "warning",
				Title: "High Variance", Metric: fmt.Sprintf("+%.0f%%", diff), Sublabel: api.AnthropicDisplayName(name),
				Desc: fmt.Sprintf("Peak period %.0f%% vs average %.0f%% for %s — usage varies significantly.", peak, avg, api.AnthropicDisplayName(name)),
			}
		case diff > 10:
			item = insightItem{Key: key, Type: "factual", Severity: "info",
				Title: "Usage Spread", Metric: fmt.Sprintf("+%.0f%%", diff), Sublabel: api.AnthropicDisplayName(name),
				Desc: fmt.Sprintf("Peak: %.0f%%, average: %.0f%% for %s — moderately consistent.", peak, avg, api.AnthropicDisplayName(name)),
			}
		default:
			item = insightItem{Key: key, Type: "factual", Severity: "positive",
				Title: "Consistent", Metric: fmt.Sprintf("~%.0f%%", avg), Sublabel: api.AnthropicDisplayName(name),
				Desc: fmt.Sprintf("Peak (%.0f%%) close to average (%.0f%%) for %s — predictable usage.", peak, avg, api.AnthropicDisplayName(name)),
			}
		}
		resp.Insights = append(resp.Insights, item)
	}

	// 3. Trend (per quota, ≥4 real billing periods)
	for _, name := range quotaNames {
		count := quotaBillingCount[name]
		if count < 4 {
			continue
		}
		key := fmt.Sprintf("trend_%s", name)
		if hidden[key] {
			continue
		}
		periods := groupAnthropicBillingPeriods(quotaCycles[name])
		mid := len(periods) / 2
		var recentSum, olderSum float64
		for _, p := range periods[:mid] {
			recentSum += p.maxPeak
		}
		for _, p := range periods[mid:] {
			olderSum += p.maxPeak
		}
		recentAvg := recentSum / float64(mid)
		olderAvg := olderSum / float64(len(periods)-mid)
		if olderAvg <= 0 {
			continue
		}
		change := ((recentAvg - olderAvg) / olderAvg) * 100
		var desc, sev, metric string
		switch {
		case change > 15:
			metric = fmt.Sprintf("+%.0f%%", change)
			desc = fmt.Sprintf("Recent %s periods avg %.0f%% vs earlier %.0f%% — usage is increasing.", api.AnthropicDisplayName(name), recentAvg, olderAvg)
			sev = "warning"
		case change < -15:
			metric = fmt.Sprintf("%.0f%%", change)
			desc = fmt.Sprintf("Recent %s periods avg %.0f%% vs earlier %.0f%% — usage is decreasing.", api.AnthropicDisplayName(name), recentAvg, olderAvg)
			sev = "positive"
		default:
			metric = "Stable"
			desc = fmt.Sprintf("Recent %s periods avg %.0f%% vs earlier %.0f%% — steady usage.", api.AnthropicDisplayName(name), recentAvg, olderAvg)
			sev = "positive"
		}
		resp.Insights = append(resp.Insights, insightItem{
			Key: key, Type: "trend", Severity: sev,
			Title: "Trend", Metric: metric, Sublabel: api.AnthropicDisplayName(name),
			Desc: desc,
		})
	}

	// 4. Cross-quota ratio: 5-Hour vs Weekly All-Model
	if !hidden["ratio_5h_weekly"] {
		r5h := quotaRates["five_hour"]
		r7d := quotaRates["seven_day"]
		if r5h.HasRate && r7d.HasRate && r5h.Rate >= 0.01 && r7d.Rate >= 0.01 {
			ratio := r5h.Rate / r7d.Rate
			resp.Insights = append(resp.Insights, insightItem{
				Key:      "ratio_5h_weekly",
				Type:     "factual",
				Severity: "info",
				Title:    "5-Hour vs Weekly",
				Metric:   fmt.Sprintf("1:%.0f", ratio),
				Sublabel: fmt.Sprintf("1%% weekly ~ %.0f%% of 5-hr", ratio),
				Desc: fmt.Sprintf(
					"Every 1%% of Weekly All-Model usage costs ~%.0f%% of a single 5-Hour sprint. "+
						"Based on current rates: 5-Hour at %.1f%%/hr, Weekly at %.1f%%/hr.",
					ratio, r5h.Rate, r7d.Rate),
			})
		}
	}

	// If no insights at all, add a getting-started message
	if len(resp.Insights) == 0 {
		resp.Insights = append(resp.Insights, insightItem{
			Type: "info", Severity: "info",
			Title: "Getting Started",
			Desc:  "Keep onWatch running to build up usage data. Deep insights will appear after a few cycles.",
		})
	}

	return resp
}

// anthropicQuotaRate holds computed burn rate and forecast for an Anthropic quota.
type anthropicQuotaRate struct {
	Rate          float64       // %/hr (0 if idle)
	HasRate       bool          // true if enough data to compute
	TimeToExhaust time.Duration // time until 100% at current rate
	TimeToReset   time.Duration // time until quota resets
	ExhaustsFirst bool          // true if exhaustion < reset
	ProjectedPct  float64       // projected % at reset time
}

// computeAnthropicRate computes burn rate from recent snapshots, falling back to tracker summary.
func (h *Handler) computeAnthropicRate(quotaName string, currentUtil float64, summary *tracker.AnthropicSummary) anthropicQuotaRate {
	var result anthropicQuotaRate

	// Fill reset time from summary
	if summary != nil && summary.ResetsAt != nil {
		result.TimeToReset = time.Until(*summary.ResetsAt)
	}

	// Try recent snapshots (last 30 min) for a responsive burn rate
	if h.store != nil {
		points, err := h.store.QueryAnthropicUtilizationSeries(quotaName, time.Now().Add(-30*time.Minute))
		if err == nil && len(points) >= 2 {
			first := points[0]
			last := points[len(points)-1]
			elapsed := last.CapturedAt.Sub(first.CapturedAt)
			if elapsed >= 5*time.Minute {
				delta := last.Utilization - first.Utilization
				if delta > 0 {
					result.Rate = delta / elapsed.Hours()
					result.HasRate = true
				} else {
					// Utilization didn't increase — idle
					result.Rate = 0
					result.HasRate = true
				}
			}
		}
	}

	// Fall back to tracker's cycle-averaged rate
	if !result.HasRate && summary != nil && summary.CurrentRate > 0 {
		result.Rate = summary.CurrentRate
		result.HasRate = true
	}

	// Compute derived values
	if result.HasRate && result.Rate > 0 {
		remaining := 100 - currentUtil
		if remaining > 0 {
			result.TimeToExhaust = time.Duration(remaining / result.Rate * float64(time.Hour))
		}
		if result.TimeToReset > 0 {
			result.ProjectedPct = currentUtil + (result.Rate * result.TimeToReset.Hours())
			if result.ProjectedPct > 100 {
				result.ProjectedPct = 100
			}
			result.ExhaustsFirst = result.TimeToExhaust > 0 && result.TimeToExhaust < result.TimeToReset
		}
	}

	return result
}

// severityFromPercent returns a severity string based on a usage percentage
func severityFromPercent(pct float64) string {
	switch {
	case pct >= 95:
		return "negative"
	case pct >= 80:
		return "warning"
	case pct >= 50:
		return "info"
	default:
		return "positive"
	}
}

// ── Insight helpers ──

// billingPeriod represents an actual billing period (may span many mini-cycles
// created by renewsAt jitter). A real reset boundary is detected when
// peak_requests drops by >50%, indicating the quota counter went back to ~0.
type billingPeriod struct {
	start   time.Time
	maxPeak float64
}

// groupBillingPeriods groups mini-cycles into actual billing periods.
// Cycles are expected sorted DESC (newest first, as returned by QueryCyclesSince).
func groupBillingPeriods(cycles []*store.ResetCycle) []billingPeriod {
	if len(cycles) == 0 {
		return nil
	}

	// Process in chronological order (oldest first)
	last := len(cycles) - 1
	current := billingPeriod{
		start:   cycles[last].CycleStart,
		maxPeak: cycles[last].PeakRequests,
	}

	var periods []billingPeriod
	for i := last - 1; i >= 0; i-- {
		c := cycles[i]
		// If peak drops significantly, this is a new billing period
		if c.PeakRequests < current.maxPeak*0.5 {
			periods = append(periods, current)
			current = billingPeriod{
				start:   c.CycleStart,
				maxPeak: c.PeakRequests,
			}
		} else if c.PeakRequests > current.maxPeak {
			current.maxPeak = c.PeakRequests
		}
	}
	periods = append(periods, current)
	return periods
}

// cycleSumConsumption computes total consumption by grouping mini-cycles into
// actual billing periods and summing the max peak per period.
func cycleSumConsumption(cycles []*store.ResetCycle) float64 {
	var total float64
	for _, p := range groupBillingPeriods(cycles) {
		total += p.maxPeak
	}
	return total
}

// cycleSumConsumptionSince computes consumption for cycles starting after since.
func cycleSumConsumptionSince(cycles []*store.ResetCycle, since time.Time) float64 {
	var filtered []*store.ResetCycle
	for _, c := range cycles {
		if !c.CycleStart.Before(since) {
			filtered = append(filtered, c)
		}
	}
	return cycleSumConsumption(filtered)
}

// billingPeriodCount returns the number of actual billing periods.
func billingPeriodCount(cycles []*store.ResetCycle) int {
	return len(groupBillingPeriods(cycles))
}

// billingPeriodAvg returns avg consumption per actual billing period.
func billingPeriodAvg(cycles []*store.ResetCycle) float64 {
	periods := groupBillingPeriods(cycles)
	if len(periods) == 0 {
		return 0
	}
	var total float64
	for _, p := range periods {
		total += p.maxPeak
	}
	return total / float64(len(periods))
}

// billingPeriodPeak returns the highest consumption in any single billing period.
func billingPeriodPeak(cycles []*store.ResetCycle) float64 {
	var peak float64
	for _, p := range groupBillingPeriods(cycles) {
		if p.maxPeak > peak {
			peak = p.maxPeak
		}
	}
	return peak
}

// anthropicBillingPeriod represents an actual Anthropic billing period
// (many mini-cycles from renewsAt jitter merged into one real period).
type anthropicBillingPeriod struct {
	start   time.Time
	maxPeak float64 // highest PeakUtilization across mini-cycles in this period
}

// groupAnthropicBillingPeriods merges micro-cycles caused by renewsAt jitter
// into actual billing periods. A real reset is detected when PeakUtilization
// drops by >50% (utilization went back to ~0). Cycles expected sorted DESC.
func groupAnthropicBillingPeriods(cycles []*store.AnthropicResetCycle) []anthropicBillingPeriod {
	if len(cycles) == 0 {
		return nil
	}

	// Process in chronological order (oldest first)
	last := len(cycles) - 1
	current := anthropicBillingPeriod{
		start:   cycles[last].CycleStart,
		maxPeak: cycles[last].PeakUtilization,
	}

	var periods []anthropicBillingPeriod
	for i := last - 1; i >= 0; i-- {
		c := cycles[i]
		if current.maxPeak > 5 && c.PeakUtilization < current.maxPeak*0.5 {
			// Peak dropped significantly — this is a real reset
			periods = append(periods, current)
			current = anthropicBillingPeriod{
				start:   c.CycleStart,
				maxPeak: c.PeakUtilization,
			}
		} else if c.PeakUtilization > current.maxPeak {
			current.maxPeak = c.PeakUtilization
		}
	}
	periods = append(periods, current)
	return periods
}

// anthropicBillingPeriodCount returns the number of real billing periods.
func anthropicBillingPeriodCount(cycles []*store.AnthropicResetCycle) int {
	return len(groupAnthropicBillingPeriods(cycles))
}

// anthropicBillingPeriodAvg returns the avg peak utilization per real billing period.
func anthropicBillingPeriodAvg(cycles []*store.AnthropicResetCycle) float64 {
	periods := groupAnthropicBillingPeriods(cycles)
	if len(periods) == 0 {
		return 0
	}
	var total float64
	for _, p := range periods {
		total += p.maxPeak
	}
	return total / float64(len(periods))
}

// anthropicBillingPeriodPeak returns the highest peak utilization across all real billing periods.
func anthropicBillingPeriodPeak(cycles []*store.AnthropicResetCycle) float64 {
	var peak float64
	for _, p := range groupAnthropicBillingPeriods(cycles) {
		if p.maxPeak > peak {
			peak = p.maxPeak
		}
	}
	return peak
}

func compactNum(v float64) string {
	if v >= 1000000000 {
		return fmt.Sprintf("%.1fB", v/1000000000)
	}
	if v >= 1000000 {
		return fmt.Sprintf("%.1fM", v/1000000)
	}
	if v >= 1000 {
		return fmt.Sprintf("%.1fK", v/1000)
	}
	return fmt.Sprintf("%.0f", v)
}

// GetSettings returns current settings as JSON.
func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	tz := ""
	var hiddenInsights []string
	if h.store != nil {
		val, err := h.store.GetSetting("timezone")
		if err != nil {
			h.logger.Error("failed to get timezone setting", "error", err)
		} else {
			tz = val
		}
		hiVal, err := h.store.GetSetting("hidden_insights")
		if err != nil {
			h.logger.Error("failed to get hidden_insights setting", "error", err)
		} else if hiVal != "" {
			_ = json.Unmarshal([]byte(hiVal), &hiddenInsights)
		}
	}
	if hiddenInsights == nil {
		hiddenInsights = []string{}
	}

	result := map[string]interface{}{
		"timezone":        tz,
		"hidden_insights": hiddenInsights,
	}

	// SMTP settings (never return the actual password)
	if h.store != nil {
		smtpJSON, _ := h.store.GetSetting("smtp")
		if smtpJSON != "" {
			var smtp map[string]interface{}
			if json.Unmarshal([]byte(smtpJSON), &smtp) == nil {
				// Mask the password — only indicate whether one is set
				if _, ok := smtp["password"]; ok {
					pwd, _ := smtp["password"].(string)
					smtp["password"] = ""
					smtp["password_set"] = pwd != ""
				}
				result["smtp"] = smtp
			}
		}

		// Notification settings
		notifJSON, _ := h.store.GetSetting("notifications")
		if notifJSON != "" {
			var notif map[string]interface{}
			if json.Unmarshal([]byte(notifJSON), &notif) == nil {
				result["notifications"] = notif
			}
		}

		// Provider visibility settings
		visJSON, _ := h.store.GetSetting("provider_visibility")
		if visJSON != "" {
			var vis map[string]interface{}
			if json.Unmarshal([]byte(visJSON), &vis) == nil {
				result["provider_visibility"] = vis
			}
		}
	}

	respondJSON(w, http.StatusOK, result)
}

// emailRegex validates email addresses.
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// UpdateSettings updates settings from JSON body (partial updates supported).
func (h *Handler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Limit request body size to 64KB
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)

	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// Check if error is due to MaxBytesReader limit exceeded
		if err.Error() == "http: request body too large" {
			respondError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if h.store == nil {
		respondError(w, http.StatusInternalServerError, "store not available")
		return
	}

	result := map[string]interface{}{}

	// Handle timezone
	if raw, ok := body["timezone"]; ok {
		var tz string
		if err := json.Unmarshal(raw, &tz); err != nil {
			respondError(w, http.StatusBadRequest, "invalid timezone value")
			return
		}
		if tz != "" {
			if _, err := time.LoadLocation(tz); err != nil {
				respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid timezone: %s", tz))
				return
			}
		}
		if err := h.store.SetSetting("timezone", tz); err != nil {
			h.logger.Error("failed to save timezone setting", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
		result["timezone"] = tz
	}

	// Handle hidden_insights
	if raw, ok := body["hidden_insights"]; ok {
		var keys []string
		if err := json.Unmarshal(raw, &keys); err != nil {
			respondError(w, http.StatusBadRequest, "invalid hidden_insights value")
			return
		}
		if keys == nil {
			keys = []string{}
		}
		hiddenJSON, _ := json.Marshal(keys)
		if err := h.store.SetSetting("hidden_insights", string(hiddenJSON)); err != nil {
			h.logger.Error("failed to save hidden_insights setting", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
		result["hidden_insights"] = keys
	}

	// Handle SMTP settings
	if raw, ok := body["smtp"]; ok {
		var smtp struct {
			Host        string `json:"host"`
			Port        int    `json:"port"`
			Protocol    string `json:"protocol"`
			Username    string `json:"username"`
			Password    string `json:"password"`
			FromAddress string `json:"from_address"`
			FromName    string `json:"from_name"`
			To          string `json:"to"`
		}
		if err := json.Unmarshal(raw, &smtp); err != nil {
			respondError(w, http.StatusBadRequest, "invalid smtp value")
			return
		}
		// Validate
		if smtp.Port < 0 || smtp.Port > 65535 {
			respondError(w, http.StatusBadRequest, "SMTP port must be between 1 and 65535")
			return
		}
		validProtocols := map[string]bool{"tls": true, "starttls": true, "none": true, "": true}
		if !validProtocols[smtp.Protocol] {
			respondError(w, http.StatusBadRequest, "SMTP protocol must be tls, starttls, or none")
			return
		}
		if smtp.FromAddress != "" && !emailRegex.MatchString(smtp.FromAddress) {
			respondError(w, http.StatusBadRequest, "invalid from address")
			return
		}
		if smtp.To != "" {
			for _, addr := range strings.Split(smtp.To, ",") {
				addr = strings.TrimSpace(addr)
				if addr != "" && !emailRegex.MatchString(addr) {
					respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid recipient address: %s", addr))
					return
				}
			}
		}

		// If password is empty, preserve the existing password
		if smtp.Password == "" {
			existingJSON, _ := h.store.GetSetting("smtp")
			if existingJSON != "" {
				var existing map[string]interface{}
				if json.Unmarshal([]byte(existingJSON), &existing) == nil {
					if pwd, ok := existing["password"].(string); ok {
						smtp.Password = pwd
					}
				}
			}
		}

		// Encrypt SMTP password using admin password hash as key
		if smtp.Password != "" && !IsEncryptedValue(smtp.Password) {
			encryptionKey := DeriveEncryptionKey(h.sessions.passwordHash, nil)
			encryptedPass, err := notify.Encrypt(smtp.Password, encryptionKey)
			if err != nil {
				h.logger.Error("failed to encrypt SMTP password", "error", err)
				respondError(w, http.StatusInternalServerError, "failed to encrypt SMTP password")
				return
			}
			smtp.Password = encryptedPass
		}

		smtpJSON, _ := json.Marshal(smtp)
		if err := h.store.SetSetting("smtp", string(smtpJSON)); err != nil {
			h.logger.Error("failed to save SMTP settings", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to save SMTP settings")
			return
		}
		result["smtp"] = "saved"

		// Reconfigure SMTP mailer with new settings
		if h.notifier != nil {
			if err := h.notifier.ConfigureSMTP(); err != nil {
				h.logger.Error("failed to reconfigure SMTP after settings update", "error", err)
			}
		}
	}

	// Handle notification settings
	if raw, ok := body["notifications"]; ok {
		var notif struct {
			WarningThreshold  float64 `json:"warning_threshold"`
			CriticalThreshold float64 `json:"critical_threshold"`
			NotifyWarning     bool    `json:"notify_warning"`
			NotifyCritical    bool    `json:"notify_critical"`
			NotifyReset       bool    `json:"notify_reset"`
			CooldownMinutes   int     `json:"cooldown_minutes"`
			Overrides         []struct {
				QuotaKey   string  `json:"quota_key"`
				Provider   string  `json:"provider"`
				Warning    float64 `json:"warning"`
				Critical   float64 `json:"critical"`
				IsAbsolute bool    `json:"is_absolute"`
			} `json:"overrides"`
		}
		if err := json.Unmarshal(raw, &notif); err != nil {
			respondError(w, http.StatusBadRequest, "invalid notifications value")
			return
		}
		// Validate thresholds
		if notif.WarningThreshold < 0 || notif.WarningThreshold > 100 {
			respondError(w, http.StatusBadRequest, "warning threshold must be between 0 and 100")
			return
		}
		if notif.CriticalThreshold < 0 || notif.CriticalThreshold > 100 {
			respondError(w, http.StatusBadRequest, "critical threshold must be between 0 and 100")
			return
		}
		if notif.WarningThreshold >= notif.CriticalThreshold {
			respondError(w, http.StatusBadRequest, "warning threshold must be less than critical threshold")
			return
		}
		if notif.CooldownMinutes < 1 {
			notif.CooldownMinutes = 1
		}
		// Validate per-quota overrides
		for _, o := range notif.Overrides {
			if o.IsAbsolute {
				if o.Warning < 0 || o.Critical < 0 {
					respondError(w, http.StatusBadRequest, "absolute threshold values must be >= 0")
					return
				}
			} else {
				if o.Warning < 0 || o.Warning > 100 || o.Critical < 0 || o.Critical > 100 {
					respondError(w, http.StatusBadRequest, "percentage threshold values must be between 0 and 100")
					return
				}
			}
		}

		notifJSON, _ := json.Marshal(notif)
		if err := h.store.SetSetting("notifications", string(notifJSON)); err != nil {
			h.logger.Error("failed to save notification settings", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to save notification settings")
			return
		}
		result["notifications"] = "saved"

		// Reload notifier if available
		if h.notifier != nil {
			if err := h.notifier.Reload(); err != nil {
				h.logger.Error("failed to reload notifier after notification update", "error", err)
			}
		}
	}

	// Handle provider visibility
	if raw, ok := body["provider_visibility"]; ok {
		var vis map[string]map[string]bool
		if err := json.Unmarshal(raw, &vis); err != nil {
			respondError(w, http.StatusBadRequest, "invalid provider_visibility value")
			return
		}
		visJSON, _ := json.Marshal(vis)
		if err := h.store.SetSetting("provider_visibility", string(visJSON)); err != nil {
			h.logger.Error("failed to save provider visibility settings", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to save provider visibility settings")
			return
		}
		result["provider_visibility"] = vis
	}

	respondJSON(w, http.StatusOK, result)
}

// SMTPTest sends a test email via the configured SMTP settings.
func (h *Handler) SMTPTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Rate limit: 30 second cooldown
	h.smtpTestMu.Lock()
	elapsed := time.Since(h.smtpTestLastSent)
	if elapsed < 30*time.Second {
		h.smtpTestMu.Unlock()
		remaining := int((30*time.Second - elapsed).Seconds())
		respondError(w, http.StatusTooManyRequests, fmt.Sprintf("please wait %d seconds before sending another test", remaining))
		return
	}
	h.smtpTestLastSent = time.Now()
	h.smtpTestMu.Unlock()

	if h.notifier == nil {
		respondError(w, http.StatusServiceUnavailable, "notification engine not configured")
		return
	}

	if err := h.notifier.SendTestEmail(); err != nil {
		h.logger.Error("SMTP test failed", "error", err)
		// Sanitize error message to prevent information leakage
		errorMsg := sanitizeSMTPError(err)
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"message": errorMsg,
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Test email sent successfully",
	})
}

// PushVAPIDKey returns the VAPID public key for push subscription.
func (h *Handler) PushVAPIDKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.notifier == nil {
		respondError(w, http.StatusServiceUnavailable, "notification engine not configured")
		return
	}
	key := h.notifier.GetVAPIDPublicKey()
	if key == "" {
		respondError(w, http.StatusServiceUnavailable, "push notifications not configured")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"public_key": key})
}

// PushSubscribe handles POST (subscribe) and DELETE (unsubscribe) for push notifications.
func (h *Handler) PushSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		// Limit request body size to 64KB
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)

		var body struct {
			Endpoint string `json:"endpoint"`
			Keys     struct {
				P256dh string `json:"p256dh"`
				Auth   string `json:"auth"`
			} `json:"keys"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			if err.Error() == "http: request body too large" {
				respondError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			respondError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.Endpoint == "" || body.Keys.P256dh == "" || body.Keys.Auth == "" {
			respondError(w, http.StatusBadRequest, "endpoint, p256dh, and auth are required")
			return
		}
		if err := h.store.SavePushSubscription(body.Endpoint, body.Keys.P256dh, body.Keys.Auth); err != nil {
			h.logger.Error("failed to save push subscription", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to save subscription")
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "subscribed"})
		return
	}

	if r.Method == http.MethodDelete {
		// Limit request body size to 64KB
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)

		var body struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			if err.Error() == "http: request body too large" {
				respondError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			respondError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.Endpoint == "" {
			respondError(w, http.StatusBadRequest, "endpoint is required")
			return
		}
		if err := h.store.DeletePushSubscription(body.Endpoint); err != nil {
			h.logger.Error("failed to delete push subscription", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to delete subscription")
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "unsubscribed"})
		return
	}

	respondError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// PushTest sends a test push notification to all subscribed devices.
func (h *Handler) PushTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Rate limit: 30 second cooldown
	h.pushTestMu.Lock()
	elapsed := time.Since(h.pushTestLastSent)
	if elapsed < 30*time.Second {
		h.pushTestMu.Unlock()
		remaining := int((30*time.Second - elapsed).Seconds())
		respondError(w, http.StatusTooManyRequests, fmt.Sprintf("please wait %d seconds before sending another test", remaining))
		return
	}
	h.pushTestLastSent = time.Now()
	h.pushTestMu.Unlock()

	if h.notifier == nil {
		respondError(w, http.StatusServiceUnavailable, "notification engine not configured")
		return
	}

	if err := h.notifier.SendTestPush(); err != nil {
		h.logger.Error("push test failed", "error", err)
		// Return generic error message to prevent information leakage
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"message": "Push test failed",
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Test push notification sent",
	})
}

// Login handles GET (show form) and POST (authenticate).
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	// If already logged in, redirect to dashboard
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if h.sessions != nil && h.sessions.ValidateToken(cookie.Value) {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	}

	if r.Method == http.MethodPost {
		h.loginPost(w, r)
		return
	}

	// Use whitelisted error messages to prevent XSS and info leakage
	errorCode := r.URL.Query().Get("error")
	errorMsg := loginErrors[errorCode] // empty string if not in whitelist

	data := map[string]interface{}{
		"Title":   "Login",
		"Error":   errorMsg,
		"Version": h.version,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.loginTmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		h.logger.Error("failed to render login template", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *Handler) loginPost(w http.ResponseWriter, r *http.Request) {
	// Check rate limit before processing login attempt
	if h.rateLimiter != nil {
		clientIP := getClientIP(r)
		if h.rateLimiter.IsBlocked(clientIP) {
			w.Header().Set("Retry-After", "300") // 5 minutes in seconds
			http.Redirect(w, r, "/login?error="+LoginErrorRateLimit, http.StatusFound)
			return
		}
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error="+LoginErrorInvalid, http.StatusFound)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if h.sessions == nil {
		http.Redirect(w, r, "/login?error="+LoginErrorRequired, http.StatusFound)
		return
	}

	token, ok := h.sessions.Authenticate(username, password)
	if !ok {
		// Record failed attempt for rate limiting
		if h.rateLimiter != nil {
			clientIP := getClientIP(r)
			if h.rateLimiter.RecordFailure(clientIP) {
				// IP is now blocked
				w.Header().Set("Retry-After", "300")
			}
		}
		http.Redirect(w, r, "/login?error="+LoginErrorInvalid, http.StatusFound)
		return
	}

	// Clear rate limit on successful login
	if h.rateLimiter != nil {
		clientIP := getClientIP(r)
		h.rateLimiter.Clear(clientIP)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   sessionMaxAge,
		HttpOnly: true,
		Secure:   h.config.SecureCookies || (h.config.Host != "" && h.config.Host != "0.0.0.0" && h.config.Host != "127.0.0.1"),
		SameSite: http.SameSiteStrictMode,
	})

	http.Redirect(w, r, "/", http.StatusFound)
}

// Logout clears the session and redirects to login.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && h.sessions != nil {
		h.sessions.Invalidate(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// ChangePassword handles password change requests.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.sessions == nil || h.store == nil {
		respondError(w, http.StatusInternalServerError, "auth not configured")
		return
	}

	// Limit request body size to 64KB
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err.Error() == "http: request body too large" {
			respondError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.CurrentPassword == "" || req.NewPassword == "" {
		respondError(w, http.StatusBadRequest, "current and new passwords are required")
		return
	}

	if len(req.NewPassword) < 6 {
		respondError(w, http.StatusBadRequest, "new password must be at least 6 characters")
		return
	}

	// Verify current password and get old hash for re-encryption
	oldHash := h.sessions.passwordHash
	_, ok := h.sessions.Authenticate(h.sessions.username, req.CurrentPassword)
	if !ok {
		respondError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}

	// Hash and store new password
	newHash, err := HashPassword(req.NewPassword)
	if err != nil {
		h.logger.Error("failed to hash new password", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to process new password")
		return
	}
	if err := h.store.UpsertUser(h.sessions.username, newHash); err != nil {
		h.logger.Error("failed to update password in database", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to save new password")
		return
	}

	// Update in-memory hash
	h.sessions.UpdatePassword(newHash)

	// Re-encrypt all encrypted data with new password key
	reEncryptErrors := ReEncryptAllData(h.store, oldHash, newHash)
	if len(reEncryptErrors) > 0 {
		h.logger.Warn("some data could not be re-encrypted during password change", "errors", reEncryptErrors)
		// Continue anyway - data might need manual re-entry or was already encrypted with new key
	}

	// Invalidate all sessions (force re-login)
	h.sessions.InvalidateAll()

	respondJSON(w, http.StatusOK, map[string]string{"message": "password updated successfully"})
}

// CheckUpdate checks for available updates (GET /api/update/check).
func (h *Handler) CheckUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.updater == nil {
		respondError(w, http.StatusServiceUnavailable, "updater not configured")
		return
	}
	info, err := h.updater.Check()
	if err != nil {
		h.logger.Error("update check failed", "error", err)
		respondError(w, http.StatusInternalServerError, "update check failed")
		return
	}
	respondJSON(w, http.StatusOK, info)
}

// ApplyUpdate downloads and applies an update (POST /api/update/apply).
func (h *Handler) ApplyUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.updater == nil {
		respondError(w, http.StatusServiceUnavailable, "updater not configured")
		return
	}
	if err := h.updater.Apply(); err != nil {
		h.logger.Error("update apply failed", "error", err)
		// Return generic error message to prevent information leakage
		respondError(w, http.StatusInternalServerError, "update failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "updated"})

	// Schedule restart after response is flushed
	go func() {
		time.Sleep(1 * time.Second)
		if err := h.updater.Restart(); err != nil {
			h.logger.Error("restart after update failed", "error", err)
		}
	}()
}

// CycleOverview returns cycle overview with cross-quota data at peak moments.
func (h *Handler) CycleOverview(w http.ResponseWriter, r *http.Request) {
	provider, err := h.getProviderFromRequest(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	switch provider {
	case "both":
		h.cycleOverviewBoth(w, r)
	case "zai":
		h.cycleOverviewZai(w, r)
	case "synthetic":
		h.cycleOverviewSynthetic(w, r)
	case "anthropic":
		h.cycleOverviewAnthropic(w, r)
	case "copilot":
		h.cycleOverviewCopilot(w, r)
	case "codex":
		h.cycleOverviewCodex(w, r)
	default:
		respondError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider: %s", provider))
	}
}

// parseCycleOverviewLimit parses the limit query param, defaulting to 50.
// Caps at 500 to prevent unbounded queries.
func parseCycleOverviewLimit(r *http.Request) int {
	const maxLimit = 500
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			if n > maxLimit {
				return maxLimit
			}
			return n
		}
	}
	return 50
}

// cycleOverviewSynthetic returns Synthetic cycle overview with cross-quota data.
func (h *Handler) cycleOverviewSynthetic(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"cycles": []interface{}{}})
		return
	}

	groupBy := r.URL.Query().Get("groupBy")
	if groupBy == "" {
		groupBy = "subscription"
	}

	limit := parseCycleOverviewLimit(r)
	rows, err := h.store.QuerySyntheticCycleOverview(groupBy, limit)
	if err != nil {
		h.logger.Error("failed to query synthetic cycle overview", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycle overview")
		return
	}

	quotaNames := []string{"subscription", "search", "toolcall"}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"groupBy":    groupBy,
		"provider":   "synthetic",
		"quotaNames": quotaNames,
		"cycles":     cycleOverviewRowsToJSON(rows),
	})
}

// cycleOverviewZai returns Z.ai cycle overview with cross-quota data.
func (h *Handler) cycleOverviewZai(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"cycles": []interface{}{}})
		return
	}

	groupBy := r.URL.Query().Get("groupBy")
	if groupBy == "" {
		groupBy = "tokens"
	}

	limit := parseCycleOverviewLimit(r)
	rows, err := h.store.QueryZaiCycleOverview(groupBy, limit)
	if err != nil {
		h.logger.Error("failed to query Z.ai cycle overview", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycle overview")
		return
	}

	quotaNames := []string{"tokens", "time"}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"groupBy":    groupBy,
		"provider":   "zai",
		"quotaNames": quotaNames,
		"cycles":     cycleOverviewRowsToJSON(rows),
	})
}

// cycleOverviewAnthropic returns Anthropic cycle overview with cross-quota data.
func (h *Handler) cycleOverviewAnthropic(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"cycles": []interface{}{}})
		return
	}

	groupBy := r.URL.Query().Get("groupBy")
	if groupBy == "" {
		groupBy = "five_hour"
	}

	limit := parseCycleOverviewLimit(r)
	rows, err := h.store.QueryAnthropicCycleOverview(groupBy, limit)
	if err != nil {
		h.logger.Error("failed to query Anthropic cycle overview", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycle overview")
		return
	}

	// Determine quota names from first row with cross-quota data, or default
	quotaNames := []string{}
	for _, row := range rows {
		if len(row.CrossQuotas) > 0 {
			for _, cq := range row.CrossQuotas {
				quotaNames = append(quotaNames, cq.Name)
			}
			break
		}
	}
	if len(quotaNames) == 0 {
		// Fallback defaults
		quotaNames = []string{"five_hour", "seven_day", "seven_day_sonnet"}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"groupBy":    groupBy,
		"provider":   "anthropic",
		"quotaNames": quotaNames,
		"cycles":     cycleOverviewRowsToJSON(rows),
	})
}

// cycleOverviewBoth returns combined cycle overview from all configured providers.
func (h *Handler) cycleOverviewBoth(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{}
	if h.store == nil {
		respondJSON(w, http.StatusOK, response)
		return
	}

	limit := parseCycleOverviewLimit(r)

	if h.config.HasProvider("synthetic") {
		groupBy := r.URL.Query().Get("groupBy")
		if groupBy == "" {
			groupBy = "subscription"
		}
		if rows, err := h.store.QuerySyntheticCycleOverview(groupBy, limit); err == nil {
			response["synthetic"] = map[string]interface{}{
				"groupBy":    groupBy,
				"provider":   "synthetic",
				"quotaNames": []string{"subscription", "search", "toolcall"},
				"cycles":     cycleOverviewRowsToJSON(rows),
			}
		}
	}

	if h.config.HasProvider("zai") {
		groupBy := r.URL.Query().Get("zaiGroupBy")
		if groupBy == "" {
			groupBy = "tokens"
		}
		if rows, err := h.store.QueryZaiCycleOverview(groupBy, limit); err == nil {
			response["zai"] = map[string]interface{}{
				"groupBy":    groupBy,
				"provider":   "zai",
				"quotaNames": []string{"tokens", "time"},
				"cycles":     cycleOverviewRowsToJSON(rows),
			}
		}
	}

	if h.config.HasProvider("anthropic") {
		groupBy := r.URL.Query().Get("anthropicGroupBy")
		if groupBy == "" {
			groupBy = "five_hour"
		}
		if rows, err := h.store.QueryAnthropicCycleOverview(groupBy, limit); err == nil {
			quotaNames := []string{}
			for _, row := range rows {
				if len(row.CrossQuotas) > 0 {
					for _, cq := range row.CrossQuotas {
						quotaNames = append(quotaNames, cq.Name)
					}
					break
				}
			}
			if len(quotaNames) == 0 {
				quotaNames = []string{"five_hour", "seven_day", "seven_day_sonnet"}
			}
			response["anthropic"] = map[string]interface{}{
				"groupBy":    groupBy,
				"provider":   "anthropic",
				"quotaNames": quotaNames,
				"cycles":     cycleOverviewRowsToJSON(rows),
			}
		}
	}

	if h.config.HasProvider("copilot") {
		groupBy := r.URL.Query().Get("copilotGroupBy")
		if groupBy == "" {
			groupBy = "premium_interactions"
		}
		if rows, err := h.store.QueryCopilotCycleOverview(groupBy, limit); err == nil {
			quotaNames := []string{}
			for _, row := range rows {
				if len(row.CrossQuotas) > 0 {
					for _, cq := range row.CrossQuotas {
						quotaNames = append(quotaNames, cq.Name)
					}
					break
				}
			}
			if len(quotaNames) == 0 {
				quotaNames = []string{"premium_interactions", "chat", "completions"}
			}
			response["copilot"] = map[string]interface{}{
				"groupBy":    groupBy,
				"provider":   "copilot",
				"quotaNames": quotaNames,
				"cycles":     cycleOverviewRowsToJSON(rows),
			}
		}
	}

	if h.config.HasProvider("codex") {
		groupBy := r.URL.Query().Get("codexGroupBy")
		if groupBy == "" {
			groupBy = r.URL.Query().Get("groupBy")
		}
		if groupBy == "" {
			groupBy = "five_hour"
		}
		if rows, err := h.store.QueryCodexCycleOverview(groupBy, limit); err == nil {
			quotaNames := []string{}
			for _, row := range rows {
				if len(row.CrossQuotas) > 0 {
					for _, cq := range row.CrossQuotas {
						quotaNames = append(quotaNames, cq.Name)
					}
					break
				}
			}
			if len(quotaNames) == 0 {
				quotaNames = []string{"five_hour", "seven_day"}
			}
			response["codex"] = map[string]interface{}{
				"groupBy":    groupBy,
				"provider":   "codex",
				"quotaNames": quotaNames,
				"cycles":     cycleOverviewRowsToJSON(rows),
			}
		}
	}

	respondJSON(w, http.StatusOK, response)
}

// cycleOverviewRowsToJSON converts CycleOverviewRow slices to JSON-friendly maps.
func cycleOverviewRowsToJSON(rows []store.CycleOverviewRow) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		entry := map[string]interface{}{
			"cycleId":    row.CycleID,
			"quotaType":  row.QuotaType,
			"cycleStart": row.CycleStart.Format(time.RFC3339),
			"peakValue":  row.PeakValue,
			"totalDelta": row.TotalDelta,
			"peakTime":   row.PeakTime.Format(time.RFC3339),
		}
		if row.CycleEnd != nil {
			entry["cycleEnd"] = row.CycleEnd.Format(time.RFC3339)
		} else {
			entry["cycleEnd"] = nil
		}

		crossQuotas := make([]map[string]interface{}, 0, len(row.CrossQuotas))
		for _, cq := range row.CrossQuotas {
			crossQuotas = append(crossQuotas, map[string]interface{}{
				"name":         cq.Name,
				"value":        cq.Value,
				"limit":        cq.Limit,
				"percent":      cq.Percent,
				"startPercent": cq.StartPercent,
				"delta":        cq.Delta,
			})
		}
		entry["crossQuotas"] = crossQuotas
		result = append(result, entry)
	}
	return result
}

// ── Copilot Handlers ──

// currentCopilot returns current Copilot quota status.
func (h *Handler) currentCopilot(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildCopilotCurrent())
}

// buildCopilotCurrent builds the Copilot current quota response map.
func (h *Handler) buildCopilotCurrent() map[string]interface{} {
	now := time.Now().UTC()
	response := map[string]interface{}{
		"capturedAt": now.Format(time.RFC3339),
		"quotas":     []interface{}{},
	}

	if h.store == nil {
		return response
	}

	latest, err := h.store.QueryLatestCopilot()
	if err != nil {
		h.logger.Error("failed to query latest Copilot snapshot", "error", err)
		return response
	}

	if latest == nil {
		return response
	}

	response["capturedAt"] = latest.CapturedAt.Format(time.RFC3339)
	if latest.CopilotPlan != "" {
		response["copilotPlan"] = latest.CopilotPlan
	}

	var quotas []map[string]interface{}
	for _, q := range latest.Quotas {
		usagePercent := 0.0
		if q.Entitlement > 0 {
			usagePercent = float64(q.Entitlement-q.Remaining) / float64(q.Entitlement) * 100
		}
		qMap := map[string]interface{}{
			"name":             q.Name,
			"displayName":      api.CopilotDisplayName(q.Name),
			"entitlement":      q.Entitlement,
			"remaining":        q.Remaining,
			"percentRemaining": q.PercentRemaining,
			"usagePercent":     usagePercent,
			"unlimited":        q.Unlimited,
			"status":           copilotUsageStatus(usagePercent, q.Unlimited),
		}
		if latest.ResetDate != nil {
			timeUntilReset := time.Until(*latest.ResetDate)
			qMap["resetDate"] = latest.ResetDate.Format(time.RFC3339)
			qMap["timeUntilReset"] = formatDuration(timeUntilReset)
			qMap["timeUntilResetSeconds"] = int64(timeUntilReset.Seconds())
		}
		// Enrich with tracker data
		if h.copilotTracker != nil {
			if summary, err := h.copilotTracker.UsageSummary(q.Name); err == nil && summary != nil {
				qMap["currentRate"] = summary.CurrentRate
				qMap["projectedUsage"] = summary.ProjectedUsage
			}
		}
		quotas = append(quotas, qMap)
	}
	response["quotas"] = quotas
	return response
}

// copilotUsageStatus returns a status string based on usage percentage.
func copilotUsageStatus(usagePercent float64, unlimited bool) string {
	if unlimited {
		return "healthy"
	}
	switch {
	case usagePercent >= 95:
		return "critical"
	case usagePercent >= 80:
		return "danger"
	case usagePercent >= 50:
		return "warning"
	default:
		return "healthy"
	}
}

// historyCopilot returns Copilot usage history.
func (h *Handler) historyCopilot(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}
	rangeStr := r.URL.Query().Get("range")
	duration, err := parseTimeRange(rangeStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	start := now.Add(-duration)
	snapshots, err := h.store.QueryCopilotRange(start, now)
	if err != nil {
		h.logger.Error("failed to query Copilot history", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query history")
		return
	}
	step := downsampleStep(len(snapshots), maxChartPoints)
	last := len(snapshots) - 1
	response := make([]map[string]interface{}, 0, min(len(snapshots), maxChartPoints))
	for i, snap := range snapshots {
		if step > 1 && i != 0 && i != last && i%step != 0 {
			continue
		}
		entry := map[string]interface{}{
			"capturedAt": snap.CapturedAt.Format(time.RFC3339),
		}
		for _, q := range snap.Quotas {
			if q.Entitlement > 0 {
				entry[q.Name] = float64(q.Entitlement-q.Remaining) / float64(q.Entitlement) * 100
			}
		}
		response = append(response, entry)
	}
	respondJSON(w, http.StatusOK, response)
}

// cyclesCopilot returns per-minute Copilot snapshot data as cycle-shaped rows.
func (h *Handler) cyclesCopilot(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}
	quotaName := r.URL.Query().Get("type")
	if quotaName == "" {
		quotaName = "premium_interactions"
	}

	rangeDur := parseInsightsRange(r.URL.Query().Get("range"))
	since := time.Now().UTC().Add(-rangeDur)

	points, err := h.store.QueryCopilotUsageSeries(quotaName, since)
	if err != nil {
		h.logger.Error("failed to query Copilot usage series", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycles")
		return
	}

	response := make([]map[string]interface{}, 0, len(points))
	for i, pt := range points {
		usagePercent := 0.0
		if pt.Entitlement > 0 {
			usagePercent = float64(pt.Entitlement-pt.Remaining) / float64(pt.Entitlement) * 100
		}
		var delta float64
		if i > 0 {
			prevPercent := 0.0
			if points[i-1].Entitlement > 0 {
				prevPercent = float64(points[i-1].Entitlement-points[i-1].Remaining) / float64(points[i-1].Entitlement) * 100
			}
			d := usagePercent - prevPercent
			if d > 0 {
				delta = d
			}
		}
		var cycleEnd interface{}
		if i < len(points)-1 {
			cycleEnd = points[i+1].CapturedAt.Format(time.RFC3339)
		}
		response = append(response, map[string]interface{}{
			"id":              i + 1,
			"quotaName":       quotaName,
			"cycleStart":      pt.CapturedAt.Format(time.RFC3339),
			"cycleEnd":        cycleEnd,
			"peakUtilization": usagePercent,
			"totalDelta":      delta,
		})
	}

	// Reverse to DESC order (newest first)
	for i, j := 0, len(response)-1; i < j; i, j = i+1, j-1 {
		response[i], response[j] = response[j], response[i]
	}

	respondJSON(w, http.StatusOK, response)
}

// copilotCycleToMap converts a CopilotResetCycle to a JSON-friendly map.
func copilotCycleToMap(cycle *store.CopilotResetCycle) map[string]interface{} {
	result := map[string]interface{}{
		"id":         cycle.ID,
		"quotaName":  cycle.QuotaName,
		"cycleStart": cycle.CycleStart.Format(time.RFC3339),
		"cycleEnd":   nil,
		"peakUsed":   cycle.PeakUsed,
		"totalDelta": cycle.TotalDelta,
	}
	if cycle.CycleEnd != nil {
		result["cycleEnd"] = cycle.CycleEnd.Format(time.RFC3339)
	}
	if cycle.ResetDate != nil {
		result["resetDate"] = cycle.ResetDate.Format(time.RFC3339)
	}
	return result
}

// summaryCopilot returns Copilot usage summary.
func (h *Handler) summaryCopilot(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildCopilotSummaryMap())
}

// buildCopilotSummaryMap builds the Copilot summary response.
func (h *Handler) buildCopilotSummaryMap() map[string]interface{} {
	response := map[string]interface{}{}
	if h.copilotTracker != nil && h.store != nil {
		latest, err := h.store.QueryLatestCopilot()
		if err == nil && latest != nil {
			for _, q := range latest.Quotas {
				if summary, err := h.copilotTracker.UsageSummary(q.Name); err == nil && summary != nil {
					response[q.Name] = buildCopilotSummaryResponse(summary)
				}
			}
		}
	}
	return response
}

// buildCopilotSummaryResponse builds a summary response from CopilotTracker data.
func buildCopilotSummaryResponse(summary *tracker.CopilotSummary) map[string]interface{} {
	result := map[string]interface{}{
		"quotaName":        summary.QuotaName,
		"entitlement":      summary.Entitlement,
		"currentUsed":      summary.CurrentUsed,
		"currentRemaining": summary.CurrentRemaining,
		"usagePercent":     summary.UsagePercent,
		"unlimited":        summary.Unlimited,
		"currentRate":      summary.CurrentRate,
		"projectedUsage":   summary.ProjectedUsage,
		"completedCycles":  summary.CompletedCycles,
		"avgPerCycle":      summary.AvgPerCycle,
		"peakCycle":        summary.PeakCycle,
		"totalTracked":     summary.TotalTracked,
		"trackingSince":    nil,
	}
	if summary.ResetDate != nil {
		result["resetDate"] = summary.ResetDate.Format(time.RFC3339)
		result["timeUntilReset"] = formatDuration(summary.TimeUntilReset)
	}
	if !summary.TrackingSince.IsZero() {
		result["trackingSince"] = summary.TrackingSince.Format(time.RFC3339)
	}
	return result
}

// insightsCopilot returns Copilot deep analytics.
func (h *Handler) insightsCopilot(w http.ResponseWriter, r *http.Request, rangeDur time.Duration) {
	hidden := h.getHiddenInsightKeys()
	respondJSON(w, http.StatusOK, h.buildCopilotInsights(hidden, rangeDur))
}

// buildCopilotInsights builds the Copilot insights response.
func (h *Handler) buildCopilotInsights(hidden map[string]bool, rangeDur time.Duration) insightsResponse {
	resp := insightsResponse{Stats: []insightStat{}, Insights: []insightItem{}}
	if h.store == nil {
		return resp
	}
	latest, err := h.store.QueryLatestCopilot()
	if err != nil || latest == nil {
		resp.Insights = append(resp.Insights, insightItem{
			Type: "info", Severity: "info",
			Title: "Getting Started",
			Desc:  "Keep onWatch running to collect Copilot usage data. Insights will appear after a few snapshots.",
		})
		return resp
	}

	// Collect summaries for all quotas
	quotaNames, _ := h.store.QueryAllCopilotQuotaNames()
	summaries := map[string]*tracker.CopilotSummary{}
	if h.copilotTracker != nil {
		for _, name := range quotaNames {
			if s, err := h.copilotTracker.UsageSummary(name); err == nil && s != nil {
				summaries[name] = s
			}
		}
	}

	// ═══ Stats Cards ═══
	for _, q := range latest.Quotas {
		if q.Unlimited {
			resp.Stats = append(resp.Stats, insightStat{
				Value: "∞",
				Label: api.CopilotDisplayName(q.Name),
			})
			continue
		}
		usagePercent := 0.0
		if q.Entitlement > 0 {
			usagePercent = float64(q.Entitlement-q.Remaining) / float64(q.Entitlement) * 100
		}
		resp.Stats = append(resp.Stats, insightStat{
			Value:    fmt.Sprintf("%.0f%%", usagePercent),
			Label:    api.CopilotDisplayName(q.Name),
			Sublabel: fmt.Sprintf("%d / %d used", q.Entitlement-q.Remaining, q.Entitlement),
		})
	}

	// ═══ Deep Insights ═══

	// 1. Burn Rate & Forecast per non-unlimited quota
	for _, q := range latest.Quotas {
		if q.Unlimited || q.Entitlement == 0 {
			continue
		}
		key := fmt.Sprintf("forecast_%s", q.Name)
		if hidden[key] {
			continue
		}
		s := summaries[q.Name]
		usagePercent := float64(q.Entitlement-q.Remaining) / float64(q.Entitlement) * 100

		if s != nil && s.CurrentRate > 0 {
			resp.Insights = append(resp.Insights, insightItem{
				Key: key, Type: "forecast", Severity: copilotInsightSeverity(usagePercent),
				Title:  fmt.Sprintf("%s Burn Rate", api.CopilotDisplayName(q.Name)),
				Metric: fmt.Sprintf("%.1f / hr", s.CurrentRate),
				Desc:   fmt.Sprintf("Currently at %.0f%% usage (%d/%d). At this rate, projected to use %d by reset.", usagePercent, q.Entitlement-q.Remaining, q.Entitlement, s.ProjectedUsage),
			})
		} else {
			resp.Insights = append(resp.Insights, insightItem{
				Key: key, Type: "current", Severity: copilotInsightSeverity(usagePercent),
				Title:  fmt.Sprintf("%s Usage", api.CopilotDisplayName(q.Name)),
				Metric: fmt.Sprintf("%.0f%%", usagePercent),
				Desc:   fmt.Sprintf("%d of %d used. Need more data to estimate burn rate.", q.Entitlement-q.Remaining, q.Entitlement),
			})
		}
	}

	// 2. Reset countdown
	if !hidden["reset_countdown"] && latest.ResetDate != nil {
		timeLeft := time.Until(*latest.ResetDate)
		if timeLeft > 0 {
			resp.Insights = append(resp.Insights, insightItem{
				Key: "reset_countdown", Type: "info", Severity: "info",
				Title:  "Quota Reset",
				Metric: formatDuration(timeLeft),
				Desc:   fmt.Sprintf("Quotas reset on %s.", latest.ResetDate.Format("Jan 2, 2006")),
			})
		}
	}

	// 3. Coverage — how long we've been tracking
	if !hidden["coverage"] {
		snapCount := 0
		since := time.Now().Add(-rangeDur)
		if points, err := h.store.QueryCopilotUsageSeries("premium_interactions", since); err == nil {
			snapCount = len(points)
		}
		if snapCount > 0 {
			resp.Insights = append(resp.Insights, insightItem{
				Key: "coverage", Type: "info", Severity: "info",
				Title:  "Data Coverage",
				Metric: fmt.Sprintf("%d snapshots", snapCount),
				Desc:   fmt.Sprintf("Tracking Copilot usage with %d data points in selected range.", snapCount),
			})
		}
	}

	return resp
}

// copilotInsightSeverity returns an insight severity based on usage percentage.
func copilotInsightSeverity(usagePercent float64) string {
	switch {
	case usagePercent >= 90:
		return "critical"
	case usagePercent >= 70:
		return "warning"
	default:
		return "info"
	}
}

// codexInsightSeverity returns an insight severity based on usage percentage for Codex.
// Uses the same thresholds as codexUtilStatus for consistency.
func codexInsightSeverity(util float64) string {
	return codexUtilStatus(util)
}

// ── Codex Handlers ──

func (h *Handler) currentCodex(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildCodexCurrent())
}

func (h *Handler) buildCodexCurrent() map[string]interface{} {
	now := time.Now().UTC()
	response := map[string]interface{}{
		"capturedAt": now.Format(time.RFC3339),
		"quotas":     []interface{}{},
	}
	if h.store == nil {
		return response
	}

	latest, err := h.store.QueryLatestCodex()
	if err != nil {
		h.logger.Error("failed to query latest Codex snapshot", "error", err)
		return response
	}
	if latest == nil {
		return response
	}

	response["capturedAt"] = latest.CapturedAt.Format(time.RFC3339)
	if latest.PlanType != "" {
		response["planType"] = latest.PlanType
	}

	quotas := make([]map[string]interface{}, 0, len(latest.Quotas))
	for _, q := range latest.Quotas {
		qMap := map[string]interface{}{
			"name":        q.Name,
			"displayName": api.CodexDisplayName(q.Name),
			"utilization": q.Utilization,
			"status":      codexUtilStatus(q.Utilization),
		}
		if q.ResetsAt != nil {
			timeUntilReset := time.Until(*q.ResetsAt)
			qMap["resetsAt"] = q.ResetsAt.Format(time.RFC3339)
			qMap["timeUntilReset"] = formatDuration(timeUntilReset)
			qMap["timeUntilResetSeconds"] = int64(timeUntilReset.Seconds())
		}
		if h.codexTracker != nil {
			if summary, err := h.codexTracker.UsageSummary(q.Name); err == nil && summary != nil {
				qMap["currentRate"] = summary.CurrentRate
				qMap["projectedUtil"] = summary.ProjectedUtil
			}
		}
		quotas = append(quotas, qMap)
	}
	response["quotas"] = quotas
	return response
}

func codexUtilStatus(util float64) string {
	switch {
	case util >= 95:
		return "critical"
	case util >= 80:
		return "danger"
	case util >= 50:
		return "warning"
	default:
		return "healthy"
	}
}

func (h *Handler) historyCodex(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}
	duration, err := parseTimeRange(r.URL.Query().Get("range"))
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	end := time.Now().UTC()
	start := end.Add(-duration)
	snapshots, err := h.store.QueryCodexRange(start, end)
	if err != nil {
		h.logger.Error("failed to query Codex history", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query history")
		return
	}
	step := downsampleStep(len(snapshots), maxChartPoints)
	last := len(snapshots) - 1
	response := make([]map[string]interface{}, 0, min(len(snapshots), maxChartPoints))
	for i, snap := range snapshots {
		if step > 1 && i != 0 && i != last && i%step != 0 {
			continue
		}
		entry := map[string]interface{}{"capturedAt": snap.CapturedAt.Format(time.RFC3339)}
		for _, q := range snap.Quotas {
			entry[q.Name] = q.Utilization
		}
		response = append(response, entry)
	}
	respondJSON(w, http.StatusOK, response)
}

func (h *Handler) cyclesCodex(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	quotaName := r.URL.Query().Get("type")
	if quotaName == "" {
		quotaName = "five_hour"
	}

	validTypes := map[string]bool{
		"five_hour": true,
		"seven_day": true,
	}
	if !validTypes[quotaName] {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid quota type: %s", quotaName))
		return
	}

	response := []map[string]interface{}{}

	active, err := h.store.QueryActiveCodexCycle(quotaName)
	if err != nil {
		h.logger.Error("failed to query active Codex cycle", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycles")
		return
	}
	if active != nil {
		response = append(response, codexCycleToMap(active))
	}

	history, err := h.store.QueryCodexCycleHistory(quotaName, 200)
	if err != nil {
		h.logger.Error("failed to query Codex cycle history", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycles")
		return
	}
	for _, cycle := range history {
		response = append(response, codexCycleToMap(cycle))
	}

	respondJSON(w, http.StatusOK, response)
}

func codexCycleToMap(cycle *store.CodexResetCycle) map[string]interface{} {
	result := map[string]interface{}{
		"id":              cycle.ID,
		"quotaName":       cycle.QuotaName,
		"cycleStart":      cycle.CycleStart.Format(time.RFC3339),
		"cycleEnd":        nil,
		"peakUtilization": cycle.PeakUtilization,
		"totalDelta":      cycle.TotalDelta,
	}
	if cycle.CycleEnd != nil {
		result["cycleEnd"] = cycle.CycleEnd.Format(time.RFC3339)
	}
	if cycle.ResetsAt != nil {
		result["resetsAt"] = cycle.ResetsAt.Format(time.RFC3339)
	}
	return result
}

func (h *Handler) summaryCodex(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildCodexSummaryMap())
}

func (h *Handler) buildCodexSummaryMap() map[string]interface{} {
	response := map[string]interface{}{}
	if h.codexTracker == nil || h.store == nil {
		return response
	}
	latest, err := h.store.QueryLatestCodex()
	if err != nil || latest == nil {
		return response
	}
	for _, q := range latest.Quotas {
		if summary, err := h.codexTracker.UsageSummary(q.Name); err == nil && summary != nil {
			response[q.Name] = buildCodexSummaryResponse(summary)
		}
	}
	return response
}

func buildCodexSummaryResponse(summary *tracker.CodexSummary) map[string]interface{} {
	result := map[string]interface{}{
		"quotaName":       summary.QuotaName,
		"currentUtil":     summary.CurrentUtil,
		"currentRate":     summary.CurrentRate,
		"projectedUtil":   summary.ProjectedUtil,
		"completedCycles": summary.CompletedCycles,
		"avgPerCycle":     summary.AvgPerCycle,
		"peakCycle":       summary.PeakCycle,
		"totalTracked":    summary.TotalTracked,
		"trackingSince":   nil,
	}
	if summary.ResetsAt != nil {
		result["resetsAt"] = summary.ResetsAt.Format(time.RFC3339)
		result["timeUntilReset"] = formatDuration(summary.TimeUntilReset)
	}
	if !summary.TrackingSince.IsZero() {
		result["trackingSince"] = summary.TrackingSince.Format(time.RFC3339)
	}
	return result
}

func (h *Handler) insightsCodex(w http.ResponseWriter, r *http.Request, rangeDur time.Duration) {
	hidden := h.getHiddenInsightKeys()
	respondJSON(w, http.StatusOK, h.buildCodexInsights(hidden, rangeDur))
}

func (h *Handler) buildCodexInsights(hidden map[string]bool, rangeDur time.Duration) insightsResponse {
	resp := insightsResponse{Stats: []insightStat{}, Insights: []insightItem{}}
	if h.store == nil {
		return resp
	}
	latest, err := h.store.QueryLatestCodex()
	if err != nil || latest == nil {
		resp.Insights = append(resp.Insights, insightItem{Type: "info", Severity: "info", Title: "Getting Started", Desc: "Keep onWatch running to collect Codex usage data. Insights will appear after a few snapshots."})
		return resp
	}
	quotaNames, _ := h.store.QueryAllCodexQuotaNames()
	summaries := map[string]*tracker.CodexSummary{}
	if h.codexTracker != nil {
		for _, name := range quotaNames {
			if s, err := h.codexTracker.UsageSummary(name); err == nil && s != nil {
				summaries[name] = s
			}
		}
	}
	for _, q := range latest.Quotas {
		resp.Stats = append(resp.Stats, insightStat{Value: fmt.Sprintf("%.0f%%", q.Utilization), Label: api.CodexDisplayName(q.Name)})
		key := fmt.Sprintf("forecast_%s", q.Name)
		if hidden[key] {
			continue
		}
		s := summaries[q.Name]
		if s != nil && s.CurrentRate > 0 {
			resp.Insights = append(resp.Insights, insightItem{Key: key, Type: "forecast", Severity: codexInsightSeverity(q.Utilization), Title: fmt.Sprintf("%s Burn Rate", api.CodexDisplayName(q.Name)), Metric: fmt.Sprintf("%.1f%%/hr", s.CurrentRate), Desc: fmt.Sprintf("Currently at %.0f%%. At this rate, projected %.0f%% by reset.", q.Utilization, s.ProjectedUtil)})
		} else {
			resp.Insights = append(resp.Insights, insightItem{Key: key, Type: "current", Severity: codexInsightSeverity(q.Utilization), Title: fmt.Sprintf("%s Usage", api.CodexDisplayName(q.Name)), Metric: fmt.Sprintf("%.0f%%", q.Utilization), Desc: "Need more data to estimate burn rate."})
		}
	}
	if !hidden["coverage"] {
		snapCount := 0
		since := time.Now().Add(-rangeDur)
		if points, err := h.store.QueryCodexUtilizationSeries("five_hour", since); err == nil {
			snapCount = len(points)
		}
		if snapCount > 0 {
			resp.Insights = append(resp.Insights, insightItem{Key: "coverage", Type: "info", Severity: "info", Title: "Data Coverage", Metric: fmt.Sprintf("%d snapshots", snapCount), Desc: fmt.Sprintf("Tracking Codex usage with %d data points in selected range.", snapCount)})
		}
	}
	return resp
}

func (h *Handler) cycleOverviewCodex(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"cycles": []interface{}{}})
		return
	}
	groupBy := r.URL.Query().Get("groupBy")
	if groupBy == "" {
		groupBy = "five_hour"
	}
	rows, err := h.store.QueryCodexCycleOverview(groupBy, parseCycleOverviewLimit(r))
	if err != nil {
		h.logger.Error("failed to query Codex cycle overview", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycle overview")
		return
	}
	quotaNames := []string{}
	for _, row := range rows {
		if len(row.CrossQuotas) > 0 {
			for _, cq := range row.CrossQuotas {
				quotaNames = append(quotaNames, cq.Name)
			}
			break
		}
	}
	if len(quotaNames) == 0 {
		quotaNames = []string{"five_hour", "seven_day"}
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"groupBy":    groupBy,
		"provider":   "codex",
		"quotaNames": quotaNames,
		"cycles":     cycleOverviewRowsToJSON(rows),
	})
}

// cycleOverviewCopilot returns Copilot cycle overview with cross-quota data.
func (h *Handler) cycleOverviewCopilot(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"cycles": []interface{}{}})
		return
	}

	groupBy := r.URL.Query().Get("groupBy")
	if groupBy == "" {
		groupBy = "premium_interactions"
	}

	limit := parseCycleOverviewLimit(r)
	rows, err := h.store.QueryCopilotCycleOverview(groupBy, limit)
	if err != nil {
		h.logger.Error("failed to query Copilot cycle overview", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycle overview")
		return
	}

	// Determine quota names from first row with cross-quota data, or default
	quotaNames := []string{}
	for _, row := range rows {
		if len(row.CrossQuotas) > 0 {
			for _, cq := range row.CrossQuotas {
				quotaNames = append(quotaNames, cq.Name)
			}
			break
		}
	}
	if len(quotaNames) == 0 {
		quotaNames = []string{"premium_interactions", "chat", "completions"}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"groupBy":    groupBy,
		"provider":   "copilot",
		"quotaNames": quotaNames,
		"cycles":     cycleOverviewRowsToJSON(rows),
	})
}
