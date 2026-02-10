package notify

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// encryptedPrefix marks values that have been encrypted.
// This allows explicit identification of encrypted vs plaintext values.
const encryptedPrefix = "enc:"

// GenerateEncryptionKey returns a random 32-byte key as a hex string (64 chars).
func GenerateEncryptionKey() (string, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", fmt.Errorf("notify.GenerateEncryptionKey: %w", err)
	}
	return hex.EncodeToString(key), nil
}

// Encrypt encrypts plaintext using AES-256-GCM with the given hex key.
// Returns base64(nonce + ciphertext).
// Optional aad (Additional Authenticated Data) binds the ciphertext to a specific context.
func Encrypt(plaintext, keyHex string, aad ...string) (string, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", fmt.Errorf("notify.Encrypt: invalid key hex: %w", err)
	}
	if len(key) != 32 {
		return "", fmt.Errorf("notify.Encrypt: key must be 32 bytes, got %d", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("notify.Encrypt: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("notify.Encrypt: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("notify.Encrypt: %w", err)
	}

	// Use AAD if provided for context binding
	var additionalData []byte
	if len(aad) > 0 && aad[0] != "" {
		additionalData = []byte(aad[0])
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), additionalData)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a base64(nonce + ciphertext) string using AES-256-GCM.
// Optional aad must match the value used during encryption.
func Decrypt(encoded, keyHex string, aad ...string) (string, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", fmt.Errorf("notify.Decrypt: invalid key hex: %w", err)
	}
	if len(key) != 32 {
		return "", fmt.Errorf("notify.Decrypt: key must be 32 bytes, got %d", len(key))
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("notify.Decrypt: invalid base64: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("notify.Decrypt: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("notify.Decrypt: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("notify.Decrypt: ciphertext too short")
	}

	// Use AAD if provided for context binding
	var additionalData []byte
	if len(aad) > 0 && aad[0] != "" {
		additionalData = []byte(aad[0])
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, additionalData)
	if err != nil {
		return "", fmt.Errorf("notify.Decrypt: %w", err)
	}

	return string(plaintext), nil
}

// EncryptForStorage encrypts plaintext and returns it with the encrypted prefix.
// Format: "enc:" + base64(nonce + ciphertext)
func EncryptForStorage(plaintext, keyHex string) (string, error) {
	ciphertext, err := Encrypt(plaintext, keyHex)
	if err != nil {
		return "", err
	}
	return encryptedPrefix + ciphertext, nil
}

// DecryptFromStorage checks for the encrypted prefix and decrypts if present.
// If no prefix is found, returns the value as-is (plaintext).
func DecryptFromStorage(stored, keyHex string) (string, error) {
	if !strings.HasPrefix(stored, encryptedPrefix) {
		// No prefix - treat as plaintext
		return stored, nil
	}
	// Strip prefix and decrypt
	ciphertext := strings.TrimPrefix(stored, encryptedPrefix)
	return Decrypt(ciphertext, keyHex)
}

// IsEncryptedValue checks if a string has the encrypted prefix.
func IsEncryptedValue(value string) bool {
	return strings.HasPrefix(value, encryptedPrefix)
}
