package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
)

// currentMiniMax returns current MiniMax quota status.
func (h *Handler) currentMiniMax(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildMiniMaxCurrent())
}

// buildMiniMaxCurrent builds the MiniMax current quota response map.
func (h *Handler) buildMiniMaxCurrent() map[string]interface{} {
	now := time.Now().UTC()
	response := map[string]interface{}{
		"capturedAt": now.Format(time.RFC3339),
		"quotas":     []interface{}{},
	}

	if h.store == nil {
		return response
	}

	latest, err := h.store.QueryLatestMiniMax()
	if err != nil {
		h.logger.Error("failed to query latest MiniMax snapshot", "error", err)
		return response
	}
	if latest == nil {
		return response
	}

	response["capturedAt"] = latest.CapturedAt.Format(time.RFC3339)
	quotas := make([]map[string]interface{}, 0, len(latest.Models))
	for _, m := range latest.Models {
		qMap := map[string]interface{}{
			"name":                  m.ModelName,
			"displayName":           api.MiniMaxDisplayName(m.ModelName),
			"total":                 m.Total,
			"remain":                m.Remain,
			"used":                  m.Used,
			"usedPercent":           m.UsedPercent,
			"status":                minimaxUsageStatus(m.UsedPercent),
			"timeUntilReset":        formatDuration(m.TimeUntilReset),
			"timeUntilResetSeconds": int64(m.TimeUntilReset.Seconds()),
		}
		if m.ResetAt != nil {
			qMap["resetAt"] = m.ResetAt.Format(time.RFC3339)
		}
		if h.miniMaxTracker != nil {
			if summary, err := h.miniMaxTracker.UsageSummary(m.ModelName); err == nil && summary != nil {
				qMap["currentRate"] = summary.CurrentRate
				qMap["projectedUsage"] = summary.ProjectedUsage
			}
		}
		quotas = append(quotas, qMap)
	}
	response["quotas"] = quotas
	return response
}

// minimaxUsageStatus returns a status string based on usage percentage.
func minimaxUsageStatus(usagePercent float64) string {
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

// historyMiniMax returns MiniMax usage history.
func (h *Handler) historyMiniMax(w http.ResponseWriter, r *http.Request) {
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
	snapshots, err := h.store.QueryMiniMaxRange(start, now)
	if err != nil {
		h.logger.Error("failed to query MiniMax history", "error", err)
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
		for _, m := range snap.Models {
			if m.Total > 0 {
				entry[m.ModelName] = m.UsedPercent
			}
		}
		response = append(response, entry)
	}
	respondJSON(w, http.StatusOK, response)
}

// cyclesMiniMax returns MiniMax reset cycles for a model.
func (h *Handler) cyclesMiniMax(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	modelName := r.URL.Query().Get("type")
	if modelName == "" {
		modelName = "MiniMax-M2"
	}

	var response []map[string]interface{}
	if active, err := h.store.QueryActiveMiniMaxCycle(modelName); err == nil && active != nil {
		response = append(response, miniMaxCycleToMap(active))
	}
	if history, err := h.store.QueryMiniMaxCycleHistory(modelName, 200); err == nil {
		for _, c := range history {
			response = append(response, miniMaxCycleToMap(c))
		}
	}

	respondJSON(w, http.StatusOK, response)
}

func miniMaxCycleToMap(cycle *store.MiniMaxResetCycle) map[string]interface{} {
	result := map[string]interface{}{
		"id":         cycle.ID,
		"quotaName":  cycle.ModelName,
		"cycleStart": cycle.CycleStart.Format(time.RFC3339),
		"cycleEnd":   nil,
		"peakUsed":   cycle.PeakUsed,
		"totalDelta": cycle.TotalDelta,
	}
	if cycle.CycleEnd != nil {
		result["cycleEnd"] = cycle.CycleEnd.Format(time.RFC3339)
	}
	if cycle.ResetAt != nil {
		result["resetAt"] = cycle.ResetAt.Format(time.RFC3339)
	}
	return result
}

// summaryMiniMax returns MiniMax usage summary.
func (h *Handler) summaryMiniMax(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildMiniMaxSummaryMap())
}

// buildMiniMaxSummaryMap builds the MiniMax summary response.
func (h *Handler) buildMiniMaxSummaryMap() map[string]interface{} {
	response := map[string]interface{}{}
	if h.miniMaxTracker == nil || h.store == nil {
		return response
	}

	names, err := h.store.QueryAllMiniMaxModelNames()
	if err != nil {
		return response
	}
	for _, name := range names {
		if summary, err := h.miniMaxTracker.UsageSummary(name); err == nil && summary != nil {
			response[name] = buildMiniMaxSummaryResponse(summary)
		}
	}
	return response
}

func buildMiniMaxSummaryResponse(summary *tracker.MiniMaxSummary) map[string]interface{} {
	result := map[string]interface{}{
		"quotaName":       summary.ModelName,
		"total":           summary.Total,
		"currentRemain":   summary.CurrentRemain,
		"currentUsed":     summary.CurrentUsed,
		"usagePercent":    summary.UsagePercent,
		"currentRate":     summary.CurrentRate,
		"projectedUsage":  summary.ProjectedUsage,
		"completedCycles": summary.CompletedCycles,
		"avgPerCycle":     summary.AvgPerCycle,
		"peakCycle":       summary.PeakCycle,
		"totalTracked":    summary.TotalTracked,
		"trackingSince":   nil,
	}
	if summary.ResetAt != nil {
		result["resetAt"] = summary.ResetAt.Format(time.RFC3339)
		result["timeUntilReset"] = formatDuration(summary.TimeUntilReset)
	}
	if !summary.TrackingSince.IsZero() {
		result["trackingSince"] = summary.TrackingSince.Format(time.RFC3339)
	}
	return result
}

// insightsMiniMax returns MiniMax deep analytics.
func (h *Handler) insightsMiniMax(w http.ResponseWriter, r *http.Request, rangeDur time.Duration) {
	hidden := h.getHiddenInsightKeys()
	respondJSON(w, http.StatusOK, h.buildMiniMaxInsights(hidden, rangeDur))
}

// buildMiniMaxInsights builds the MiniMax insights response.
func (h *Handler) buildMiniMaxInsights(hidden map[string]bool, rangeDur time.Duration) insightsResponse {
	resp := insightsResponse{Stats: []insightStat{}, Insights: []insightItem{}}
	if h.store == nil {
		return resp
	}

	latest, err := h.store.QueryLatestMiniMax()
	if err != nil || latest == nil {
		resp.Insights = append(resp.Insights, insightItem{
			Type: "info", Severity: "info",
			Title: "Getting Started",
			Desc:  "Keep onWatch running to collect MiniMax usage data. Insights will appear after a few snapshots.",
		})
		return resp
	}

	names, _ := h.store.QueryAllMiniMaxModelNames()
	summaries := map[string]*tracker.MiniMaxSummary{}
	if h.miniMaxTracker != nil {
		for _, name := range names {
			if s, err := h.miniMaxTracker.UsageSummary(name); err == nil && s != nil {
				summaries[name] = s
			}
		}
	}

	for _, m := range latest.Models {
		resp.Stats = append(resp.Stats, insightStat{
			Value:    fmt.Sprintf("%.0f%%", m.UsedPercent),
			Label:    api.MiniMaxDisplayName(m.ModelName),
			Sublabel: fmt.Sprintf("%d / %d used", m.Used, m.Total),
		})
	}

	for _, m := range latest.Models {
		key := fmt.Sprintf("forecast_%s", m.ModelName)
		if hidden[key] {
			continue
		}
		s := summaries[m.ModelName]
		if s != nil && s.CurrentRate > 0 {
			resp.Insights = append(resp.Insights, insightItem{
				Key: key, Type: "forecast", Severity: miniMaxInsightSeverity(m.UsedPercent),
				Title:  fmt.Sprintf("%s Burn Rate", api.MiniMaxDisplayName(m.ModelName)),
				Metric: fmt.Sprintf("%.1f / hr", s.CurrentRate),
				Desc:   fmt.Sprintf("Currently at %.0f%% usage (%d/%d). At this rate, projected to use %d before reset.", m.UsedPercent, m.Used, m.Total, s.ProjectedUsage),
			})
		} else {
			resp.Insights = append(resp.Insights, insightItem{
				Key: key, Type: "current", Severity: miniMaxInsightSeverity(m.UsedPercent),
				Title:  fmt.Sprintf("%s Usage", api.MiniMaxDisplayName(m.ModelName)),
				Metric: fmt.Sprintf("%.0f%%", m.UsedPercent),
				Desc:   fmt.Sprintf("%d of %d used. Need more data to estimate burn rate.", m.Used, m.Total),
			})
		}
	}

	if !hidden["coverage"] {
		snapCount := 0
		since := time.Now().Add(-rangeDur)
		for _, name := range names {
			if points, err := h.store.QueryMiniMaxUsageSeries(name, since); err == nil && len(points) > snapCount {
				snapCount = len(points)
			}
		}
		if snapCount > 0 {
			resp.Insights = append(resp.Insights, insightItem{
				Key: "coverage", Type: "info", Severity: "info",
				Title:  "Data Coverage",
				Metric: fmt.Sprintf("%d snapshots", snapCount),
				Desc:   fmt.Sprintf("Tracking MiniMax usage with %d data points in selected range.", snapCount),
			})
		}
	}

	return resp
}

func miniMaxInsightSeverity(usagePercent float64) string {
	switch {
	case usagePercent >= 90:
		return "critical"
	case usagePercent >= 70:
		return "warning"
	default:
		return "info"
	}
}

// cycleOverviewMiniMax returns MiniMax cycle overview with cross-model data.
func (h *Handler) cycleOverviewMiniMax(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"cycles": []interface{}{}})
		return
	}
	groupBy := r.URL.Query().Get("groupBy")
	if groupBy == "" {
		groupBy = "MiniMax-M2"
	}
	limit := parseCycleOverviewLimit(r)
	rows, err := h.store.QueryMiniMaxCycleOverview(groupBy, limit)
	if err != nil {
		h.logger.Error("failed to query MiniMax cycle overview", "error", err)
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
		if names, err := h.store.QueryAllMiniMaxModelNames(); err == nil && len(names) > 0 {
			quotaNames = names
		} else {
			quotaNames = []string{"MiniMax-M2"}
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"groupBy":    groupBy,
		"provider":   "minimax",
		"quotaNames": quotaNames,
		"cycles":     cycleOverviewRowsToJSON(rows),
	})
}
