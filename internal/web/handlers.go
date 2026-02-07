package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/onllm-dev/syntrack/internal/api"
	"github.com/onllm-dev/syntrack/internal/config"
	"github.com/onllm-dev/syntrack/internal/store"
	"github.com/onllm-dev/syntrack/internal/tracker"
)

// Handler handles HTTP requests for the web dashboard
type Handler struct {
	store         *store.Store
	tracker       *tracker.Tracker
	zaiTracker    *tracker.ZaiTracker
	logger        *slog.Logger
	dashboardTmpl *template.Template
	loginTmpl     *template.Template
	sessions      *SessionStore
	config        *config.Config
	version       string
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

	h := &Handler{
		store:         store,
		tracker:       tracker,
		logger:        logger,
		dashboardTmpl: dashboardTmpl,
		loginTmpl:     loginTmpl,
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

// parseTimeRange parses a time range string (1h, 6h, 24h, 7d, 30d)
func parseTimeRange(rangeStr string) (time.Duration, error) {
	if rangeStr == "" {
		return 6 * time.Hour, nil
	}

	switch rangeStr {
	case "1h":
		return time.Hour, nil
	case "6h":
		return 6 * time.Hour, nil
	case "24h":
		return 24 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	case "30d":
		return 30 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("invalid range: %s", rangeStr)
	}
}

// formatDuration formats a duration as a human-readable string (e.g., "4h 16m")
func formatDuration(d time.Duration) string {
	if d < 0 {
		return "Resetting..."
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60

	if hours > 0 && minutes > 0 {
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

	// "both" is a virtual provider — allowed when both are configured
	if provider == "both" {
		if h.config.HasBothProviders() {
			return "both", nil
		}
		return "", fmt.Errorf("'both' requires both Synthetic and Z.ai to be configured")
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
	if h.config.HasBothProviders() {
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
		// Add "both" option when both providers are available
		if h.config.HasBothProviders() {
			providers = append(providers, "both")
		}
		if len(providers) > 0 {
			currentProvider = providers[0]
		}
		// Allow overriding via query param
		if reqProvider := r.URL.Query().Get("provider"); reqProvider != "" {
			reqProvider = strings.ToLower(reqProvider)
			if h.config.HasProvider(reqProvider) || (reqProvider == "both" && h.config.HasBothProviders()) {
				currentProvider = reqProvider
			}
		}
	}

	data := map[string]interface{}{
		"Title":           "Dashboard",
		"Providers":       providers,
		"CurrentProvider": currentProvider,
		"Version":         h.version,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
	default:
		respondError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider: %s", provider))
	}
}

// currentBoth returns combined quota status for both providers.
func (h *Handler) currentBoth(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{}
	if h.config.HasProvider("synthetic") {
		response["synthetic"] = h.buildSyntheticCurrent()
	}
	if h.config.HasProvider("zai") {
		response["zai"] = h.buildZaiCurrent()
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
	budget := snapshot.TokensUsage       // API's "usage" = total budget
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
		snapshots, err := h.store.QueryRange(start, now, 200)
		if err == nil {
			var synData []map[string]interface{}
			for _, s := range snapshots {
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
		snapshots, err := h.store.QueryZaiRange(start, now, 200)
		if err == nil {
			var zaiData []map[string]interface{}
			for _, s := range snapshots {
				zaiData = append(zaiData, map[string]interface{}{
					"capturedAt":    s.CapturedAt.Format(time.RFC3339),
					"tokensLimit":   s.TokensUsage,
					"tokensUsage":   s.TokensCurrentValue,
					"tokensPercent": float64(s.TokensPercentage),
					"timeLimit":     s.TimeUsage,
					"timeUsage":     s.TimeCurrentValue,
					"timePercent":   float64(s.TimePercentage),
				})
			}
			response["zai"] = zaiData
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

	var response []map[string]interface{}
	for _, snapshot := range snapshots {
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

	var response []map[string]interface{}
	for _, snapshot := range snapshots {
		// Z.ai API: "usage" = budget, "currentValue" = actual usage, "percentage" = server %
		response = append(response, map[string]interface{}{
			"capturedAt":    snapshot.CapturedAt.Format(time.RFC3339),
			"tokensLimit":   snapshot.TokensUsage,        // budget
			"tokensUsage":   snapshot.TokensCurrentValue,  // actual usage
			"tokensPercent": float64(snapshot.TokensPercentage),
			"timeLimit":     snapshot.TimeUsage,           // budget
			"timeUsage":     snapshot.TimeCurrentValue,    // actual usage
			"timePercent":   float64(snapshot.TimePercentage),
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
	default:
		respondError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider: %s", provider))
	}
}

// cyclesBoth returns combined cycles from both providers.
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

	history, err := h.store.QueryCycleHistory(quotaType)
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

	history, err := h.store.QueryZaiCycleHistory(quotaType)
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
		"peakRequests": cycle.PeakValue,  // normalized to match Synthetic field name for frontend
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
	default:
		respondError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider: %s", provider))
	}
}

// summaryBoth returns combined summaries from both providers.
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
			"id":                session.ID,
			"startedAt":         session.StartedAt.Format(time.RFC3339),
			"endedAt":           nil,
			"pollInterval":      session.PollInterval,
			"maxSubRequests":    session.MaxSubRequests,
			"maxSearchRequests": session.MaxSearchRequests,
			"maxToolRequests":   session.MaxToolRequests,
			"snapshotCount":     session.SnapshotCount,
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
				"id":                s.ID,
				"startedAt":         s.StartedAt.Format(time.RFC3339),
				"endedAt":           nil,
				"pollInterval":      s.PollInterval,
				"maxSubRequests":    s.MaxSubRequests,
				"maxSearchRequests": s.MaxSearchRequests,
				"maxToolRequests":   s.MaxToolRequests,
				"snapshotCount":     s.SnapshotCount,
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

	respondJSON(w, http.StatusOK, response)
}

// ── Deep Insights ──

type insightStat struct {
	Value string `json:"value"`
	Label string `json:"label"`
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

	switch provider {
	case "both":
		h.insightsBoth(w, r)
	case "zai":
		h.insightsZai(w, r)
	case "synthetic":
		h.insightsSynthetic(w, r)
	default:
		respondError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider: %s", provider))
	}
}

// insightsBoth returns combined insights from both providers.
func (h *Handler) insightsBoth(w http.ResponseWriter, r *http.Request) {
	hidden := h.getHiddenInsightKeys()
	response := map[string]interface{}{}

	if h.config.HasProvider("synthetic") {
		response["synthetic"] = h.buildSyntheticInsights(hidden)
	}
	if h.config.HasProvider("zai") {
		response["zai"] = h.buildZaiInsights(hidden)
	}

	respondJSON(w, http.StatusOK, response)
}

// insightsSynthetic returns Synthetic deep analytics
func (h *Handler) insightsSynthetic(w http.ResponseWriter, r *http.Request) {
	hidden := h.getHiddenInsightKeys()
	respondJSON(w, http.StatusOK, h.buildSyntheticInsights(hidden))
}

// buildSyntheticInsights builds the Synthetic insights response.
func (h *Handler) buildSyntheticInsights(hidden map[string]bool) insightsResponse {
	resp := insightsResponse{Stats: []insightStat{}, Insights: []insightItem{}}

	if h.store == nil {
		return resp
	}

	now := time.Now().UTC()
	d30 := now.Add(-30 * 24 * time.Hour)
	d7 := now.Add(-7 * 24 * time.Hour)

	// Fetch cycle data for all quota types (last 30 days)
	subCycles, _ := h.store.QueryCyclesSince("subscription", d30)
	searchCycles, _ := h.store.QueryCyclesSince("search", d30)
	toolCycles, _ := h.store.QueryCyclesSince("toolcall", d30)

	sessions, _ := h.store.QuerySessionHistory()
	latest, _ := h.store.QueryLatest()

	var subLimit float64
	if latest != nil {
		subLimit = latest.Sub.Limit
	}

	// Aggregate totals using billing-period grouping (not raw mini-cycle deltas).
	// This correctly counts the initial baseline when tracking started mid-period.
	sub30 := cycleSumConsumption(subCycles)
	sub7 := cycleSumConsumptionSince(subCycles, d7)
	search30 := cycleSumConsumption(searchCycles)
	tool30 := cycleSumConsumption(toolCycles)
	total30 := sub30 + search30 + tool30

	subAvg := billingPeriodAvg(subCycles)
	subPeak := billingPeriodPeak(subCycles)

	// Tracking duration
	var trackingDays int
	if len(sessions) > 0 {
		earliest := sessions[len(sessions)-1].StartedAt
		trackingDays = int(now.Sub(earliest).Hours() / 24)
		if trackingDays == 0 && now.Sub(earliest).Hours() > 0 {
			trackingDays = 1
		}
	}

	// Session stats (last 10)
	recentN := min(len(sessions), 10)
	var avgSessionDurMin, avgSessionConsumption, peakSessionConsumption float64
	if recentN > 0 {
		var totalDur, totalCons float64
		for i := 0; i < recentN; i++ {
			s := sessions[i]
			end := now
			if s.EndedAt != nil {
				end = *s.EndedAt
			}
			totalDur += end.Sub(s.StartedAt).Minutes()
			c := s.MaxSubRequests + s.MaxSearchRequests + s.MaxToolRequests
			totalCons += c
			if c > peakSessionConsumption {
				peakSessionConsumption = c
			}
		}
		avgSessionDurMin = totalDur / float64(recentN)
		avgSessionConsumption = totalCons / float64(recentN)
	}

	// ═══ Stats Cards (quick KPI numbers — no duplicates with insights below) ═══
	resp.Stats = append(resp.Stats, insightStat{Value: compactNum(sub30), Label: "Requests (30d)"})
	resp.Stats = append(resp.Stats, insightStat{Value: compactNum(total30), Label: "Total API Calls (30d)"})
	resp.Stats = append(resp.Stats, insightStat{Value: compactNum(tool30), Label: "Tool Calls (30d)"})
	resp.Stats = append(resp.Stats, insightStat{Value: fmt.Sprintf("%d", len(sessions)), Label: "Sessions Tracked"})
	if trackingDays > 0 {
		resp.Stats = append(resp.Stats, insightStat{Value: fmt.Sprintf("%dd", trackingDays), Label: "Tracking Duration"})
	}

	// ═══ Deep Insights (interactive card format with metric + sublabel) ═══

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

	// 4. Peak vs Average Variance
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

	// 5. Session Avg
	if !hidden["session_avg"] && recentN > 0 {
		avgH := avgSessionDurMin / 60
		resp.Insights = append(resp.Insights, insightItem{
			Key:  "session_avg",
			Type: "session", Severity: "info",
			Title:    "Session Avg",
			Metric:   fmt.Sprintf("%.0f", avgSessionConsumption),
			Sublabel: fmt.Sprintf("req/session (%d)", recentN),
			Desc: fmt.Sprintf("Last %d sessions: avg duration %.1fh, avg consumption %.0f requests.%s", recentN, avgH, avgSessionConsumption, func() string {
				if peakSessionConsumption > avgSessionConsumption*1.5 && avgSessionConsumption > 0 {
					return fmt.Sprintf(" Peak: %.0f (%.1fx avg).", peakSessionConsumption, peakSessionConsumption/avgSessionConsumption)
				}
				return ""
			}()),
		})
	}

	// 6. Consumption Trend (needs at least 4 billing periods to be meaningful)
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
			Desc:  "Keep SynTrack running to build up usage data. Deep insights will appear after a few cycles.",
		})
	}

	return resp
}

// insightsZai returns Z.ai deep analytics with historical data
func (h *Handler) insightsZai(w http.ResponseWriter, r *http.Request) {
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
			Desc:  "Keep SynTrack running to collect Z.ai usage data. Insights appear after a few snapshots.",
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
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"timezone":        tz,
		"hidden_insights": hiddenInsights,
	})
}

// UpdateSettings updates settings from JSON body (partial updates supported).
func (h *Handler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
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

	respondJSON(w, http.StatusOK, result)
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

	data := map[string]interface{}{
		"Title": "Login",
		"Error": r.URL.Query().Get("error"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.loginTmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		h.logger.Error("failed to render login template", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *Handler) loginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=Invalid+request", http.StatusFound)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if h.sessions == nil {
		http.Redirect(w, r, "/login?error=Auth+not+configured", http.StatusFound)
		return
	}

	token, ok := h.sessions.Authenticate(username, password)
	if !ok {
		http.Redirect(w, r, "/login?error=Invalid+username+or+password", http.StatusFound)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   sessionMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
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

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	// Verify current password
	_, ok := h.sessions.Authenticate(h.sessions.username, req.CurrentPassword)
	if !ok {
		respondError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}

	// Hash and store new password
	newHash := HashPassword(req.NewPassword)
	if err := h.store.UpsertUser(h.sessions.username, newHash); err != nil {
		h.logger.Error("failed to update password in database", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to save new password")
		return
	}

	// Update in-memory hash
	h.sessions.UpdatePassword(newHash)

	// Invalidate all sessions (force re-login)
	h.sessions.InvalidateAll()

	respondJSON(w, http.StatusOK, map[string]string{"message": "password updated successfully"})
}
