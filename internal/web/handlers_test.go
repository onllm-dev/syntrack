package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/config"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
)

// Test helper functions for creating configurations
func createTestConfigWithSynthetic() *config.Config {
	return &config.Config{
		SyntheticAPIKey: "syn_test_key",
		PollInterval:    60 * time.Second,
		Port:            9211,
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
		Port:         9211,
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
		Port:            9211,
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
	if !strings.Contains(body, "onWatch") {
		t.Error("expected 'onWatch' in response body")
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

	s.CreateSession("session-1", time.Now().Add(-2*time.Hour), 60, "synthetic")
	s.CreateSession("session-2", time.Now().Add(-1*time.Hour), 60, "synthetic")

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

	s.CreateSession("session-1", time.Now(), 60, "synthetic")
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

	s.CreateSession("active-session", time.Now(), 60, "synthetic")
	s.CreateSession("closed-session", time.Now().Add(-2*time.Hour), 60, "synthetic")
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
		{"undefined", 0, true},
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
		Port:         9211,
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
	if len(providers) != 3 {
		t.Errorf("expected 3 providers (synthetic, zai, both), got %d", len(providers))
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
		TokensUsage:         200000000, // budget
		TokensCurrentValue:  200000000, // 100% consumed
		TokensRemaining:     0,
		TokensPercentage:    100,
		TimeUsage:           1000, // budget
		TimeCurrentValue:    19,   // actual consumption
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

func TestHandler_Dashboard_CodexSessionHeaders(t *testing.T) {
	cfg := createTestConfigWithCodex()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/?provider=codex", nil)
	rr := httptest.NewRecorder()
	h.Dashboard(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "5-Hour Limit") {
		t.Error("expected codex-specific 5-Hour Limit session column")
	}
	if !strings.Contains(body, "Weekly All-Model") {
		t.Error("expected codex-specific Weekly All-Model session column")
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
		TokensUsage:         200000000, // budget
		TokensCurrentValue:  100000000, // 50% consumed
		TokensRemaining:     100000000,
		TokensPercentage:    50,
		TimeUsage:           1000, // budget
		TimeCurrentValue:    500,  // 50% consumed
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

// ── KPI Modal Chart Regression Tests ──
// These tests guard against the range-selector misfire bug where
// insights range pills (data-insights-range) were picked up instead
// of chart range buttons (data-range), sending range=undefined to the API.

func TestHandler_History_UndefinedRange_Returns400(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/history?range=undefined&provider=synthetic", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for range=undefined, got %d", rr.Code)
	}
}

func TestHandler_History_EmptyDB_ReturnsEmptyArray_Synthetic(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/history?range=6h&provider=synthetic", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	body := strings.TrimSpace(rr.Body.String())
	if body != "[]" {
		t.Errorf("expected empty JSON array '[]' for empty DB, got %q", body)
	}
}

func TestHandler_History_EmptyDB_ReturnsEmptyArray_Zai(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithZai()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/history?range=6h&provider=zai", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	body := strings.TrimSpace(rr.Body.String())
	if body != "[]" {
		t.Errorf("expected empty JSON array '[]' for empty DB, got %q", body)
	}
}

func TestHandler_History_EmptyDB_ReturnsEmptyArrays_Both(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithBoth()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/history?range=6h&provider=both", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	for _, key := range []string{"synthetic", "zai", "anthropic", "copilot", "codex"} {
		val, ok := response[key]
		if !ok {
			continue
		}
		arr, ok := val.([]interface{})
		if !ok {
			t.Errorf("expected %s to be an array, got %T", key, val)
			continue
		}
		if len(arr) != 0 {
			t.Errorf("expected %s to be empty array, got %d items", key, len(arr))
		}
	}
}

// ── Anthropic Provider Tests ──

func createTestConfigWithAnthropic() *config.Config {
	return &config.Config{
		AnthropicToken: "test_anthropic_token",
		PollInterval:   60 * time.Second,
		Port:           9211,
		AdminUser:      "admin",
		AdminPass:      "test",
		DBPath:         "./test.db",
	}
}

func createTestConfigWithAll() *config.Config {
	return &config.Config{
		SyntheticAPIKey: "syn_test_key",
		ZaiAPIKey:       "zai_test_key",
		ZaiBaseURL:      "https://api.z.ai/api",
		AnthropicToken:  "test_anthropic_token",
		CodexToken:      "codex_test_token",
		PollInterval:    60 * time.Second,
		Port:            9211,
		AdminUser:       "admin",
		AdminPass:       "test",
		DBPath:          "./test.db",
	}
}

func createTestConfigWithCodex() *config.Config {
	return &config.Config{
		CodexToken:   "codex_test_token",
		PollInterval: 60 * time.Second,
		Port:         9211,
		AdminUser:    "admin",
		AdminPass:    "test",
		DBPath:       "./test.db",
	}
}

func TestHandler_SetAnthropicTracker(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)

	tr := tracker.NewAnthropicTracker(s, nil)
	h.SetAnthropicTracker(tr)

	if h.anthropicTracker == nil {
		t.Error("expected anthropicTracker to be set")
	}
}

func TestHandler_SetCodexTracker(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithCodex()
	h := NewHandler(s, nil, nil, nil, cfg)

	tr := tracker.NewCodexTracker(s, nil)
	h.SetCodexTracker(tr)

	if h.codexTracker == nil {
		t.Error("expected codexTracker to be set")
	}
}

func TestHandler_Current_WithAnthropicProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetsAt := time.Now().Add(5 * time.Hour)
	snapshot := &api.AnthropicSnapshot{
		CapturedAt: time.Now().UTC(),
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.0, ResetsAt: &resetsAt},
			{Name: "seven_day", Utilization: 20.0, ResetsAt: &resetsAt},
		},
		RawJSON: `{"five_hour":{"utilization":0.45},"seven_day":{"utilization":0.20}}`,
	}
	s.InsertAnthropicSnapshot(snapshot)

	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=anthropic", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if _, ok := response["capturedAt"]; !ok {
		t.Error("expected capturedAt field")
	}

	quotas, ok := response["quotas"].([]interface{})
	if !ok {
		t.Fatal("expected quotas array")
	}

	if len(quotas) != 2 {
		t.Errorf("expected 2 quotas, got %d", len(quotas))
	}

	// Verify first quota has expected fields
	q0, ok := quotas[0].(map[string]interface{})
	if !ok {
		t.Fatal("expected first quota to be a map")
	}
	if q0["name"] != "five_hour" {
		t.Errorf("expected first quota name 'five_hour', got %v", q0["name"])
	}
	if q0["displayName"] != "5-Hour Limit" {
		t.Errorf("expected displayName '5-Hour Limit', got %v", q0["displayName"])
	}
	if _, ok := q0["status"]; !ok {
		t.Error("expected status field")
	}
}

func TestHandler_Current_AnthropicEmptyDB(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=anthropic", nil)
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

	quotas, ok := response["quotas"].([]interface{})
	if !ok {
		t.Fatal("expected quotas array")
	}
	if len(quotas) != 0 {
		t.Errorf("expected 0 quotas with empty DB, got %d", len(quotas))
	}
}

func TestHandler_History_WithAnthropicProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetsAt := time.Now().Add(5 * time.Hour)
	snapshot := &api.AnthropicSnapshot{
		CapturedAt: time.Now().UTC(),
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.0, ResetsAt: &resetsAt},
		},
		RawJSON: `{"five_hour":{"utilization":0.45}}`,
	}
	s.InsertAnthropicSnapshot(snapshot)

	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/history?provider=anthropic&range=24h", nil)
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

	if _, ok := response[0]["capturedAt"]; !ok {
		t.Error("expected capturedAt field in history entry")
	}
	if _, ok := response[0]["five_hour"]; !ok {
		t.Error("expected five_hour utilization in history entry")
	}
}

func TestHandler_Cycles_WithAnthropicProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	now := time.Now().UTC()
	resetsAt := now.Add(5 * time.Hour)

	// Insert 3 snapshots with increasing utilization
	for i, util := range []float64{10.0, 25.0, 40.0} {
		snap := &api.AnthropicSnapshot{
			CapturedAt: now.Add(time.Duration(i) * time.Minute),
			Quotas: []api.AnthropicQuota{
				{Name: "five_hour", Utilization: util, ResetsAt: &resetsAt},
			},
			RawJSON: fmt.Sprintf(`{"five_hour":{"utilization":%v}}`, util),
		}
		s.InsertAnthropicSnapshot(snap)
	}

	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycles?provider=anthropic&type=five_hour", nil)
	rr := httptest.NewRecorder()
	h.Cycles(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(response) != 3 {
		t.Fatalf("expected 3 snapshot rows, got %d", len(response))
	}

	// Response is DESC order (newest first)
	if response[0]["quotaName"] != "five_hour" {
		t.Errorf("expected quotaName to be five_hour, got %v", response[0]["quotaName"])
	}

	// Newest snapshot (util=40) should be first, with cycleEnd=nil (active)
	if response[0]["cycleEnd"] != nil {
		t.Errorf("expected latest snapshot cycleEnd to be nil, got %v", response[0]["cycleEnd"])
	}

	// Check peakUtilization of newest = 40.0
	if peak, ok := response[0]["peakUtilization"].(float64); !ok || peak != 40.0 {
		t.Errorf("expected peakUtilization=40.0, got %v", response[0]["peakUtilization"])
	}

	// Check delta computation: 40-25=15 for the newest snapshot
	if delta, ok := response[0]["totalDelta"].(float64); !ok || delta != 15.0 {
		t.Errorf("expected totalDelta=15.0, got %v", response[0]["totalDelta"])
	}

	// First snapshot (util=10, oldest) should have delta=0
	if delta, ok := response[2]["totalDelta"].(float64); !ok || delta != 0.0 {
		t.Errorf("expected first snapshot totalDelta=0, got %v", response[2]["totalDelta"])
	}
}

func TestHandler_Summary_WithAnthropicProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetsAt := time.Now().Add(5 * time.Hour)
	snapshot := &api.AnthropicSnapshot{
		CapturedAt: time.Now().UTC(),
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.0, ResetsAt: &resetsAt},
		},
		RawJSON: `{"five_hour":{"utilization":0.45}}`,
	}
	s.InsertAnthropicSnapshot(snapshot)

	tr := tracker.NewAnthropicTracker(s, nil)
	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)
	h.SetAnthropicTracker(tr)

	req := httptest.NewRequest(http.MethodGet, "/api/summary?provider=anthropic", nil)
	rr := httptest.NewRecorder()
	h.Summary(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	// Summary should be keyed by quota name
	if _, ok := response["five_hour"]; !ok {
		t.Error("expected five_hour summary")
	}
}

func TestHandler_Insights_WithAnthropicProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetsAt := time.Now().Add(5 * time.Hour)
	snapshot := &api.AnthropicSnapshot{
		CapturedAt: time.Now().UTC(),
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.0, ResetsAt: &resetsAt},
		},
		RawJSON: `{"five_hour":{"utilization":0.45}}`,
	}
	s.InsertAnthropicSnapshot(snapshot)

	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/insights?provider=anthropic", nil)
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

func TestHandler_Current_WithCodexProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	capturedAt := time.Now().UTC()
	resetsAt := capturedAt.Add(5 * time.Hour)
	snapshot := &api.CodexSnapshot{
		CapturedAt: capturedAt,
		PlanType:   "plus",
		Quotas: []api.CodexQuota{
			{Name: "five_hour", Utilization: 42.5, ResetsAt: &resetsAt},
			{Name: "seven_day", Utilization: 18.0, ResetsAt: &resetsAt},
			{Name: "code_review", Utilization: 35.0, ResetsAt: &resetsAt},
		},
		RawJSON: `{"plan_type":"plus"}`,
	}
	if _, err := s.InsertCodexSnapshot(snapshot); err != nil {
		t.Fatalf("failed to insert codex snapshot: %v", err)
	}

	cfg := createTestConfigWithCodex()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=codex", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if response["planType"] != "plus" {
		t.Errorf("expected planType plus, got %v", response["planType"])
	}

	quotas, ok := response["quotas"].([]interface{})
	if !ok {
		t.Fatal("expected quotas array")
	}
	if len(quotas) != 3 {
		t.Fatalf("expected 3 codex quotas, got %d", len(quotas))
	}

	q0, ok := quotas[0].(map[string]interface{})
	if !ok {
		t.Fatal("expected first quota to be a map")
	}
	if q0["displayName"] != "5-Hour Limit" {
		t.Errorf("expected 5-Hour Limit displayName, got %v", q0["displayName"])
	}

	foundCodeReview := false
	for _, raw := range quotas {
		q, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if q["name"] != "code_review" {
			continue
		}
		foundCodeReview = true
		if q["displayName"] != "Review Requests" {
			t.Errorf("expected code_review displayName Review Requests, got %v", q["displayName"])
		}
		if q["cardLabel"] != "Remaining" {
			t.Errorf("expected code_review cardLabel Remaining, got %v", q["cardLabel"])
		}
		cardPercent, ok := q["cardPercent"].(float64)
		if !ok || cardPercent != 65.0 {
			t.Errorf("expected code_review cardPercent 65.0, got %v", q["cardPercent"])
		}
		if q["status"] != "healthy" {
			t.Errorf("expected code_review status healthy, got %v", q["status"])
		}
	}
	if !foundCodeReview {
		t.Error("expected code_review quota in codex response")
	}
}

func TestHandler_History_WithCodexProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	capturedAt := time.Now().UTC()
	snap := &api.CodexSnapshot{
		CapturedAt: capturedAt,
		Quotas: []api.CodexQuota{
			{Name: "five_hour", Utilization: 22.0},
			{Name: "seven_day", Utilization: 11.5},
			{Name: "code_review", Utilization: 7.0},
		},
		RawJSON: `{"ok":true}`,
	}
	if _, err := s.InsertCodexSnapshot(snap); err != nil {
		t.Fatalf("failed to insert codex snapshot: %v", err)
	}

	cfg := createTestConfigWithCodex()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/history?provider=codex&range=24h", nil)
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
		t.Fatalf("expected 1 history entry, got %d", len(response))
	}
	if _, ok := response[0]["capturedAt"]; !ok {
		t.Error("expected capturedAt in codex history entry")
	}
	if _, ok := response[0]["five_hour"]; !ok {
		t.Error("expected five_hour value in codex history entry")
	}
	if _, ok := response[0]["code_review"]; !ok {
		t.Error("expected code_review value in codex history entry")
	}
}

func TestHandler_Cycles_WithCodexProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	now := time.Now().UTC().Add(-5 * time.Hour)
	resetsAt := now.Add(5 * time.Hour)
	tkr := tracker.NewCodexTracker(s, nil)
	for i, util := range []float64{10.0, 30.0, 55.0} {
		snap := &api.CodexSnapshot{
			CapturedAt: now.Add(time.Duration(i) * time.Minute),
			Quotas: []api.CodexQuota{
				{Name: "five_hour", Utilization: util, ResetsAt: &resetsAt},
			},
			RawJSON: `{"ok":true}`,
		}
		if _, err := s.InsertCodexSnapshot(snap); err != nil {
			t.Fatalf("failed to insert codex snapshot: %v", err)
		}
		if err := tkr.Process(snap); err != nil {
			t.Fatalf("failed to process codex snapshot: %v", err)
		}
	}

	cfg := createTestConfigWithCodex()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycles?provider=codex&type=five_hour", nil)
	rr := httptest.NewRecorder()
	h.Cycles(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(response) != 1 {
		t.Fatalf("expected 1 cycle row, got %d", len(response))
	}
	if response[0]["quotaName"] != "five_hour" {
		t.Errorf("expected quotaName five_hour, got %v", response[0]["quotaName"])
	}
	if _, ok := response[0]["peakUtilization"]; !ok {
		t.Error("expected peakUtilization in codex cycle entry")
	}
}

func TestHandler_Cycles_CodexInvalidType(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithCodex()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycles?provider=codex&type=invalid", nil)
	rr := httptest.NewRecorder()
	h.Cycles(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandler_Summary_WithCodexProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	now := time.Now().UTC()
	resetsAt := now.Add(4 * time.Hour)
	tkr := tracker.NewCodexTracker(s, nil)
	for i, util := range []float64{20.0, 40.0} {
		snap := &api.CodexSnapshot{
			CapturedAt: now.Add(time.Duration(i) * time.Minute),
			Quotas:     []api.CodexQuota{{Name: "five_hour", Utilization: util, ResetsAt: &resetsAt}},
			RawJSON:    `{"ok":true}`,
		}
		if _, err := s.InsertCodexSnapshot(snap); err != nil {
			t.Fatalf("failed to insert codex snapshot: %v", err)
		}
		if err := tkr.Process(snap); err != nil {
			t.Fatalf("failed to process codex snapshot: %v", err)
		}
	}

	cfg := createTestConfigWithCodex()
	h := NewHandler(s, nil, nil, nil, cfg)
	h.SetCodexTracker(tkr)

	req := httptest.NewRequest(http.MethodGet, "/api/summary?provider=codex", nil)
	rr := httptest.NewRecorder()
	h.Summary(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if _, ok := response["five_hour"]; !ok {
		t.Error("expected five_hour summary")
	}
}

func TestHandler_Insights_CodexEmptyDB(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithCodex()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/insights?provider=codex", nil)
	rr := httptest.NewRecorder()
	h.Insights(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response insightsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if len(response.Insights) == 0 {
		t.Fatal("expected at least one insight")
	}
	if response.Insights[0].Title != "Getting Started" {
		t.Errorf("expected Getting Started insight, got %q", response.Insights[0].Title)
	}
}

func TestHandler_Insights_CodexRichData(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	now := time.Now().UTC().Add(-2 * time.Hour)
	fiveHourReset := now.Add(3 * time.Hour)
	weeklyReset := now.Add(5 * 24 * time.Hour)
	credits := 87.5
	tkr := tracker.NewCodexTracker(s, nil)

	for i, util := range []float64{22.0, 31.0, 44.0} {
		snap := &api.CodexSnapshot{
			CapturedAt:     now.Add(time.Duration(i) * 30 * time.Minute),
			PlanType:       "plus",
			CreditsBalance: &credits,
			Quotas: []api.CodexQuota{
				{Name: "five_hour", Utilization: util, ResetsAt: &fiveHourReset},
				{Name: "seven_day", Utilization: 60.0 + float64(i), ResetsAt: &weeklyReset},
			},
			RawJSON: `{"ok":true}`,
		}
		if _, err := s.InsertCodexSnapshot(snap); err != nil {
			t.Fatalf("failed to insert codex snapshot: %v", err)
		}
		if err := tkr.Process(snap); err != nil {
			t.Fatalf("failed to process codex snapshot: %v", err)
		}
	}

	cfg := createTestConfigWithCodex()
	h := NewHandler(s, nil, nil, nil, cfg)
	h.SetCodexTracker(tkr)

	req := httptest.NewRequest(http.MethodGet, "/api/insights?provider=codex&range=1d", nil)
	rr := httptest.NewRecorder()
	h.Insights(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response insightsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if len(response.Stats) == 0 {
		t.Fatal("expected codex stats")
	}
	if len(response.Insights) == 0 {
		t.Fatal("expected codex insights")
	}

	hasPlan := false
	for _, st := range response.Stats {
		if st.Label == "Plan" {
			hasPlan = true
		}
	}
	if !hasPlan {
		t.Error("expected Plan stat in codex insights response")
	}
	for _, in := range response.Insights {
		if in.Title == "Tracking Quality" {
			t.Error("did not expect Tracking Quality insight in codex insights response")
		}
		if in.Title == "Next Reset" {
			t.Error("did not expect Next Reset insight in codex insights response")
		}
		if in.Title == "Credits Balance" {
			t.Error("did not expect Credits Balance insight in codex insights response")
		}
	}
	for _, st := range response.Stats {
		if st.Label == "Credits" {
			t.Error("did not expect Credits stat in codex insights response")
		}
		if st.Label == "Next Reset" {
			t.Error("did not expect Next Reset stat in codex insights response")
		}
	}

	shortForecastFound := false
	weeklyForecastFound := false
	for _, in := range response.Insights {
		if in.Title == "Short Window Burn Rate" {
			shortForecastFound = strings.Contains(in.Sublabel, "by reset")
		}
		if in.Title == "Weekly Window Burn Rate" {
			weeklyForecastFound = strings.Contains(in.Sublabel, "by reset")
		}
	}
	if !shortForecastFound {
		t.Error("expected Short Window Burn Rate to show reset estimate sublabel")
	}
	if !weeklyForecastFound {
		t.Error("expected Weekly Window Burn Rate to show reset estimate sublabel")
	}
}

func TestHandler_Providers_WithCodexOnly(t *testing.T) {
	cfg := createTestConfigWithCodex()
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
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
	if providers[0] != "codex" {
		t.Errorf("expected codex provider, got %v", providers[0])
	}
}

func TestHandler_Current_BothIncludesCodex(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	snap := &api.CodexSnapshot{
		CapturedAt: time.Now().UTC(),
		Quotas:     []api.CodexQuota{{Name: "five_hour", Utilization: 25.0}},
		RawJSON:    `{"ok":true}`,
	}
	if _, err := s.InsertCodexSnapshot(snap); err != nil {
		t.Fatalf("failed to insert codex snapshot: %v", err)
	}

	cfg := createTestConfigWithAll()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=both", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if _, ok := response["codex"]; !ok {
		t.Error("expected codex field in both response")
	}
}

func TestHandler_CycleOverview_Codex(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithCodex()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycle-overview?provider=codex", nil)
	rr := httptest.NewRecorder()
	h.CycleOverview(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if response["provider"] != "codex" {
		t.Errorf("expected provider codex, got %v", response["provider"])
	}
	if response["groupBy"] != "five_hour" {
		t.Errorf("expected default groupBy five_hour, got %v", response["groupBy"])
	}
}

func TestHandler_CodexUtilStatus(t *testing.T) {
	tests := []struct {
		util   float64
		status string
	}{
		{0, "healthy"},
		{49.9, "healthy"},
		{50, "warning"},
		{79.9, "warning"},
		{80, "danger"},
		{94.9, "danger"},
		{95, "critical"},
		{100, "critical"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("util_%.0f", tt.util), func(t *testing.T) {
			got := codexUtilStatus(tt.util)
			if got != tt.status {
				t.Errorf("codexUtilStatus(%.1f) = %q, want %q", tt.util, got, tt.status)
			}
		})
	}
}

func TestHandler_CodexRemainingStatus(t *testing.T) {
	tests := []struct {
		remaining float64
		status    string
	}{
		{100, "healthy"},
		{50, "warning"},
		{20, "danger"},
		{5, "critical"},
		{0, "critical"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("remaining_%.0f", tt.remaining), func(t *testing.T) {
			got := codexRemainingStatus(tt.remaining)
			if got != tt.status {
				t.Errorf("codexRemainingStatus(%.1f) = %q, want %q", tt.remaining, got, tt.status)
			}
		})
	}
}

func TestHandler_Providers_WithAnthropicOnly(t *testing.T) {
	cfg := createTestConfigWithAnthropic()
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
	if providers[0] != "anthropic" {
		t.Errorf("expected anthropic provider, got %v", providers[0])
	}
}

func TestHandler_Providers_WithAllProviders_IncludesBoth(t *testing.T) {
	cfg := createTestConfigWithAll()
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

	// Should have synthetic, zai, anthropic, codex, both = 5
	if len(providers) != 5 {
		t.Errorf("expected 5 providers (synthetic, zai, anthropic, codex, both), got %d: %v", len(providers), providers)
	}
}

func TestHandler_Current_BothIncludesAnthropic(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetsAt := time.Now().Add(5 * time.Hour)
	snapshot := &api.AnthropicSnapshot{
		CapturedAt: time.Now().UTC(),
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.0, ResetsAt: &resetsAt},
		},
		RawJSON: `{"five_hour":{"utilization":0.45}}`,
	}
	s.InsertAnthropicSnapshot(snapshot)

	cfg := createTestConfigWithAll()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=both", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if _, ok := response["anthropic"]; !ok {
		t.Error("expected anthropic field in 'both' response")
	}
}

func TestHandler_AnthropicUtilStatus(t *testing.T) {
	tests := []struct {
		util   float64
		status string
	}{
		{0, "healthy"},
		{49.9, "healthy"},
		{50, "warning"},
		{79.9, "warning"},
		{80, "danger"},
		{94.9, "danger"},
		{95, "critical"},
		{100, "critical"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("util_%.0f", tt.util), func(t *testing.T) {
			got := anthropicUtilStatus(tt.util)
			if got != tt.status {
				t.Errorf("anthropicUtilStatus(%.1f) = %q, want %q", tt.util, got, tt.status)
			}
		})
	}
}

func TestHandler_Insights_AnthropicEmptyDB(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/insights?provider=anthropic", nil)
	rr := httptest.NewRecorder()
	h.Insights(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response insightsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	// Should have a "Getting Started" insight for empty DB
	if len(response.Insights) == 0 {
		t.Fatal("expected at least one insight")
	}
	if response.Insights[0].Title != "Getting Started" {
		t.Errorf("expected 'Getting Started' insight, got %q", response.Insights[0].Title)
	}
}

// ═══════════════════════════════════════════════════════════════════
// ── Login / Logout Tests ──
// ═══════════════════════════════════════════════════════════════════

func TestHandler_Login_GET_RendersForm(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)
	h.SetVersion("2.10.3")

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html, got %s", ct)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "/static/app.js?v=2.10.3") {
		t.Fatalf("expected login page to include versioned app.js URL, body=%s", body)
	}
	if !regexp.MustCompile(`/static/app\.js\?v=[^"\s]+`).MatchString(body) {
		t.Fatalf("expected login page to include non-empty app.js version token, body=%s", body)
	}
}

func TestHandler_Login_POST_ValidCredentials_Redirects(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	passHash := legacyHashPassword("test")
	sessions := NewSessionStore("admin", passHash, s)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, sessions, cfg)

	body := strings.NewReader("username=admin&password=test")
	req := httptest.NewRequest(http.MethodPost, "/login", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.Login(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("expected status 302, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if loc != "/" {
		t.Errorf("expected redirect to /, got %s", loc)
	}
	// Should set a session cookie
	cookies := rr.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "onwatch_session" && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected onwatch_session cookie to be set")
	}
}

func TestHandler_Login_POST_InvalidCredentials_RedirectsWithError(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	passHash := legacyHashPassword("test")
	sessions := NewSessionStore("admin", passHash, s)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, sessions, cfg)

	body := strings.NewReader("username=admin&password=wrong")
	req := httptest.NewRequest(http.MethodPost, "/login", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.Login(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("expected status 302, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "/login?error=") {
		t.Errorf("expected redirect to /login with error, got %s", loc)
	}
}

func TestHandler_Login_GET_AlreadyAuthenticated_RedirectsToDashboard(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	passHash := legacyHashPassword("test")
	sessions := NewSessionStore("admin", passHash, s)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, sessions, cfg)

	// Authenticate to get a token
	token, ok := sessions.Authenticate("admin", "test")
	if !ok {
		t.Fatal("authentication should succeed")
	}

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.AddCookie(&http.Cookie{Name: "onwatch_session", Value: token})
	rr := httptest.NewRecorder()

	h.Login(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("expected status 302, got %d", rr.Code)
	}
	if rr.Header().Get("Location") != "/" {
		t.Errorf("expected redirect to /, got %s", rr.Header().Get("Location"))
	}
}

func TestHandler_Logout_ClearsCookieAndRedirects(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	passHash := legacyHashPassword("test")
	sessions := NewSessionStore("admin", passHash, s)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, sessions, cfg)

	token, _ := sessions.Authenticate("admin", "test")

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: "onwatch_session", Value: token})
	rr := httptest.NewRecorder()

	h.Logout(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("expected status 302, got %d", rr.Code)
	}
	if rr.Header().Get("Location") != "/login" {
		t.Errorf("expected redirect to /login, got %s", rr.Header().Get("Location"))
	}
	// Cookie should be expired
	for _, c := range rr.Result().Cookies() {
		if c.Name == "onwatch_session" && c.MaxAge >= 0 {
			t.Error("expected session cookie to be expired (MaxAge < 0)")
		}
	}
	// Token should be invalidated
	if sessions.ValidateToken(token) {
		t.Error("expected token to be invalidated after logout")
	}
}

func TestHandler_SettingsPage_RendersHTML(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)
	h.SetVersion("2.5.0")

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	rr := httptest.NewRecorder()
	h.SettingsPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html, got %s", ct)
	}
}

// ═══════════════════════════════════════════════════════════════════
// ── Password Change Tests ──
// ═══════════════════════════════════════════════════════════════════

func TestHandler_ChangePassword_Success(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	passHash := legacyHashPassword("oldpass")
	sessions := NewSessionStore("admin", passHash, s)
	s.UpsertUser("admin", passHash)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, sessions, cfg)

	body := strings.NewReader(`{"current_password":"oldpass","new_password":"newpass123"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/password", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ChangePassword(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", rr.Code, rr.Body.String())
	}

	var response map[string]string
	json.Unmarshal(rr.Body.Bytes(), &response)
	if response["message"] != "password updated successfully" {
		t.Errorf("unexpected message: %s", response["message"])
	}
}

func TestHandler_ChangePassword_WrongCurrentPassword(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	passHash := legacyHashPassword("oldpass")
	sessions := NewSessionStore("admin", passHash, s)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, sessions, cfg)

	body := strings.NewReader(`{"current_password":"wrongpass","new_password":"newpass123"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/password", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ChangePassword(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestHandler_ChangePassword_TooShortNewPassword(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	passHash := legacyHashPassword("oldpass")
	sessions := NewSessionStore("admin", passHash, s)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, sessions, cfg)

	body := strings.NewReader(`{"current_password":"oldpass","new_password":"abc"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/password", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ChangePassword(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}
	var response map[string]string
	json.Unmarshal(rr.Body.Bytes(), &response)
	if !strings.Contains(response["error"], "at least 6 characters") {
		t.Errorf("expected 'at least 6 characters' error, got %s", response["error"])
	}
}

func TestHandler_ChangePassword_MissingFields(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	passHash := legacyHashPassword("oldpass")
	sessions := NewSessionStore("admin", passHash, s)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, sessions, cfg)

	body := strings.NewReader(`{"current_password":"oldpass","new_password":""}`)
	req := httptest.NewRequest(http.MethodPut, "/api/password", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ChangePassword(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}
}

func TestHandler_ChangePassword_InvalidatesAllSessions(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	passHash := legacyHashPassword("oldpass")
	sessions := NewSessionStore("admin", passHash, s)
	s.UpsertUser("admin", passHash)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, sessions, cfg)

	// Create a session token first
	token, ok := sessions.Authenticate("admin", "oldpass")
	if !ok {
		t.Fatal("auth should succeed")
	}
	if !sessions.ValidateToken(token) {
		t.Fatal("token should be valid before password change")
	}

	body := strings.NewReader(`{"current_password":"oldpass","new_password":"newpass123"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/password", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ChangePassword(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body: %s", rr.Code, rr.Body.String())
	}

	// Old token should be invalidated
	if sessions.ValidateToken(token) {
		t.Error("expected all sessions to be invalidated after password change")
	}
}

func TestHandler_ChangePassword_MethodNotAllowed(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/password", nil)
	rr := httptest.NewRecorder()
	h.ChangePassword(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", rr.Code)
	}
}

// ═══════════════════════════════════════════════════════════════════
// ── Settings CRUD Tests ──
// ═══════════════════════════════════════════════════════════════════

func TestHandler_GetSettings_ReturnsTimezoneAndHiddenInsights(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	s.SetSetting("timezone", "America/New_York")
	s.SetSetting("hidden_insights", `["cycle_utilization","trend"]`)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rr := httptest.NewRecorder()
	h.GetSettings(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	if response["timezone"] != "America/New_York" {
		t.Errorf("expected timezone America/New_York, got %v", response["timezone"])
	}
	hidden, ok := response["hidden_insights"].([]interface{})
	if !ok || len(hidden) != 2 {
		t.Errorf("expected 2 hidden insights, got %v", response["hidden_insights"])
	}
}

func TestHandler_UpdateSettings_Timezone(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	body := strings.NewReader(`{"timezone":"Europe/London"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.UpdateSettings(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", rr.Code, rr.Body.String())
	}

	// Verify it was saved
	val, _ := s.GetSetting("timezone")
	if val != "Europe/London" {
		t.Errorf("expected timezone Europe/London, got %s", val)
	}
}

func TestHandler_UpdateSettings_InvalidTimezone(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	body := strings.NewReader(`{"timezone":"Invalid/Timezone"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.UpdateSettings(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}
}

func TestHandler_UpdateSettings_HiddenInsights(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	body := strings.NewReader(`{"hidden_insights":["cycle_utilization","weekly_pace"]}`)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.UpdateSettings(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", rr.Code, rr.Body.String())
	}

	val, _ := s.GetSetting("hidden_insights")
	if !strings.Contains(val, "cycle_utilization") || !strings.Contains(val, "weekly_pace") {
		t.Errorf("expected hidden insights to be saved, got %s", val)
	}
}

func TestHandler_GetSettings_SMTPMasksPassword(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	smtpConfig := `{"host":"smtp.example.com","port":587,"password":"secret123","from_address":"test@example.com"}`
	s.SetSetting("smtp", smtpConfig)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rr := httptest.NewRecorder()
	h.GetSettings(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	smtp, ok := response["smtp"].(map[string]interface{})
	if !ok {
		t.Fatal("expected smtp field in response")
	}
	// Password should be empty (masked)
	if smtp["password"] != "" {
		t.Error("SMTP password should be masked (empty) in GET response")
	}
	// password_set should be true
	if smtp["password_set"] != true {
		t.Error("expected password_set to be true")
	}
}

func TestHandler_GetSettings_NotificationSettings(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	notifConfig := `{"warning_threshold":70,"critical_threshold":90,"notify_warning":true,"notify_critical":true}`
	s.SetSetting("notifications", notifConfig)

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rr := httptest.NewRecorder()
	h.GetSettings(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	notif, ok := response["notifications"].(map[string]interface{})
	if !ok {
		t.Fatal("expected notifications field in response")
	}
	if notif["warning_threshold"].(float64) != 70 {
		t.Errorf("expected warning_threshold 70, got %v", notif["warning_threshold"])
	}
}

func TestHandler_UpdateSettings_ProviderVisibility(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	body := strings.NewReader(`{"provider_visibility":{"synthetic":{"dashboard":true,"polling":true}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.UpdateSettings(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", rr.Code, rr.Body.String())
	}

	val, _ := s.GetSetting("provider_visibility")
	if !strings.Contains(val, "synthetic") {
		t.Errorf("expected provider_visibility to be saved, got %s", val)
	}
}

func TestHandler_UpdateSettings_Notifications(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	body := strings.NewReader(`{"notifications":{"warning_threshold":60,"critical_threshold":85,"notify_warning":true,"notify_critical":true,"notify_reset":false,"cooldown_minutes":15}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.UpdateSettings(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", rr.Code, rr.Body.String())
	}

	val, _ := s.GetSetting("notifications")
	if !strings.Contains(val, "60") {
		t.Errorf("expected notification settings to be saved, got %s", val)
	}
}

func TestHandler_UpdateSettings_Notifications_InvalidThresholds(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	// Warning >= Critical should fail
	body := strings.NewReader(`{"notifications":{"warning_threshold":90,"critical_threshold":85,"notify_warning":true,"notify_critical":true,"cooldown_minutes":15}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.UpdateSettings(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for warning >= critical, got %d", rr.Code)
	}
}

func TestHandler_UpdateSettings_MethodNotAllowed(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rr := httptest.NewRecorder()

	// UpdateSettings checks for PUT method
	h.UpdateSettings(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", rr.Code)
	}
}

// ═══════════════════════════════════════════════════════════════════
// ── SMTP Test Handler Tests ──
// ═══════════════════════════════════════════════════════════════════

// mockNotifier implements the Notifier interface for testing.
type mockNotifier struct {
	sendTestErr  error
	reloadCalled bool
}

func (m *mockNotifier) Reload() error             { m.reloadCalled = true; return nil }
func (m *mockNotifier) ConfigureSMTP() error      { return nil }
func (m *mockNotifier) ConfigurePush() error      { return nil }
func (m *mockNotifier) SendTestEmail() error      { return m.sendTestErr }
func (m *mockNotifier) SendTestPush() error       { return nil }
func (m *mockNotifier) SetEncryptionKey(_ string) {}
func (m *mockNotifier) GetVAPIDPublicKey() string { return "" }

func TestHandler_SMTPTest_Success(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)
	h.SetNotifier(&mockNotifier{})

	req := httptest.NewRequest(http.MethodPost, "/api/settings/smtp/test", nil)
	rr := httptest.NewRecorder()
	h.SMTPTest(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	if response["success"] != true {
		t.Errorf("expected success true, got %v", response["success"])
	}
}

func TestHandler_SMTPTest_RateLimit(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)
	h.SetNotifier(&mockNotifier{})

	// First request succeeds
	req1 := httptest.NewRequest(http.MethodPost, "/api/settings/smtp/test", nil)
	rr1 := httptest.NewRecorder()
	h.SMTPTest(rr1, req1)

	if rr1.Code != http.StatusOK {
		t.Fatalf("first request: expected status 200, got %d", rr1.Code)
	}

	// Second request within 30s should be rate-limited
	req2 := httptest.NewRequest(http.MethodPost, "/api/settings/smtp/test", nil)
	rr2 := httptest.NewRecorder()
	h.SMTPTest(rr2, req2)

	if rr2.Code != http.StatusTooManyRequests {
		t.Errorf("second request: expected status 429, got %d", rr2.Code)
	}
}

func TestHandler_SMTPTest_NoNotifierConfigured(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)
	// No notifier set

	req := httptest.NewRequest(http.MethodPost, "/api/settings/smtp/test", nil)
	rr := httptest.NewRecorder()
	h.SMTPTest(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rr.Code)
	}
}

func TestHandler_SMTPTest_MethodNotAllowed(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/settings/smtp/test", nil)
	rr := httptest.NewRecorder()
	h.SMTPTest(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", rr.Code)
	}
}

// ═══════════════════════════════════════════════════════════════════
// ── CycleOverview Tests ──
// ═══════════════════════════════════════════════════════════════════

func TestHandler_CycleOverview_Synthetic(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycle-overview?provider=synthetic", nil)
	rr := httptest.NewRecorder()
	h.CycleOverview(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	if response["provider"] != "synthetic" {
		t.Errorf("expected provider synthetic, got %v", response["provider"])
	}
	if response["groupBy"] != "subscription" {
		t.Errorf("expected default groupBy subscription, got %v", response["groupBy"])
	}
}

func TestHandler_CycleOverview_Zai(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithZai()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycle-overview?provider=zai", nil)
	rr := httptest.NewRecorder()
	h.CycleOverview(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	if response["provider"] != "zai" {
		t.Errorf("expected provider zai, got %v", response["provider"])
	}
	if response["groupBy"] != "tokens" {
		t.Errorf("expected default groupBy tokens, got %v", response["groupBy"])
	}
}

func TestHandler_CycleOverview_Anthropic(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycle-overview?provider=anthropic", nil)
	rr := httptest.NewRecorder()
	h.CycleOverview(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	if response["provider"] != "anthropic" {
		t.Errorf("expected provider anthropic, got %v", response["provider"])
	}
	if response["groupBy"] != "five_hour" {
		t.Errorf("expected default groupBy five_hour, got %v", response["groupBy"])
	}
}

func TestHandler_CycleOverview_Both(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithBoth()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycle-overview?provider=both", nil)
	rr := httptest.NewRecorder()
	h.CycleOverview(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	if _, ok := response["synthetic"]; !ok {
		t.Error("expected synthetic field in 'both' response")
	}
	if _, ok := response["zai"]; !ok {
		t.Error("expected zai field in 'both' response")
	}
}

func TestHandler_Sessions_BothIncludesCodex(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithAll()
	h := NewHandler(s, nil, nil, nil, cfg)

	if err := s.CreateSession("codex-session", time.Now().Add(-30*time.Minute), 60, "codex", 12.0, 8.0, 0); err != nil {
		t.Fatalf("failed to create codex session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions?provider=both", nil)
	rr := httptest.NewRecorder()
	h.Sessions(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response map[string][]map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	codexSessions, ok := response["codex"]
	if !ok {
		t.Fatal("expected codex field in both sessions response")
	}
	if len(codexSessions) != 1 {
		t.Fatalf("expected 1 codex session, got %d", len(codexSessions))
	}
	if codexSessions[0]["id"] != "codex-session" {
		t.Fatalf("expected codex session id codex-session, got %v", codexSessions[0]["id"])
	}
}

func TestHandler_CycleOverview_BothIncludesCodex(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithAll()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycle-overview?provider=both", nil)
	rr := httptest.NewRecorder()
	h.CycleOverview(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	codexRaw, ok := response["codex"]
	if !ok {
		t.Fatal("expected codex field in both cycle overview response")
	}
	codex, ok := codexRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("expected codex overview to be object, got %T", codexRaw)
	}
	if codex["provider"] != "codex" {
		t.Fatalf("expected codex provider field, got %v", codex["provider"])
	}
	if codex["groupBy"] != "five_hour" {
		t.Fatalf("expected codex default groupBy five_hour, got %v", codex["groupBy"])
	}
}

func TestHandler_CycleOverview_BothCodexRespectsGroupByFallback(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithAll()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycle-overview?provider=both&groupBy=seven_day", nil)
	rr := httptest.NewRecorder()
	h.CycleOverview(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	codexRaw, ok := response["codex"]
	if !ok {
		t.Fatal("expected codex field in both cycle overview response")
	}
	codex, ok := codexRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("expected codex overview to be object, got %T", codexRaw)
	}
	if codex["groupBy"] != "seven_day" {
		t.Fatalf("expected codex groupBy seven_day from generic groupBy fallback, got %v", codex["groupBy"])
	}
}

// ═══════════════════════════════════════════════════════════════════
// ── Update Handler Tests ──
// ═══════════════════════════════════════════════════════════════════

func TestHandler_CheckUpdate_NoUpdater(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)
	// No updater set

	req := httptest.NewRequest(http.MethodGet, "/api/update/check", nil)
	rr := httptest.NewRecorder()
	h.CheckUpdate(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rr.Code)
	}
}

func TestHandler_CheckUpdate_MethodNotAllowed(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/update/check", nil)
	rr := httptest.NewRecorder()
	h.CheckUpdate(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", rr.Code)
	}
}

func TestHandler_ApplyUpdate_NoUpdater(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)
	// No updater set

	req := httptest.NewRequest(http.MethodPost, "/api/update/apply", nil)
	rr := httptest.NewRecorder()
	h.ApplyUpdate(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rr.Code)
	}
}

func TestHandler_ApplyUpdate_MethodNotAllowed(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/update/apply", nil)
	rr := httptest.NewRecorder()
	h.ApplyUpdate(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", rr.Code)
	}
}

// ═══════════════════════════════════════════════════════════════════
// ── Anthropic Handler Tests ──
// ═══════════════════════════════════════════════════════════════════

func TestHandler_Current_Anthropic_WithData(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetsAt := time.Now().Add(5 * time.Hour)
	snapshot := &api.AnthropicSnapshot{
		CapturedAt: time.Now().UTC(),
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.2, ResetsAt: &resetsAt},
			{Name: "seven_day", Utilization: 12.8, ResetsAt: &resetsAt},
		},
		RawJSON: `{"five_hour":{"utilization":45.2},"seven_day":{"utilization":12.8}}`,
	}
	s.InsertAnthropicSnapshot(snapshot)

	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=anthropic", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	quotas, ok := response["quotas"].([]interface{})
	if !ok {
		t.Fatal("expected quotas array in response")
	}
	if len(quotas) != 2 {
		t.Errorf("expected 2 quotas, got %d", len(quotas))
	}

	// Verify first quota structure
	q0, ok := quotas[0].(map[string]interface{})
	if !ok {
		t.Fatal("expected quota to be a map")
	}
	if q0["name"] != "five_hour" {
		t.Errorf("expected first quota name 'five_hour', got %v", q0["name"])
	}
	if q0["utilization"].(float64) != 45.2 {
		t.Errorf("expected utilization 45.2, got %v", q0["utilization"])
	}
}

func TestHandler_Current_Anthropic_EmptyDB(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=anthropic", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &response)

	quotas, ok := response["quotas"].([]interface{})
	if !ok {
		t.Fatal("expected quotas array in response")
	}
	if len(quotas) != 0 {
		t.Errorf("expected empty quotas for empty DB, got %d", len(quotas))
	}
}

func TestHandler_History_Anthropic(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetsAt := time.Now().Add(5 * time.Hour)
	snapshot := &api.AnthropicSnapshot{
		CapturedAt: time.Now().UTC(),
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.2, ResetsAt: &resetsAt},
		},
		RawJSON: `{"five_hour":{"utilization":45.2}}`,
	}
	s.InsertAnthropicSnapshot(snapshot)

	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/history?provider=anthropic&range=24h", nil)
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
	if len(response) > 0 {
		if _, ok := response[0]["five_hour"]; !ok {
			t.Error("expected five_hour field in history entry")
		}
	}
}

func TestHandler_Insights_Anthropic_WithData(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	resetsAt := time.Now().Add(5 * time.Hour)
	snapshot := &api.AnthropicSnapshot{
		CapturedAt: time.Now().UTC(),
		Quotas: []api.AnthropicQuota{
			{Name: "five_hour", Utilization: 45.2, ResetsAt: &resetsAt},
			{Name: "seven_day", Utilization: 12.8, ResetsAt: &resetsAt},
		},
		RawJSON: `{"five_hour":{"utilization":45.2},"seven_day":{"utilization":12.8}}`,
	}
	s.InsertAnthropicSnapshot(snapshot)

	cfg := createTestConfigWithAnthropic()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/insights?provider=anthropic", nil)
	rr := httptest.NewRecorder()
	h.Insights(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response insightsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if response.Stats == nil {
		t.Error("expected stats in response")
	}
	if response.Insights == nil {
		t.Error("expected insights in response")
	}
}

// ═══════════════════════════════════════════════════════════════════
// ── Dashboard With Provider Param Tests ──
// ═══════════════════════════════════════════════════════════════════

func TestHandler_Dashboard_WithProviderParam(t *testing.T) {
	cfg := createTestConfigWithAll()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/?provider=anthropic", nil)
	rr := httptest.NewRecorder()
	h.Dashboard(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html, got %s", ct)
	}
}

func TestHandler_Dashboard_AppJSVersionedURL_Rendered(t *testing.T) {
	cfg := createTestConfigWithAll()
	h := NewHandler(nil, nil, nil, nil, cfg)
	h.SetVersion("2.10.3")

	req := httptest.NewRequest(http.MethodGet, "/?provider=both", nil)
	rr := httptest.NewRecorder()
	h.Dashboard(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "/static/app.js?v=2.10.3") {
		t.Fatalf("expected versioned app.js URL, body=%s", body)
	}

	if strings.Contains(body, "/static/app.js?v=") && !strings.Contains(body, "/static/app.js?v=2.10.3") {
		t.Fatalf("expected app.js version token to match 2.10.3, body=%s", body)
	}
}

func TestHandler_Dashboard_NotFound_For_NonRootPath(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rr := httptest.NewRecorder()
	h.Dashboard(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status 404 for non-root path, got %d", rr.Code)
	}
}

// ═══════════════════════════════════════════════════════════════════
// ── Utility Function Tests ──
// ═══════════════════════════════════════════════════════════════════

func TestHandler_formatDuration(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Duration
		expected string
	}{
		{"negative", -1 * time.Minute, "Resetting..."},
		{"days and hours", 4*24*time.Hour + 11*time.Hour, "4d 11h"},
		{"hours and minutes", 3*time.Hour + 16*time.Minute, "3h 16m"},
		{"only minutes", 45 * time.Minute, "45m"},
		{"zero", 0, "0m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.input)
			if got != tt.expected {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestHandler_downsampleStep(t *testing.T) {
	tests := []struct {
		n, max, want int
	}{
		{100, 500, 1},  // No downsampling needed
		{1000, 500, 2}, // Need to reduce
		{0, 500, 1},    // Empty
		{500, 0, 1},    // Max 0
		{1500, 500, 3}, // ceil(1500/500) = 3
	}

	for _, tt := range tests {
		got := downsampleStep(tt.n, tt.max)
		if got != tt.want {
			t.Errorf("downsampleStep(%d, %d) = %d, want %d", tt.n, tt.max, got, tt.want)
		}
	}
}

func TestHandler_parseInsightsRange(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"", 7 * 24 * time.Hour},        // default
		{"invalid", 7 * 24 * time.Hour}, // default
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseInsightsRange(tt.input)
			if got != tt.want {
				t.Errorf("parseInsightsRange(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════
// ── Security Tests: MaxBytesReader and Error Sanitization ──
// ═══════════════════════════════════════════════════════════════════

func TestHandler_MaxBytesReader_RejectsLargeBody(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	cfg := createTestConfigWithSynthetic()
	h := NewHandler(s, nil, nil, nil, cfg)

	// Create valid JSON that exceeds 64KB when parsed
	// Use a key with a large string value to exceed the limit
	largeValue := strings.Repeat("x", 65*1024)
	largePayload := fmt.Sprintf(`{"timezone":"%s"}`, largeValue)

	tests := []struct {
		name    string
		method  string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{
			name:    "UpdateSettings PUT",
			method:  http.MethodPut,
			handler: h.UpdateSettings,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/settings", strings.NewReader(largePayload))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()

			tt.handler(rr, req)

			// MaxBytesReader returns 413 Entity Too Large for oversized bodies
			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("expected status %d (RequestEntityTooLarge), got %d", http.StatusRequestEntityTooLarge, rr.Code)
			}
		})
	}
}

func TestHandler_ApplyUpdate_SanitizesErrors(t *testing.T) {
	// Create a mock updater that will return an error
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)

	// The handler should sanitize internal errors
	// We'll test that the ApplyUpdate endpoint doesn't leak internal error details

	// Since we can't easily mock the updater, we test the 503 case (no updater configured)
	// which already returns a generic message
	req := httptest.NewRequest(http.MethodPost, "/api/update/apply", nil)
	rr := httptest.NewRecorder()

	h.ApplyUpdate(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rr.Code)
	}

	// Verify the error message is generic
	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if response["error"] != "updater not configured" {
		t.Errorf("expected generic error message, got %q", response["error"])
	}
}

// ═══════════════════════════════════════════════════════════════════
// ── Security Tests: Login Error Whitelist ──
// ═══════════════════════════════════════════════════════════════════

func TestLogin_WhitelistsErrorCodes(t *testing.T) {
	tests := []struct {
		name         string
		errorCode    string
		wantContains string
	}{
		{
			name:         "invalid error code shows whitelisted message",
			errorCode:    "invalid",
			wantContains: "Invalid username or password",
		},
		{
			name:         "expired error code shows whitelisted message",
			errorCode:    "expired",
			wantContains: "Session expired",
		},
		{
			name:         "required error code shows whitelisted message",
			errorCode:    "required",
			wantContains: "Authentication required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := createTestConfigWithSynthetic()
			h := NewHandler(nil, nil, nil, nil, cfg)

			req := httptest.NewRequest(http.MethodGet, "/login?error="+tt.errorCode, nil)
			rr := httptest.NewRecorder()
			h.Login(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("expected status 200, got %d", rr.Code)
			}

			body := rr.Body.String()
			if !strings.Contains(body, tt.wantContains) {
				t.Errorf("expected body to contain %q, got:\n%s", tt.wantContains, body)
			}
		})
	}
}

func TestLogin_RejectsUnknownErrorCode(t *testing.T) {
	cfg := createTestConfigWithSynthetic()
	h := NewHandler(nil, nil, nil, nil, cfg)

	// Unknown error code should result in empty error message
	req := httptest.NewRequest(http.MethodGet, "/login?error=malicious<script>alert(1)</script>", nil)
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	body := rr.Body.String()
	// The error should not contain the malicious input
	// Note: we check for the specific malicious pattern, not all <script> tags
	// since the template legitimately contains theme-toggle scripts
	if strings.Contains(body, "malicious") {
		t.Error("body should not contain unknown error code")
	}
	if strings.Contains(body, "alert(1)") {
		t.Error("body should not contain malicious script content")
	}
	// Verify the error-message div is not rendered for unknown codes
	if strings.Contains(body, `class="error-message"`) {
		t.Error("error-message div should not be rendered for unknown error codes")
	}
}
