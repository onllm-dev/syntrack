package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
)

func insertMiniMaxSampleData(t *testing.T, s *store.Store) {
	t.Helper()
	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)
	snap := &api.MiniMaxSnapshot{
		CapturedAt: now,
		RawJSON:    `{"base_resp":{"status_code":0}}`,
		Models: []api.MiniMaxModelQuota{
			{ModelName: "MiniMax-M1", Total: 200, Remain: 158, Used: 42, UsedPercent: 21, ResetAt: &resetAt, TimeUntilReset: 2 * time.Hour},
			{ModelName: "MiniMax-Text-01", Total: 100, Remain: 80, Used: 20, UsedPercent: 20, ResetAt: &resetAt, TimeUntilReset: 2 * time.Hour},
		},
	}
	if _, err := s.InsertMiniMaxSnapshot(snap); err != nil {
		t.Fatalf("InsertMiniMaxSnapshot: %v", err)
	}
}

func TestHandler_Current_WithMiniMaxProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()
	insertMiniMaxSampleData(t, s)

	tr := tracker.NewMiniMaxTracker(s, nil)
	cfg := createTestConfigWithMiniMax()
	h := NewHandler(s, nil, nil, nil, cfg)
	h.SetMiniMaxTracker(tr)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=minimax", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	quotas, ok := response["quotas"].([]interface{})
	if !ok {
		t.Fatalf("expected quotas array, got %T", response["quotas"])
	}
	if len(quotas) != 2 {
		t.Fatalf("expected 2 quotas, got %d", len(quotas))
	}
}

func TestHandler_Current_BothIncludesMiniMax(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()
	insertMiniMaxSampleData(t, s)

	tr := tracker.NewMiniMaxTracker(s, nil)
	cfg := createTestConfigWithAll()
	h := NewHandler(s, nil, nil, nil, cfg)
	h.SetMiniMaxTracker(tr)

	req := httptest.NewRequest(http.MethodGet, "/api/current?provider=both", nil)
	rr := httptest.NewRecorder()
	h.Current(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if _, ok := response["minimax"]; !ok {
		t.Error("expected minimax field in both response")
	}
}

func TestHandler_History_WithMiniMaxProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()
	insertMiniMaxSampleData(t, s)

	cfg := createTestConfigWithMiniMax()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/history?provider=minimax&range=24h", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if len(response) == 0 {
		t.Fatal("expected non-empty history response")
	}
}

func TestHandler_Cycles_WithMiniMaxProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)
	if _, err := s.CreateMiniMaxCycle("MiniMax-M1", now.Add(-time.Hour), &resetAt); err != nil {
		t.Fatalf("CreateMiniMaxCycle: %v", err)
	}
	if err := s.UpdateMiniMaxCycle("MiniMax-M1", 80, 30); err != nil {
		t.Fatalf("UpdateMiniMaxCycle: %v", err)
	}

	cfg := createTestConfigWithMiniMax()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycles?provider=minimax&type=MiniMax-M1", nil)
	rr := httptest.NewRecorder()
	h.Cycles(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if len(response) == 0 {
		t.Fatal("expected non-empty cycles response")
	}
}

func TestHandler_Summary_WithMiniMaxProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()
	insertMiniMaxSampleData(t, s)

	tr := tracker.NewMiniMaxTracker(s, nil)
	snap, _ := s.QueryLatestMiniMax()
	if snap != nil {
		_ = tr.Process(snap)
	}

	cfg := createTestConfigWithMiniMax()
	h := NewHandler(s, nil, nil, nil, cfg)
	h.SetMiniMaxTracker(tr)

	req := httptest.NewRequest(http.MethodGet, "/api/summary?provider=minimax", nil)
	rr := httptest.NewRecorder()
	h.Summary(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if _, ok := response["MiniMax-M1"]; !ok {
		t.Error("expected MiniMax-M1 summary")
	}
}

func TestHandler_Insights_WithMiniMaxProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()
	insertMiniMaxSampleData(t, s)

	cfg := createTestConfigWithMiniMax()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/insights?provider=minimax", nil)
	rr := httptest.NewRecorder()
	h.Insights(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
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

func TestHandler_CycleOverview_WithMiniMaxProvider(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()
	insertMiniMaxSampleData(t, s)

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)
	_, _ = s.CreateMiniMaxCycle("MiniMax-M1", now.Add(-30*time.Minute), &resetAt)
	_ = s.UpdateMiniMaxCycle("MiniMax-M1", 42, 42)

	cfg := createTestConfigWithMiniMax()
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/cycle-overview?provider=minimax&groupBy=MiniMax-M1&limit=10", nil)
	rr := httptest.NewRecorder()
	h.CycleOverview(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if response["provider"] != "minimax" {
		t.Errorf("expected provider minimax, got %v", response["provider"])
	}
}

func TestHandler_CyclesMiniMax_DefaultType_UsesMiniMaxM2(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)
	if _, err := s.CreateMiniMaxCycle("MiniMax-M2", now.Add(-time.Hour), &resetAt); err != nil {
		t.Fatalf("CreateMiniMaxCycle: %v", err)
	}
	if err := s.UpdateMiniMaxCycle("MiniMax-M2", 90, 25); err != nil {
		t.Fatalf("UpdateMiniMaxCycle: %v", err)
	}

	h := NewHandler(s, nil, nil, nil, createTestConfigWithMiniMax())
	req := httptest.NewRequest(http.MethodGet, "/api/cycles?provider=minimax", nil)
	rr := httptest.NewRecorder()
	h.Cycles(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if len(response) == 0 {
		t.Fatal("expected non-empty cycles response")
	}
	if response[0]["quotaName"] != "MiniMax-M2" {
		t.Fatalf("expected default minimax cycle model MiniMax-M2, got %v", response[0]["quotaName"])
	}
}

func TestHandler_CycleOverviewMiniMax_DefaultGroupBy_UsesMiniMaxM2(t *testing.T) {
	s, _ := store.New(":memory:")
	defer s.Close()
	insertMiniMaxSampleData(t, s)

	h := NewHandler(s, nil, nil, nil, createTestConfigWithMiniMax())
	req := httptest.NewRequest(http.MethodGet, "/api/cycle-overview?provider=minimax&limit=10", nil)
	rr := httptest.NewRecorder()
	h.CycleOverview(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if response["groupBy"] != "MiniMax-M2" {
		t.Fatalf("expected default groupBy MiniMax-M2, got %v", response["groupBy"])
	}
}
