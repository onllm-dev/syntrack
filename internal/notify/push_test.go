package notify

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGenerateVAPIDKeys(t *testing.T) {
	pub, priv, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("GenerateVAPIDKeys() error: %v", err)
	}

	// Public key should be 65 bytes (uncompressed P-256 point)
	pubBytes, err := base64.RawURLEncoding.DecodeString(pub)
	if err != nil {
		t.Fatalf("decode public key: %v", err)
	}
	if len(pubBytes) != 65 {
		t.Errorf("public key length = %d, want 65", len(pubBytes))
	}
	if pubBytes[0] != 0x04 {
		t.Errorf("public key prefix = 0x%02x, want 0x04 (uncompressed)", pubBytes[0])
	}

	// Private key should be 32 bytes
	privBytes, err := base64.RawURLEncoding.DecodeString(priv)
	if err != nil {
		t.Fatalf("decode private key: %v", err)
	}
	if len(privBytes) != 32 {
		t.Errorf("private key length = %d, want 32", len(privBytes))
	}

	// Keys should be valid on P-256 curve
	x, y := elliptic.Unmarshal(elliptic.P256(), pubBytes)
	if x == nil {
		t.Fatal("public key is not a valid P-256 point")
	}

	// Verify private key matches public key
	curve := elliptic.P256()
	expectedX, expectedY := curve.ScalarBaseMult(privBytes)
	if x.Cmp(expectedX) != 0 || y.Cmp(expectedY) != 0 {
		t.Error("private key does not correspond to public key")
	}
}

func TestGenerateVAPIDKeys_Unique(t *testing.T) {
	pub1, _, _ := GenerateVAPIDKeys()
	pub2, _, _ := GenerateVAPIDKeys()
	if pub1 == pub2 {
		t.Error("two calls to GenerateVAPIDKeys produced identical keys")
	}
}

func TestNewPushSender(t *testing.T) {
	pub, priv, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("GenerateVAPIDKeys: %v", err)
	}

	sender, err := NewPushSender(pub, priv, "mailto:test@example.com")
	if err != nil {
		t.Fatalf("NewPushSender: %v", err)
	}
	if sender.vapidPrivate == nil {
		t.Error("vapidPrivate is nil")
	}
	if len(sender.vapidPublic) != 65 {
		t.Errorf("vapidPublic length = %d, want 65", len(sender.vapidPublic))
	}
}

func TestNewPushSender_InvalidKeys(t *testing.T) {
	tests := []struct {
		name string
		pub  string
		priv string
	}{
		{"invalid base64 public", "!!!invalid!!!", "AAAA"},
		{"invalid base64 private", "AAAA", "!!!invalid!!!"},
		{"invalid public key point", base64.RawURLEncoding.EncodeToString(make([]byte, 65)), base64.RawURLEncoding.EncodeToString(make([]byte, 32))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewPushSender(tt.pub, tt.priv, "mailto:test@example.com")
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestCreateVAPIDJWT(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	jwt, err := createVAPIDJWT("https://fcm.googleapis.com/push/abc123", key)
	if err != nil {
		t.Fatalf("createVAPIDJWT: %v", err)
	}

	// JWT should have 3 parts
	parts := splitJWT(jwt)
	if len(parts) != 3 {
		t.Fatalf("JWT parts = %d, want 3", len(parts))
	}

	// Verify signature
	signingInput := parts[0] + "." + parts[1]
	hash := sha256.Sum256([]byte(signingInput))
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if len(sigBytes) != 64 {
		t.Fatalf("signature length = %d, want 64", len(sigBytes))
	}

	r := new(big.Int).SetBytes(sigBytes[:32])
	s := new(big.Int).SetBytes(sigBytes[32:])
	if !ecdsa.Verify(&key.PublicKey, hash[:], r, s) {
		t.Error("JWT signature verification failed")
	}
}

func splitJWT(jwt string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(jwt); i++ {
		if jwt[i] == '.' {
			parts = append(parts, jwt[start:i])
			start = i + 1
		}
	}
	parts = append(parts, jwt[start:])
	return parts
}

func TestExtractOrigin(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://fcm.googleapis.com/push/abc", "https://fcm.googleapis.com"},
		{"https://updates.push.services.mozilla.com/wpush/v2/abc", "https://updates.push.services.mozilla.com"},
		{"https://example.com", "https://example.com"},
	}
	for _, tt := range tests {
		got := extractOrigin(tt.input)
		if got != tt.want {
			t.Errorf("extractOrigin(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestEncryptPayload(t *testing.T) {
	// Generate a client key pair (simulating the browser)
	clientPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	clientPubBytes := clientPriv.PublicKey().Bytes()

	// Generate auth secret
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatalf("generate auth secret: %v", err)
	}

	payload := []byte(`{"title":"Test","body":"Hello"}`)
	encrypted, err := encryptPayload(payload, clientPubBytes, authSecret)
	if err != nil {
		t.Fatalf("encryptPayload: %v", err)
	}

	// Encrypted output should be non-empty and longer than original payload
	if len(encrypted) == 0 {
		t.Error("encrypted output is empty")
	}
	if len(encrypted) <= len(payload) {
		t.Errorf("encrypted length (%d) should be greater than payload length (%d)", len(encrypted), len(payload))
	}

	// Verify header structure: salt (16) + rs (4) + idlen (1) + keyid (65) + ciphertext
	if len(encrypted) < 86 { // 16 + 4 + 1 + 65 minimum
		t.Fatalf("encrypted too short: %d bytes", len(encrypted))
	}
	// idlen should be 65 (uncompressed P-256 point)
	idlen := encrypted[20]
	if idlen != 65 {
		t.Errorf("keyid length = %d, want 65", idlen)
	}
}

func TestPushSender_Send(t *testing.T) {
	var receivedRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRequests.Add(1)

		// Verify headers
		if r.Header.Get("Content-Encoding") != "aes128gcm" {
			t.Errorf("Content-Encoding = %q, want aes128gcm", r.Header.Get("Content-Encoding"))
		}
		if r.Header.Get("TTL") != "86400" {
			t.Errorf("TTL = %q, want 86400", r.Header.Get("TTL"))
		}
		auth := r.Header.Get("Authorization")
		if auth == "" {
			t.Error("missing Authorization header")
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}

		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	pub, priv, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("GenerateVAPIDKeys: %v", err)
	}

	sender, err := NewPushSender(pub, priv, "mailto:test@example.com")
	if err != nil {
		t.Fatalf("NewPushSender: %v", err)
	}

	// Generate fake subscription keys
	clientPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	authSecret := make([]byte, 16)
	rand.Read(authSecret)

	sub := PushSubscription{
		Endpoint: server.URL + "/push",
	}
	sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(clientPriv.PublicKey().Bytes())
	sub.Keys.Auth = base64.RawURLEncoding.EncodeToString(authSecret)

	err = sender.Send(sub, "Test Title", "Test Body")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if receivedRequests.Load() != 1 {
		t.Errorf("received %d requests, want 1", receivedRequests.Load())
	}
}

func TestPushSender_Send_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone) // 410 = subscription expired
	}))
	defer server.Close()

	pub, priv, _ := GenerateVAPIDKeys()
	sender, _ := NewPushSender(pub, priv, "mailto:test@example.com")

	clientPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	authSecret := make([]byte, 16)
	rand.Read(authSecret)

	sub := PushSubscription{Endpoint: server.URL + "/push"}
	sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(clientPriv.PublicKey().Bytes())
	sub.Keys.Auth = base64.RawURLEncoding.EncodeToString(authSecret)

	err := sender.Send(sub, "Test", "Body")
	if err == nil {
		t.Error("expected error for 410 response, got nil")
	}
}
