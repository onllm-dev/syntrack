package testutil

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMockServer_DefaultSyntheticRoute(t *testing.T) {
	ms := NewMockServer(t, WithSyntheticKey("syn_test123"))
	defer ms.Close()

	req, _ := http.NewRequest("GET", ms.URL+"/v2/quotas", nil)
	req.Header.Set("Authorization", "Bearer syn_test123")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if _, ok := result["subscription"]; !ok {
		t.Error("response missing 'subscription' key")
	}
}

func TestMockServer_SyntheticAuthRejectsInvalidKey(t *testing.T) {
	ms := NewMockServer(t, WithSyntheticKey("syn_correct"))
	defer ms.Close()

	req, _ := http.NewRequest("GET", ms.URL+"/v2/quotas", nil)
	req.Header.Set("Authorization", "Bearer syn_wrong")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestMockServer_DefaultZaiRoute(t *testing.T) {
	ms := NewMockServer(t, WithZaiKey("zai_test_key"))
	defer ms.Close()

	req, _ := http.NewRequest("GET", ms.URL+"/monitor/usage/quota/limit", nil)
	req.Header.Set("Authorization", "zai_test_key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	code, ok := result["code"]
	if !ok {
		t.Fatal("response missing 'code' key")
	}
	if int(code.(float64)) != 200 {
		t.Errorf("expected code 200, got %v", code)
	}
}

func TestMockServer_ZaiAuthRejectsInvalidKey(t *testing.T) {
	ms := NewMockServer(t, WithZaiKey("correct_key"))
	defer ms.Close()

	req, _ := http.NewRequest("GET", ms.URL+"/monitor/usage/quota/limit", nil)
	req.Header.Set("Authorization", "wrong_key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	// Z.ai returns 200 with code 401 in body
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if int(result["code"].(float64)) != 401 {
		t.Error("expected code 401 in body for invalid Z.ai key")
	}
}

func TestMockServer_DefaultAnthropicRoute(t *testing.T) {
	ms := NewMockServer(t, WithAnthropicToken("anth_token_123"))
	defer ms.Close()

	req, _ := http.NewRequest("GET", ms.URL+"/api/oauth/usage", nil)
	req.Header.Set("Authorization", "Bearer anth_token_123")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if _, ok := result["five_hour"]; !ok {
		t.Error("response missing 'five_hour' key")
	}
}

func TestMockServer_AnthropicAuthRejectsInvalidToken(t *testing.T) {
	ms := NewMockServer(t, WithAnthropicToken("correct_token"))
	defer ms.Close()

	req, _ := http.NewRequest("GET", ms.URL+"/api/oauth/usage", nil)
	req.Header.Set("Authorization", "Bearer wrong_token")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestMockServer_ResponseSequenceCycles(t *testing.T) {
	responses := SyntheticResponseSequence(3)
	ms := NewMockServer(t,
		WithSyntheticKey("syn_test"),
		WithSyntheticResponses(responses),
	)
	defer ms.Close()

	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest("GET", ms.URL+"/v2/quotas", nil)
		req.Header.Set("Authorization", "Bearer syn_test")

		resp, _ := http.DefaultClient.Do(req)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		sub := result["subscription"].(map[string]interface{})
		requests := sub["requests"].(float64)

		// Sequence: 100, 110, 120, 100, 110 (cycles back)
		expectedIdx := i % 3
		expected := 100.0 + float64(expectedIdx)*10
		if requests != expected {
			t.Errorf("request %d: expected requests=%f, got %f", i, expected, requests)
		}
	}
}

func TestMockServer_SetErrorInjectsSyntheticError(t *testing.T) {
	ms := NewMockServer(t, WithSyntheticKey("syn_test"))
	defer ms.Close()

	ms.SetSyntheticError(503)

	req, _ := http.NewRequest("GET", ms.URL+"/v2/quotas", nil)
	req.Header.Set("Authorization", "Bearer syn_test")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 503 {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

func TestMockServer_ClearErrors(t *testing.T) {
	ms := NewMockServer(t, WithSyntheticKey("syn_test"))
	defer ms.Close()

	ms.SetSyntheticError(500)
	ms.ClearErrors()

	req, _ := http.NewRequest("GET", ms.URL+"/v2/quotas", nil)
	req.Header.Set("Authorization", "Bearer syn_test")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after ClearErrors, got %d", resp.StatusCode)
	}
}

func TestMockServer_RequestCount(t *testing.T) {
	ms := NewMockServer(t, WithSyntheticKey("syn_test"))
	defer ms.Close()

	if ms.RequestCount("synthetic") != 0 {
		t.Fatal("expected 0 requests initially")
	}

	req, _ := http.NewRequest("GET", ms.URL+"/v2/quotas", nil)
	req.Header.Set("Authorization", "Bearer syn_test")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	if ms.RequestCount("synthetic") != 1 {
		t.Fatalf("expected 1 request, got %d", ms.RequestCount("synthetic"))
	}

	// Second request
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()

	if ms.RequestCount("synthetic") != 2 {
		t.Fatalf("expected 2 requests, got %d", ms.RequestCount("synthetic"))
	}
}

func TestMockServer_AdminScenarioEndpoint(t *testing.T) {
	ms := NewMockServer(t, WithSyntheticKey("syn_test"))
	defer ms.Close()

	// Switch to a new response sequence via admin endpoint
	newResp := SyntheticResponseSequence(2)
	payload, _ := json.Marshal(map[string]interface{}{
		"provider":  "synthetic",
		"responses": newResp,
	})

	req, _ := http.NewRequest("POST", ms.URL+"/admin/scenario", strings.NewReader(string(payload)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestMockServer_AdminErrorEndpoint(t *testing.T) {
	ms := NewMockServer(t, WithSyntheticKey("syn_test"))
	defer ms.Close()

	payload, _ := json.Marshal(map[string]interface{}{
		"provider":    "synthetic",
		"status_code": 503,
	})

	req, _ := http.NewRequest("POST", ms.URL+"/admin/error", strings.NewReader(string(payload)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Now the synthetic endpoint should return the error
	req, _ = http.NewRequest("GET", ms.URL+"/v2/quotas", nil)
	req.Header.Set("Authorization", "Bearer syn_test")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()

	if resp.StatusCode != 503 {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

func TestMockServer_AdminRequestsEndpoint(t *testing.T) {
	ms := NewMockServer(t,
		WithSyntheticKey("syn_test"),
		WithAnthropicToken("anth_tok"),
	)
	defer ms.Close()

	// Make some requests
	req, _ := http.NewRequest("GET", ms.URL+"/v2/quotas", nil)
	req.Header.Set("Authorization", "Bearer syn_test")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	req, _ = http.NewRequest("GET", ms.URL+"/api/oauth/usage", nil)
	req.Header.Set("Authorization", "Bearer anth_tok")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()

	// Query the request log
	req, _ = http.NewRequest("GET", ms.URL+"/admin/requests", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	synCount := int(result["synthetic"].(float64))
	anthCount := int(result["anthropic"].(float64))

	if synCount != 1 {
		t.Errorf("expected 1 synthetic request, got %d", synCount)
	}
	if anthCount != 1 {
		t.Errorf("expected 1 anthropic request, got %d", anthCount)
	}
}

func TestMockServer_SetAnthropicToken(t *testing.T) {
	ms := NewMockServer(t, WithAnthropicToken("old_token"))
	defer ms.Close()

	// Request with old token should work
	req, _ := http.NewRequest("GET", ms.URL+"/api/oauth/usage", nil)
	req.Header.Set("Authorization", "Bearer old_token")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("old token should work, got %d", resp.StatusCode)
	}

	// Change the token
	ms.SetAnthropicToken("new_token")

	// Old token should now fail
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("old token should fail after SetAnthropicToken, got %d", resp.StatusCode)
	}

	// New token should work
	req.Header.Set("Authorization", "Bearer new_token")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("new token should work, got %d", resp.StatusCode)
	}
}

func TestMockServer_SetZaiError(t *testing.T) {
	ms := NewMockServer(t, WithZaiKey("zai_key"))
	defer ms.Close()

	ms.SetZaiError(500)

	req, _ := http.NewRequest("GET", ms.URL+"/monitor/usage/quota/limit", nil)
	req.Header.Set("Authorization", "zai_key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 500 {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestMockServer_SetAnthropicError(t *testing.T) {
	ms := NewMockServer(t, WithAnthropicToken("tok"))
	defer ms.Close()

	ms.SetAnthropicError(429)

	req, _ := http.NewRequest("GET", ms.URL+"/api/oauth/usage", nil)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 429 {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}
}

func TestMockServer_UnknownRouteReturns404(t *testing.T) {
	ms := NewMockServer(t)
	defer ms.Close()

	resp, err := http.DefaultClient.Get(ms.URL + "/nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestMockServer_AllProvidersSimultaneously(t *testing.T) {
	ms := NewMockServer(t,
		WithSyntheticKey("syn_k"),
		WithZaiKey("zai_k"),
		WithAnthropicToken("anth_t"),
	)
	defer ms.Close()

	// Synthetic
	req, _ := http.NewRequest("GET", ms.URL+"/v2/quotas", nil)
	req.Header.Set("Authorization", "Bearer syn_k")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Errorf("synthetic: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Z.ai
	req, _ = http.NewRequest("GET", ms.URL+"/monitor/usage/quota/limit", nil)
	req.Header.Set("Authorization", "zai_k")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Errorf("zai: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Anthropic
	req, _ = http.NewRequest("GET", ms.URL+"/api/oauth/usage", nil)
	req.Header.Set("Authorization", "Bearer anth_t")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Errorf("anthropic: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	if ms.RequestCount("synthetic") != 1 {
		t.Errorf("expected 1 synthetic, got %d", ms.RequestCount("synthetic"))
	}
	if ms.RequestCount("zai") != 1 {
		t.Errorf("expected 1 zai, got %d", ms.RequestCount("zai"))
	}
	if ms.RequestCount("anthropic") != 1 {
		t.Errorf("expected 1 anthropic, got %d", ms.RequestCount("anthropic"))
	}
}
