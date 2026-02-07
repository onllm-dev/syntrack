package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/syntrack/internal/api"
	"github.com/onllm-dev/syntrack/internal/config"
	"github.com/onllm-dev/syntrack/internal/store"
	"github.com/onllm-dev/syntrack/internal/tracker"
)

// Test helper functions for creating configurations
func createTestConfigWithSynthetic() *config.Config {
	return &config.Config{
		SyntheticAPIKey: "syn_test_key",
		PollInterval:    60 * time.Second,
		Port:            8932,
		AdminUser:       "admin",
		AdminPass:       "test",
		DBPath:          "./test.db",
	}
}

func createTestConfigWithZai() *config.Config {
	return &config.Config{
		ZaiAPIKey:    "zai_test_key",
		ZaiBaseURL:   "https://api.z.ai/api",
		PollInterval: 60 * time.Second,
		Port:         8932,
		AdminUser:    "admin",
		AdminPass:    "test",
		DBPath:       "./test.db",
	}
}

func createTestConfigWithBoth() *config.Config {
	return &config.Config{
		SyntheticAPIKey: "syn_test_key",
		ZaiAPIKey:       "zai_test_key",
		ZaiBaseURL:      "https://api.z.ai/api",
		PollInterval:    60 * time.Second,
		Port:            8932,
		AdminUser:       "admin",
		AdminPass:       "test",
		DBPath:          "./test.db",
	}
}

func TestHandler_Dashboard_ReturnsHTML(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil, nil)
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
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, tr, nil, nil, cfg)

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
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, tr, nil, nil, cfg)

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
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, tr, nil, nil, cfg)

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
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, tr, nil, nil, cfg)

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

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

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

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

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

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

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

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

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

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	now := time.Now().UTC()
	s.CreateCycle("subscription", now, now.Add(5*time.Hour))
	s.CreateCycle("search", now, now.Add(1*time.Hour))
	s.CreateCycle("toolcall", now, now.Add(3*time.Hour))

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

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	now := time.Now().UTC()
	s.CreateCycle("subscription", now, now.Add(5*time.Hour))
	s.CreateCycle("search", now, now.Add(1*time.Hour))
	s.CreateCycle("toolcall", now, now.Add(3*time.Hour))

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

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

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

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	now := time.Now().UTC()
	s.CreateCycle("subscription", now, now.Add(5*time.Hour))

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
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, tr, nil, nil, cfg)

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
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, tr, nil, nil, cfg)

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

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	s.CreateSession("session-1", time.Now().Add(-2*time.Hour), 60)
	s.CreateSession("session-2", time.Now().Add(-1*time.Hour), 60)

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

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	s.CreateSession("session-1", time.Now(), 60)
	s.UpdateSessionMaxRequests("session-1", 100, 20, 50)

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

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	s.CreateSession("active-session", time.Now(), 60)
	s.CreateSession("closed-session", time.Now().Add(-2*time.Hour), 60)
	s.CloseSession("closed-session", time.Now().Add(-1*time.Hour))

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

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

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
		{"", 6 * time.Hour, false},
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

// Provider Endpoint Tests

func TestHandler_Providers_ReturnsAvailableProviders(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	rr := httptest.NewRecorder()
	h.Providers(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	providers, ok := response["providers"].([]interface{})
	if !ok {
		t.Fatal("expected providers array")
	}
	if len(providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(providers))
	}
	if providers[0] != "synthetic" {
		t.Errorf("expected synthetic provider, got %v", providers[0])
	}

	if response["current"] != "synthetic" {
		t.Errorf("expected current provider to be synthetic, got %v", response["current"])
	}
}

func TestHandler_Providers_WithNoProviders(t *testing.T) {
	cfg := &config.Config{
		PollInterval: 60 * time.Second,
		Port:         8932,
		AdminUser:    "admin",
		AdminPass:    "test",
		DBPath:       "./test.db",
	}
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	rr := httptest.NewRecorder()
	h.Providers(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	providers, ok := response["providers"].([]interface{})
	if !ok || providers == nil {
		// Nil providers is acceptable for no providers
		return
	}
	if len(providers) != 0 {
		t.Errorf("expected 0 providers, got %d", len(providers))
	}
}

func TestHandler_Providers_WithBothProviders(t *testing.T) {
	cfg := createTestConfigWithBoth()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	rr := httptest.NewRecorder()
	h.Providers(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	providers, ok := response["providers"].([]interface{})
	if !ok {
		t.Fatal("expected providers array")
	}
	if len(providers) != 2 {
		t.Errorf("expected 2 providers, got %d", len(providers))
	}
}

// Synthetic Provider Tests

func TestHandler_Current_WithSyntheticProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 154.3, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 7635, RenewsAt: time.Now().Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	tr := tracker.New(s, nil)
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, tr, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=synthetic", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if _, ok := response["subscription"]; !ok {
		t.Error("expected subscription field")
	}
	if _, ok := response["search"]; !ok {
		t.Error("expected search field")
	}
	if _, ok := response["toolCalls"]; !ok {
		t.Error("expected toolCalls field")
	}
}

func TestHandler_History_WithSyntheticProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 100, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 500, RenewsAt: time.Now().Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/history?provider=synthetic&range=24h", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(response) != 1 {
		t.Errorf("expected 1 history entry, got %d", len(response))
	}
}

func TestHandler_Summary_WithSyntheticProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 154.3, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 7635, RenewsAt: time.Now().Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	tr := tracker.New(s, nil)
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, tr, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/summary?provider=synthetic", nil)
	rr := httptest.NewRecorder()
	h.Summary(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	for _, field := range []string{"subscription", "search", "toolCalls"} {
		if _, ok := response[field]; !ok {
			t.Errorf("expected %s field", field)
		}
	}
}

func TestHandler_Cycles_WithSyntheticProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	now := time.Now().UTC()
	s.CreateCycle("subscription", now, now.Add(5*time.Hour))

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycles?provider=synthetic&type=subscription", nil)
	rr := httptest.NewRecorder()
	h.Cycles(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(response) == 0 {
		t.Fatal("expected at least one cycle")
	}

	if response[0]["quotaType"] != "subscription" {
		t.Errorf("expected quotaType to be subscription, got %v", response[0]["quotaType"])
	}
}

func TestHandler_Insights_WithSyntheticProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 154.3, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 7635, RenewsAt: time.Now().Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/insights?provider=synthetic", nil)
	rr := httptest.NewRecorder()
	h.Insights(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if _, ok := response["stats"]; !ok {
		t.Error("expected stats field")
	}
	if _, ok := response["insights"]; !ok {
		t.Error("expected insights field")
	}
}

// Z.ai Provider Tests

func TestHandler_Current_WithZaiProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithZai()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=zai", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if _, ok := response["tokensLimit"]; !ok {
		t.Error("expected tokensLimit field")
	}
	if _, ok := response["timeLimit"]; !ok {
		t.Error("expected timeLimit field")
	}
}

func TestHandler_Current_ZaiReturnsTokensAndTimeLimits(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetTime := time.Now().Add(24 * time.Hour)
	// Z.ai API: "usage" = budget/capacity, "currentValue" = actual consumption
	zaiSnapshot := &api.ZaiSnapshot{
		CapturedAt:          time.Now().UTC(),
		TokensUsage:         200000000,  // budget
		TokensCurrentValue:  200000000,  // 100% consumed
		TokensRemaining:     0,
		TokensPercentage:    100,
		TimeUsage:           1000,       // budget
		TimeCurrentValue:    19,         // actual consumption
		TimeRemaining:       981,
		TimePercentage:      2,
		TokensNextResetTime: &resetTime,
	}
	s.InsertZaiSnapshot(zaiSnapshot)

	cfg := createTestConfigWithZai()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=zai", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	tokensLimit, ok := response["tokensLimit"].(map[string]interface{})
	if !ok {
		t.Fatal("expected tokensLimit in response")
	}

	// usage = TokensCurrentValue (actual consumption)
	if usage, ok := tokensLimit["usage"].(float64); !ok || usage != 200000000 {
		t.Errorf("expected tokens usage 200000000, got %v", usage)
	}

	// limit = TokensUsage (budget/capacity)
	if limit, ok := tokensLimit["limit"].(float64); !ok || limit != 200000000 {
		t.Errorf("expected tokens limit 200000000, got %v", limit)
	}

	timeLimit, ok := response["timeLimit"].(map[string]interface{})
	if !ok {
		t.Fatal("expected timeLimit in response")
	}

	// usage = TimeCurrentValue (actual consumption)
	if usage, ok := timeLimit["usage"].(float64); !ok || usage != 19 {
		t.Errorf("expected time usage 19, got %v", usage)
	}
}

func TestHandler_History_WithZaiProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetTime := time.Now().Add(24 * time.Hour)
	zaiSnapshot := &api.ZaiSnapshot{
		CapturedAt:          time.Now().UTC(),
		TokensLimit:         200000000,
		TokensUsage:         200112618,
		TokensRemaining:     0,
		TokensPercentage:    100,
		TimeLimit:           1000,
		TimeUsage:           19,
		TimeRemaining:       981,
		TimePercentage:      1,
		TokensNextResetTime: &resetTime,
	}
	s.InsertZaiSnapshot(zaiSnapshot)

	cfg := createTestConfigWithZai()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/history?provider=zai&range=24h", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(response) != 1 {
		t.Errorf("expected 1 history entry, got %d", len(response))
	}

	if _, ok := response[0]["tokensLimit"]; !ok {
		t.Error("expected tokensLimit field")
	}
	if _, ok := response[0]["timeLimit"]; !ok {
		t.Error("expected timeLimit field")
	}
}

func TestHandler_Summary_WithZaiProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetTime := time.Now().Add(24 * time.Hour)
	zaiSnapshot := &api.ZaiSnapshot{
		CapturedAt:          time.Now().UTC(),
		TokensLimit:         200000000,
		TokensUsage:         200112618,
		TokensRemaining:     0,
		TokensPercentage:    100,
		TimeLimit:           1000,
		TimeUsage:           19,
		TimeRemaining:       981,
		TimePercentage:      1,
		TokensNextResetTime: &resetTime,
	}
	s.InsertZaiSnapshot(zaiSnapshot)

	cfg := createTestConfigWithZai()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/summary?provider=zai", nil)
	rr := httptest.NewRecorder()
	h.Summary(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if _, ok := response["tokensLimit"]; !ok {
		t.Error("expected tokensLimit field")
	}
	if _, ok := response["timeLimit"]; !ok {
		t.Error("expected timeLimit field")
	}
}

func TestHandler_Cycles_WithZaiProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	now := time.Now().UTC()
	nextReset := now.Add(24 * time.Hour)
	s.CreateZaiCycle("tokens", now, &nextReset)

	cfg := createTestConfigWithZai()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycles?provider=zai&type=tokens", nil)
	rr := httptest.NewRecorder()
	h.Cycles(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(response) == 0 {
		t.Fatal("expected at least one cycle")
	}

	if response[0]["quotaType"] != "tokens" {
		t.Errorf("expected quotaType to be tokens, got %v", response[0]["quotaType"])
	}
}

func TestHandler_Cycles_ZaiTokensAndTimeTypes(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	now := time.Now().UTC()
	nextReset := now.Add(24 * time.Hour)
	s.CreateZaiCycle("tokens", now, &nextReset)
	s.CreateZaiCycle("time", now, nil)

	cfg := createTestConfigWithZai()
	h := NewHandler(s, nil, nil, nil, cfg)

	tests := []struct {
		quotaType string
	}{
		{"tokens"},
		{"time"},
	}

	for _, tt := range tests {
		t.Run(tt.quotaType, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/cycles?provider=zai&type="+tt.quotaType, nil)
			rr := httptest.NewRecorder()
			h.Cycles(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("type %s: expected status 200, got %d", tt.quotaType, rr.Code)
			}

			var response []map[string]interface{}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatalf("failed to parse JSON: %v", err)
			}

			if len(response) == 0 {
				t.Fatalf("expected at least one cycle for type %s", tt.quotaType)
			}

			if response[0]["quotaType"] != tt.quotaType {
				t.Errorf("expected quotaType to be %s, got %v", tt.quotaType, response[0]["quotaType"])
			}
		})
	}
}

func TestHandler_Insights_WithZaiProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetTime := time.Now().Add(24 * time.Hour)
	zaiSnapshot := &api.ZaiSnapshot{
		CapturedAt:          time.Now().UTC(),
		TokensLimit:         200000000,
		TokensUsage:         200112618,
		TokensRemaining:     0,
		TokensPercentage:    100,
		TimeLimit:           1000,
		TimeUsage:           19,
		TimeRemaining:       981,
		TimePercentage:      1,
		TokensNextResetTime: &resetTime,
	}
	s.InsertZaiSnapshot(zaiSnapshot)

	cfg := createTestConfigWithZai()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/insights?provider=zai", nil)
	rr := httptest.NewRecorder()
	h.Insights(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if _, ok := response["stats"]; !ok {
		t.Error("expected stats field")
	}
	if _, ok := response["insights"]; !ok {
		t.Error("expected insights field")
	}
}

// Provider Switching Tests

func TestHandler_ProviderSwitching_SyntheticToZai(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetTime := time.Now().Add(24 * time.Hour)
	zaiSnapshot := &api.ZaiSnapshot{
		CapturedAt:          time.Now().UTC(),
		TokensLimit:         200000000,
		TokensUsage:         200112618,
		TokensRemaining:     0,
		TokensPercentage:    100,
		TimeLimit:           1000,
		TimeUsage:           19,
		TimeRemaining:       981,
		TimePercentage:      1,
		TokensNextResetTime: &resetTime,
	}
	s.InsertZaiSnapshot(zaiSnapshot)

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 154.3, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 7635, RenewsAt: time.Now().Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	cfg := createTestConfigWithBoth()
	h := NewHandler(s, nil, nil, nil, cfg)

	// First request to synthetic
	req1 := httptest.NewRequest(http.MethodGet, "/api/current?provider=synthetic", nil)
	rr1 := httptest.NewRecorder()
	h.Current(rr1, req1)

	if rr1.Code != http.StatusOK {
		t.Errorf("synthetic request: expected status 200, got %d", rr1.Code)
	}

	var response1 map[string]interface{}
	json.Unmarshal(rr1.Body.Bytes(), &response1)
	if _, ok := response1["subscription"]; !ok {
		t.Error("synthetic response: expected subscription field")
	}

	// Switch to Z.ai
	req2 := httptest.NewRequest(http.MethodGet, "/api/current?provider=zai", nil)
	rr2 := httptest.NewRecorder()
	h.Current(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Errorf("zai request: expected status 200, got %d", rr2.Code)
	}

	var response2 map[string]interface{}
	json.Unmarshal(rr2.Body.Bytes(), &response2)
	if _, ok := response2["tokensLimit"]; !ok {
		t.Error("zai response: expected tokensLimit field")
	}
}

func TestHandler_ProviderSwitching_ZaiToSynthetic(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetTime := time.Now().Add(24 * time.Hour)
	zaiSnapshot := &api.ZaiSnapshot{
		CapturedAt:          time.Now().UTC(),
		TokensLimit:         200000000,
		TokensUsage:         200112618,
		TokensRemaining:     0,
		TokensPercentage:    100,
		TimeLimit:           1000,
		TimeUsage:           19,
		TimeRemaining:       981,
		TimePercentage:      1,
		TokensNextResetTime: &resetTime,
	}
	s.InsertZaiSnapshot(zaiSnapshot)

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 154.3, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 10, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 7635, RenewsAt: time.Now().Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	cfg := createTestConfigWithBoth()
	h := NewHandler(s, nil, nil, nil, cfg)

	// First request to Z.ai
	req1 := httptest.NewRequest(http.MethodGet, "/api/current?provider=zai", nil)
	rr1 := httptest.NewRecorder()
	h.Current(rr1, req1)

	if rr1.Code != http.StatusOK {
		t.Errorf("zai request: expected status 200, got %d", rr1.Code)
	}

	var response1 map[string]interface{}
	json.Unmarshal(rr1.Body.Bytes(), &response1)
	if _, ok := response1["tokensLimit"]; !ok {
		t.Error("zai response: expected tokensLimit field")
	}

	// Switch to Synthetic
	req2 := httptest.NewRequest(http.MethodGet, "/api/current?provider=synthetic", nil)
	rr2 := httptest.NewRecorder()
	h.Current(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Errorf("synthetic request: expected status 200, got %d", rr2.Code)
	}

	var response2 map[string]interface{}
	json.Unmarshal(rr2.Body.Bytes(), &response2)
	if _, ok := response2["subscription"]; !ok {
		t.Error("synthetic response: expected subscription field")
	}
}

func TestHandler_InvalidProvider_ReturnsError(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=invalid", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}

	var response map[string]string
	json.Unmarshal(rr.Body.Bytes(), &response)

	if _, ok := response["error"]; !ok {
		t.Error("expected error field in response")
	}
}

func TestHandler_UnconfiguredProvider_ReturnsError(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)

	// Z.ai is not configured, so this should fail
	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=zai", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}
}

// Dashboard Template Tests

func TestHandler_Dashboard_WithSingleProvider_NoSelector(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)

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
}

func TestHandler_Dashboard_WithMultipleProviders_ShowsSelector(t *testing.T) {
	cfg := createTestConfigWithBoth()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.Dashboard(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestHandler_Dashboard_PreservesProviderQueryParam(t *testing.T) {
	cfg := createTestConfigWithBoth()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/?provider=zai", nil)
	rr := httptest.NewRecorder()
	h.Dashboard(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

// Mock Data Tests

func TestHandler_Current_SyntheticWithMockData(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	snapshot := &api.Snapshot{
		CapturedAt: time.Now().UTC(),
		Sub:        api.QuotaInfo{Limit: 1350, Requests: 750.5, RenewsAt: time.Now().Add(5 * time.Hour)},
		Search:     api.QuotaInfo{Limit: 250, Requests: 125, RenewsAt: time.Now().Add(1 * time.Hour)},
		ToolCall:   api.QuotaInfo{Limit: 16200, Requests: 8000, RenewsAt: time.Now().Add(3 * time.Hour)},
	}
	s.InsertSnapshot(snapshot)

	tr := tracker.New(s, nil)
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, tr, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=synthetic", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	sub, ok := response["subscription"].(map[string]interface{})
	if !ok {
		t.Fatal("expected subscription in response")
	}

	if usage, ok := sub["usage"].(float64); !ok || usage != 750.5 {
		t.Errorf("expected usage 750.5, got %v", usage)
	}

	if limit, ok := sub["limit"].(float64); !ok || limit != 1350 {
		t.Errorf("expected limit 1350, got %v", limit)
	}
}

func TestHandler_Current_ZaiWithMockData(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetTime := time.Now().Add(24 * time.Hour)
	// Z.ai API: "usage" = budget/capacity, "currentValue" = actual consumption
	zaiSnapshot := &api.ZaiSnapshot{
		CapturedAt:          time.Now().UTC(),
		TokensUsage:         200000000,  // budget
		TokensCurrentValue:  100000000,  // 50% consumed
		TokensRemaining:     100000000,
		TokensPercentage:    50,
		TimeUsage:           1000,       // budget
		TimeCurrentValue:    500,        // 50% consumed
		TimeRemaining:       500,
		TimePercentage:      50,
		TokensNextResetTime: &resetTime,
	}
	s.InsertZaiSnapshot(zaiSnapshot)

	cfg := createTestConfigWithZai()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=zai", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	tokensLimit, ok := response["tokensLimit"].(map[string]interface{})
	if !ok {
		t.Fatal("expected tokensLimit in response")
	}

	// usage = TokensCurrentValue (actual consumption)
	if usage, ok := tokensLimit["usage"].(float64); !ok || usage != 100000000 {
		t.Errorf("expected usage 100000000, got %v", usage)
	}

	if percent, ok := tokensLimit["percent"].(float64); !ok || percent != 50.0 {
		t.Errorf("expected percent 50.0, got %v", percent)
	}
}

func TestHandler_History_SyntheticMultipleSnapshots(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	baseTime := time.Now().UTC().Add(-2 * time.Hour)
	for i := 0; i < 5; i++ {
		snapshot := &api.Snapshot{
			CapturedAt: baseTime.Add(time.Duration(i) * 30 * time.Minute),
			Sub:        api.QuotaInfo{Limit: 1350, Requests: float64(i * 100), RenewsAt: time.Now().Add(5 * time.Hour)},
			Search:     api.QuotaInfo{Limit: 250, Requests: float64(i * 10), RenewsAt: time.Now().Add(1 * time.Hour)},
			ToolCall:   api.QuotaInfo{Limit: 16200, Requests: float64(i * 50), RenewsAt: time.Now().Add(3 * time.Hour)},
		}
		s.InsertSnapshot(snapshot)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/history?provider=synthetic&range=6h", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(response) != 5 {
		t.Errorf("expected 5 history entries, got %d", len(response))
	}
}

func TestHandler_History_ZaiMultipleSnapshots(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithZai()
	h := NewHandler(s, nil, nil, nil, cfg)

	resetTime := time.Now().Add(24 * time.Hour)
	baseTime := time.Now().UTC().Add(-2 * time.Hour)
	for i := 0; i < 5; i++ {
		zaiSnapshot := &api.ZaiSnapshot{
			CapturedAt:          baseTime.Add(time.Duration(i) * 30 * time.Minute),
			TokensLimit:         200000000,
			TokensUsage:         float64(i * 1000000),
			TokensRemaining:     float64(200000000 - i*1000000),
			TokensPercentage:    i * 5,
			TimeLimit:           1000,
			TimeUsage:           float64(i * 10),
			TimeRemaining:       float64(1000 - i*10),
			TimePercentage:      i * 5,
			TokensNextResetTime: &resetTime,
		}
		s.InsertZaiSnapshot(zaiSnapshot)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/history?provider=zai&range=6h", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(response) != 5 {
		t.Errorf("expected 5 history entries, got %d", len(response))
	}
}

func TestHandler_Cycles_SyntheticActiveAndCompleted(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	now := time.Now().UTC()

	// Create an active cycle
	s.CreateCycle("subscription", now, now.Add(5*time.Hour))

	// Note: We can't easily create a completed cycle through the Store API
	// as cycles are typically closed automatically by the tracker
	// But we can verify the active cycle is present

	req := httptest.NewRequest(http.MethodGet, "/api/cycles?provider=synthetic&type=subscription", nil)
	rr := httptest.NewRecorder()
	h.Cycles(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(response) == 0 {
		t.Fatal("expected at least one cycle")
	}

	// The active cycle should have nil cycleEnd
	if response[0]["cycleEnd"] != nil {
		t.Error("expected active cycle to have nil cycleEnd")
	}
}

func TestHandler_Cycles_ZaiActiveAndCompleted(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithZai()
	h := NewHandler(s, nil, nil, nil, cfg)

	now := time.Now().UTC()
	nextReset := now.Add(24 * time.Hour)

	// Create an active cycle
	s.CreateZaiCycle("tokens", now, &nextReset)

	req := httptest.NewRequest(http.MethodGet, "/api/cycles?provider=zai&type=tokens", nil)
	rr := httptest.NewRecorder()
	h.Cycles(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(response) == 0 {
		t.Fatal("expected at least one cycle")
	}

	// The active cycle should have nil cycleEnd
	if response[0]["cycleEnd"] != nil {
		t.Error("expected active cycle to have nil cycleEnd")
	}
}
