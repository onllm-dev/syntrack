package notify

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"golang.org/x/crypto/hkdf"

	"crypto/sha256"
)

// PushSubscription represents a browser push subscription.
type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// PushSender sends Web Push notifications using VAPID authentication.
type PushSender struct {
	vapidPrivate *ecdsa.PrivateKey
	vapidPublic  []byte // uncompressed P-256 public key
	subject      string // mailto: or https: contact
	client       *http.Client
}

// GenerateVAPIDKeys creates a new ECDSA P-256 key pair and returns
// the public and private keys as base64url-encoded strings.
func GenerateVAPIDKeys() (publicKey, privateKey string, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("notify.GenerateVAPIDKeys: %w", err)
	}

	// Public key: uncompressed point (65 bytes)
	pubBytes := elliptic.MarshalCompressed(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y)
	// Actually we need uncompressed for Web Push
	pubBytes = elliptic.Marshal(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y)

	// Private key: raw 32-byte scalar
	privBytes := priv.D.Bytes()
	// Pad to 32 bytes
	if len(privBytes) < 32 {
		padded := make([]byte, 32)
		copy(padded[32-len(privBytes):], privBytes)
		privBytes = padded
	}

	publicKey = base64.RawURLEncoding.EncodeToString(pubBytes)
	privateKey = base64.RawURLEncoding.EncodeToString(privBytes)
	return publicKey, privateKey, nil
}

// NewPushSender creates a PushSender from base64url-encoded VAPID keys.
func NewPushSender(publicKeyB64, privateKeyB64, subject string) (*PushSender, error) {
	pubBytes, err := base64.RawURLEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return nil, fmt.Errorf("notify.NewPushSender: invalid public key: %w", err)
	}

	privBytes, err := base64.RawURLEncoding.DecodeString(privateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("notify.NewPushSender: invalid private key: %w", err)
	}

	// Reconstruct ECDSA private key
	curve := elliptic.P256()
	x, y := elliptic.Unmarshal(curve, pubBytes)
	if x == nil {
		return nil, fmt.Errorf("notify.NewPushSender: invalid public key point")
	}

	priv := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: curve,
			X:     x,
			Y:     y,
		},
		D: new(big.Int).SetBytes(privBytes),
	}

	return &PushSender{
		vapidPrivate: priv,
		vapidPublic:  pubBytes,
		subject:      subject,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{MaxIdleConns: 1, MaxIdleConnsPerHost: 1},
		},
	}, nil
}

// Send encrypts and sends a push notification to a subscription endpoint.
func (p *PushSender) Send(sub PushSubscription, title, body string) error {
	payload, err := json.Marshal(map[string]string{
		"title": title,
		"body":  body,
	})
	if err != nil {
		return fmt.Errorf("notify.PushSender.Send: marshal payload: %w", err)
	}

	// Decode subscription keys
	clientPubBytes, err := base64.RawURLEncoding.DecodeString(sub.Keys.P256dh)
	if err != nil {
		return fmt.Errorf("notify.PushSender.Send: decode p256dh: %w", err)
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	if err != nil {
		return fmt.Errorf("notify.PushSender.Send: decode auth: %w", err)
	}

	// Encrypt payload per RFC 8291
	encrypted, err := encryptPayload(payload, clientPubBytes, authSecret)
	if err != nil {
		return fmt.Errorf("notify.PushSender.Send: encrypt: %w", err)
	}

	// Create VAPID JWT
	jwt, err := createVAPIDJWT(sub.Endpoint, p.vapidPrivate)
	if err != nil {
		return fmt.Errorf("notify.PushSender.Send: VAPID JWT: %w", err)
	}

	vapidPubB64 := base64.RawURLEncoding.EncodeToString(p.vapidPublic)
	authHeader := fmt.Sprintf("vapid t=%s, k=%s", jwt, vapidPubB64)

	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(encrypted))
	if err != nil {
		return fmt.Errorf("notify.PushSender.Send: create request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "86400")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("notify.PushSender.Send: HTTP POST: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("notify.PushSender.Send: push service returned %d", resp.StatusCode)
	}

	return nil
}

// encryptPayload implements RFC 8291 (Message Encryption for Web Push) using aes128gcm.
func encryptPayload(payload, clientPubBytes, authSecret []byte) ([]byte, error) {
	// Generate ephemeral ECDH key pair
	ephPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ephemeral key: %w", err)
	}
	ephPub := ephPriv.PublicKey().Bytes()

	// Import client public key for ECDH
	clientPub, err := ecdh.P256().NewPublicKey(clientPubBytes)
	if err != nil {
		return nil, fmt.Errorf("import client public key: %w", err)
	}

	// ECDH shared secret
	sharedSecret, err := ephPriv.ECDH(clientPub)
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}

	// RFC 8291 Section 3.4: IKM = HKDF(auth_secret, shared_secret, "WebPush: info" || 0x00 || client_pub || server_pub, 32)
	infoIKM := buildInfo("WebPush: info", clientPubBytes, ephPub)
	ikm, err := hkdfDerive(authSecret, sharedSecret, infoIKM, 32)
	if err != nil {
		return nil, fmt.Errorf("derive IKM: %w", err)
	}

	// Generate 16-byte salt
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	// Derive content encryption key and nonce
	cek, err := hkdfDerive(salt, ikm, buildCEKInfo("Content-Encoding: aes128gcm\x00\x01"), 16)
	if err != nil {
		return nil, fmt.Errorf("derive CEK: %w", err)
	}
	nonce, err := hkdfDerive(salt, ikm, buildCEKInfo("Content-Encoding: nonce\x00\x01"), 12)
	if err != nil {
		return nil, fmt.Errorf("derive nonce: %w", err)
	}

	// Pad payload: append 0x02 delimiter (RFC 8188 padding)
	padded := append(payload, 0x02)

	// Encrypt with AES-128-GCM
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("GCM: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, padded, nil)

	// Build aes128gcm header: salt (16) || rs (4) || idlen (1) || keyid (65) || ciphertext
	recordSize := uint32(len(ciphertext) + 16 + 4 + 1 + len(ephPub))
	var header bytes.Buffer
	header.Write(salt)
	binary.Write(&header, binary.BigEndian, recordSize)
	header.WriteByte(byte(len(ephPub)))
	header.Write(ephPub)
	header.Write(ciphertext)

	return header.Bytes(), nil
}

// buildInfo creates the info parameter for IKM derivation per RFC 8291.
func buildInfo(label string, clientPub, serverPub []byte) []byte {
	var info bytes.Buffer
	info.WriteString(label)
	info.WriteByte(0x00)
	// Client public key (with 2-byte length prefix)
	binary.Write(&info, binary.BigEndian, uint16(len(clientPub)))
	info.Write(clientPub)
	// Server public key (with 2-byte length prefix)
	binary.Write(&info, binary.BigEndian, uint16(len(serverPub)))
	info.Write(serverPub)
	return info.Bytes()
}

// buildCEKInfo creates the info parameter for CEK/nonce derivation.
func buildCEKInfo(label string) []byte {
	return []byte(label)
}

// hkdfDerive performs HKDF-SHA256 extract and expand.
func hkdfDerive(salt, ikm, info []byte, length int) ([]byte, error) {
	reader := hkdf.New(sha256.New, ikm, salt, info)
	out := make([]byte, length)
	if _, err := io.ReadFull(reader, out); err != nil {
		return nil, err
	}
	return out, nil
}

// createVAPIDJWT creates a VAPID JWT (RFC 8292) signed with the VAPID private key.
func createVAPIDJWT(endpoint string, key *ecdsa.PrivateKey) (string, error) {
	// Parse origin from endpoint
	origin := extractOrigin(endpoint)

	// JWT header: {"typ":"JWT","alg":"ES256"}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))

	// JWT claims
	now := time.Now().Unix()
	claims := fmt.Sprintf(`{"aud":"%s","exp":%d,"sub":"mailto:onwatch@localhost"}`, origin, now+86400)
	claimsB64 := base64.RawURLEncoding.EncodeToString([]byte(claims))

	// Sign
	signingInput := header + "." + claimsB64
	hash := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, hash[:])
	if err != nil {
		return "", fmt.Errorf("sign JWT: %w", err)
	}

	// Convert r, s to fixed-size 32-byte big-endian
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	sig := make([]byte, 64)
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):], sBytes)

	sigB64 := base64.RawURLEncoding.EncodeToString(sig)
	return signingInput + "." + sigB64, nil
}

// extractOrigin returns the scheme + host from a URL.
func extractOrigin(url string) string {
	// Find the third slash (end of scheme://host)
	count := 0
	for i, c := range url {
		if c == '/' {
			count++
			if count == 3 {
				return url[:i]
			}
		}
	}
	return url
}
