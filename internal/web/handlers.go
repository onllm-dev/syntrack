package web

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/onllm-dev/syntrack/internal/api"
	"github.com/onllm-dev/syntrack/internal/store"
	"github.com/onllm-dev/syntrack/internal/tracker"
)

// Handler handles HTTP requests for the web dashboard
type Handler struct {
	store   *store.Store
	tracker *tracker.Tracker
	logger  *slog.Logger
}

// NewHandler creates a new Handler instance
func NewHandler(store *store.Store, tracker *tracker.Tracker, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		store:   store,
		tracker: tracker,
		logger:  logger,
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

// Dashboard renders the main dashboard page
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	tmpl := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Dashboard - SynTrack</title>
    <link rel="stylesheet" href="/static/style.css">
</head>
<body>
    <header>
        <h1>SynTrack</h1>
    </header>
    <main>
        <div class="dashboard">
            <h2>Dashboard</h2>
            <p>Synthetic API Usage Tracker</p>
        </div>
    </main>
    <script src="/static/app.js"></script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(tmpl))
}

// Current returns current quota status (API endpoint)
func (h *Handler) Current(w http.ResponseWriter, r *http.Request) {
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
			respondJSON(w, http.StatusOK, response)
			return
		}

		if latest != nil {
			response["capturedAt"] = latest.CapturedAt.Format(time.RFC3339)
			response["subscription"] = buildQuotaResponse("Subscription", "Main API request quota for your plan", latest.Sub, h.tracker, "subscription")
			response["search"] = buildQuotaResponse("Search (Hourly)", "Search endpoint calls, resets every hour", latest.Search, h.tracker, "search")
			response["toolCalls"] = buildQuotaResponse("Tool Call Discounts", "Discounted tool call requests", latest.ToolCall, h.tracker, "toolcall")
		}
	}

	respondJSON(w, http.StatusOK, response)
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
		status = "warning"
	} else if percent >= 50 {
		status = "caution"
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

// Cycles returns reset cycle data (API endpoint)
func (h *Handler) Cycles(w http.ResponseWriter, r *http.Request) {
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
	var response []map[string]interface{}

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

// Summary returns usage summary (API endpoint)
func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
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

// Sessions returns session data (API endpoint)
func (h *Handler) Sessions(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	sessions, err := h.store.QuerySessionHistory()
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
