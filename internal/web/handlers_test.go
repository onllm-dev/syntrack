package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/syntrack/internal/api"
	"github.com/onllm-dev/syntrack/internal/store"
	"github.com/onllm-dev/syntrack/internal/tracker"
)

func TestHandler_Dashboard_ReturnsHTML(t *testing.T) {
	h := NewHandler(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	h.Dashboard(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("expected Content-Type text/html, got %s", contentType)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("expected HTML document in response")
	}
	if !strings.Contains(body, "SynTrack") {
		t.Error("expected 'SynTrack' in response body")
	}
}

func TestHandler_Current_ReturnsJSON(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 154.3, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 7635, RenewsAt: time.Now().Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	tr := tracker.New(s, nil)
	h := NewHandler(s, tr, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/current", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", contentType)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	for _, field := range []string{"capturedAt", "subscription", "search", "toolCalls"} {
		if _, ok := response[field]; !ok {
			t.Errorf("expected %s field", field)
		}
	}
}

func TestHandler_Current_IncludesResetCountdown(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 154.3, RenewsAt: time.Now().Add(4*time.Hour + 16*time.Minute)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: time.Now().Add(58 * time.Minute)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 7635, RenewsAt: time.Now().Add(2 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	tr := tracker.New(s, nil)
	h := NewHandler(s, tr, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/current", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	var response map[string]map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	for _, quotaType := range []string{"subscription", "search", "toolCalls"} {
		quota, ok := response[quotaType]
		if !ok {
			t.Errorf("missing %s quota", quotaType)
			continue
		}

		if _, ok := quota["renewsAt"]; !ok {
			t.Errorf("%s missing renewsAt", quotaType)
		}
		if _, ok := quota["timeUntilReset"]; !ok {
			t.Errorf("%s missing timeUntilReset", quotaType)
		}
		if _, ok := quota["timeUntilResetSeconds"]; !ok {
			t.Errorf("%s missing timeUntilResetSeconds", quotaType)
		}
	}
}

func TestHandler_Current_IncludesToolCallReset(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	subRenewsAt := time.Date(2026, 2, 6, 16, 16, 18, 0, time.UTC)
	toolRenewsAt := time.Date(2026, 2, 6, 15, 26, 41, 0, time.UTC)

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 154.3, RenewsAt: subRenewsAt},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 7635, RenewsAt: toolRenewsAt},
	}
	s.InsertSnapshot(snapshot)

	tr := tracker.New(s, nil)
	h := NewHandler(s, tr, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/current", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	var response map[string]map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	toolCalls := response["toolCalls"]
	if toolCalls == nil {
		t.Fatal("missing toolCalls in response")
	}

	renewsAt, ok := toolCalls["renewsAt"].(string)
	if !ok {
		t.Fatal("toolCalls renewsAt not a string")
	}

	if !strings.Contains(renewsAt, "2026-02-06T15:26:41") {
		t.Errorf("toolCalls renewsAt = %s, expected 2026-02-06T15:26:41", renewsAt)
	}
}

func TestHandler_Current_EmptyDB(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	tr := tracker.New(s, nil)
	h := NewHandler(s, tr, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/current", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200 for empty DB, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if _, ok := response["capturedAt"]; !ok {
		t.Error("expected capturedAt field even with empty DB")
	}
	if _, ok := response["subscription"]; !ok {
		t.Error("expected subscription field even with empty DB")
	}
}

func TestHandler_History_DefaultRange(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	baseTime := time.Now().UTC().Add(-5 * time.Hour)
	for i := 0; i < 10; i++ {
		snapshot := &api.Snapshot{
			CapturedAt: baseTime.Add(time.Duration(i) * time.Minute),
			Sub:        api.QuotaInfo{Limit: 1350, Requests: float64(i * 10), RenewsAt: time.Now().Add(5 * time.Hour)},
			Search:     api.QuotaInfo{Limit: 250, Requests: float64(i), RenewsAt: time.Now().Add(1 * time.Hour)},
			ToolCall:   api.QuotaInfo{Limit: 16200, Requests: float64(i * 5), RenewsAt: time.Now().Add(3 * time.Hour)},
		}
		s.InsertSnapshot(snapshot)
	}

	h := NewHandler(s, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(response) == 0 {
		t.Error("expected history data with default 6h range")
	}
}

func TestHandler_History_AllRanges(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 100, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 500, RenewsAt: time.Now().Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	h := NewHandler(s, nil, nil)

	ranges := []string{"1h", "6h", "24h", "7d", "30d"}
	for _, r := range ranges {
		t.Run(r, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/history?range="+r, nil)
			rr := httptest.NewRecorder()
			h.History(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("range %s: expected status 200, got %d", r, rr.Code)
			}
		})
	}
}

func TestHandler_History_InvalidRange(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	h := NewHandler(s, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/history?range=invalid", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}

	var response map[string]string
	json.Unmarshal(rr.Body.Bytes(), &response)

	if _, ok := response["error"]; !ok {
		t.Error("expected error field in response")
	}
}

func TestHandler_History_ReturnsPercentages(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 500, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 125, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 2000, Requests: 1000, RenewsAt: time.Now().Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	h := NewHandler(s, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/history?range=6h", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	var response []map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	if len(response) == 0 {
		t.Fatal("expected history data")
	}

	point := response[0]

	for _, field := range []string{"subscriptionPercent", "searchPercent", "toolCallsPercent"} {
		if _, ok := point[field]; !ok {
			t.Errorf("expected %s field", field)
		}
	}

	if subPct, ok := point["subscriptionPercent"].(float64); ok {
		if subPct != 50.0 {
			t.Errorf("subscriptionPercent = %v, want 50.0", subPct)
		}
	}
}

func TestHandler_Cycles_FilterByType(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	now := time.Now().UTC()
	s.CreateCycle("subscription", now, now.Add(5*time.Hour))
	s.CreateCycle("search", now, now.Add(1*time.Hour))
	s.CreateCycle("toolcall", now, now.Add(3*time.Hour))

	h := NewHandler(s, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/cycles?type=subscription", nil)
	rr := httptest.NewRecorder()
	h.Cycles(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	for _, cycle := range response {
		if cycle["quotaType"] != "subscription" {
			t.Errorf("expected only subscription cycles, got %v", cycle["quotaType"])
		}
	}
}

func TestHandler_Cycles_AllTypes(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	now := time.Now().UTC()
	s.CreateCycle("subscription", now, now.Add(5*time.Hour))
	s.CreateCycle("search", now, now.Add(1*time.Hour))
	s.CreateCycle("toolcall", now, now.Add(3*time.Hour))

	h := NewHandler(s, nil, nil)

	types := []string{"subscription", "search", "toolcall"}
	for _, quotaType := range types {
		t.Run(quotaType, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/cycles?type="+quotaType, nil)
			rr := httptest.NewRecorder()
			h.Cycles(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("type %s: expected status 200, got %d", quotaType, rr.Code)
			}
		})
	}
}

func TestHandler_Cycles_InvalidType(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	h := NewHandler(s, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/cycles?type=invalid", nil)
	rr := httptest.NewRecorder()
	h.Cycles(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}
}

func TestHandler_Cycles_IncludesActiveCycle(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	now := time.Now().UTC()
	s.CreateCycle("subscription", now, now.Add(5*time.Hour))

	h := NewHandler(s, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/cycles?type=subscription", nil)
	rr := httptest.NewRecorder()
	h.Cycles(rr, req)

	var response []map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	if len(response) == 0 {
		t.Fatal("expected at least one cycle")
	}

	cycle := response[0]
	if cycle["cycleEnd"] != nil {
		t.Error("active cycle should have nil cycleEnd")
	}
}

func TestHandler_Summary_AllThreeQuotas(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 154.3, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 0, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 7635, RenewsAt: time.Now().Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	tr := tracker.New(s, nil)
	h := NewHandler(s, tr, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	rr := httptest.NewRecorder()
	h.Summary(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	for _, quotaType := range []string{"subscription", "search", "toolCalls"} {
		if _, ok := response[quotaType]; !ok {
			t.Errorf("expected %s in summary", quotaType)
		}
	}
}

func TestHandler_Summary_IncludesProjectedUsage(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 500, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 50, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 2000, Requests: 500, RenewsAt: time.Now().Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	tr := tracker.New(s, nil)
	h := NewHandler(s, tr, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	rr := httptest.NewRecorder()
	h.Summary(rr, req)

	var response map[string]map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	sub := response["subscription"]
	if sub == nil {
		t.Fatal("missing subscription summary")
	}

	if _, ok := sub["projectedUsage"]; !ok {
		t.Error("expected projectedUsage field")
	}
}

func TestHandler_Sessions_ReturnsList(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	s.CreateSession("session-1", time.Now().Add(-2*time.Hour), 60)
	s.CreateSession("session-2", time.Now().Add(-1*time.Hour), 60)

	h := NewHandler(s, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	h.Sessions(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(response) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(response))
	}
}

func TestHandler_Sessions_IncludesMaxRequests(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	s.CreateSession("session-1", time.Now(), 60)
	s.UpdateSessionMaxRequests("session-1", 100, 20, 50)

	h := NewHandler(s, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	h.Sessions(rr, req)

	var response []map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	if len(response) == 0 {
		t.Fatal("expected at least one session")
	}

	session := response[0]

	for _, field := range []string{"maxSubRequests", "maxSearchRequests", "maxToolRequests"} {
		if _, ok := session[field]; !ok {
			t.Errorf("expected %s field", field)
		}
	}
}

func TestHandler_Sessions_IncludesActiveSession(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	s.CreateSession("active-session", time.Now(), 60)
	s.CreateSession("closed-session", time.Now().Add(-2*time.Hour), 60)
	s.CloseSession("closed-session", time.Now().Add(-1*time.Hour))

	h := NewHandler(s, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	h.Sessions(rr, req)

	var response []map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	if len(response) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(response))
	}

	var foundActive bool
	for _, session := range response {
		if session["id"] == "active-session" {
			foundActive = true
			if session["endedAt"] != nil {
				t.Error("active session should have nil endedAt")
			}
		}
	}

	if !foundActive {
		t.Error("expected to find active session")
	}
}

func TestHandler_Sessions_EmptyDB(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	h := NewHandler(s, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	h.Sessions(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if response == nil {
		t.Error("expected empty array, not null")
	}

	if len(response) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(response))
	}
}

func TestHandler_respondJSON(t *testing.T) {
	type TestData struct {
		Message string `json:"message"`
		Count   int    `json:"count"`
	}

	rr := httptest.NewRecorder()
	data := TestData{Message: "test", Count: 42}
	respondJSON(rr, http.StatusCreated, data)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", contentType)
	}

	var response TestData
	json.Unmarshal(rr.Body.Bytes(), &response)

	if response.Message != "test" || response.Count != 42 {
		t.Error("JSON response mismatch")
	}
}

func TestHandler_respondError(t *testing.T) {
	rr := httptest.NewRecorder()
	respondError(rr, http.StatusBadRequest, "invalid input")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}

	var response map[string]string
	json.Unmarshal(rr.Body.Bytes(), &response)

	if response["error"] != "invalid input" {
		t.Errorf("expected error 'invalid input', got %s", response["error"])
	}
}

func TestHandler_parseTimeRange(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		wantErr  bool
	}{
		{"1h", time.Hour, false},
		{"6h", 6 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"invalid", 0, true},
		{"", 6 * time.Hour, false}, // default
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			duration, err := parseTimeRange(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseTimeRange(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && duration != tt.expected {
				t.Errorf("parseTimeRange(%q) = %v, want %v", tt.input, duration, tt.expected)
			}
		})
	}
}
