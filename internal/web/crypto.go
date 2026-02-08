package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/onllm-dev/onwatch/internal/notify"
)

// DeriveEncryptionKey derives a 32-byte encryption key from the admin password hash.
// The password hash is expected to be a SHA-256 hex string (64 characters).
// Returns a hex-encoded 32-byte key suitable for AES-256-GCM.
func DeriveEncryptionKey(passwordHash string) string {
	// The passwordHash is already SHA-256 hex (64 chars = 32 bytes)
	// We use it directly as the encryption key
	if len(passwordHash) == 64 {
		return passwordHash
	}

	// Fallback: if somehow we get a non-hex password, hash it again
	h := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(h[:])
}

// IsEncryptedValue checks if a string looks like an encrypted value
// (base64 encoded with minimum length for nonce + ciphertext)
func IsEncryptedValue(value string) bool {
	if value == "" {
		return false
	}

	// Encrypted values are base64 encoded and typically longer than plaintext
	// Minimum: 12 bytes nonce + 1 byte ciphertext + base64 overhead
	if len(value) < 24 {
		return false
	}

	// Check if it looks like base64 (contains only base64 chars)
	base64Chars := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/="
	for _, c := range value {
		if !strings.ContainsRune(base64Chars, c) {
			return false
		}
	}

	return true
}

// ReEncryptAllData re-encrypts all encrypted data in the database when password changes.
// It uses the old key to decrypt and the new key to re-encrypt.
// Returns a map of any errors that occurred (key = setting name, value = error message).
func ReEncryptAllData(store interface {
	GetSetting(key string) (string, error)
	SetSetting(key, value string) error
}, oldPasswordHash, newPasswordHash string) map[string]string {
	errors := make(map[string]string)

	oldKey := DeriveEncryptionKey(oldPasswordHash)
	newKey := DeriveEncryptionKey(newPasswordHash)

	// If keys are the same (shouldn't happen, but safety check), skip
	if oldKey == newKey {
		return errors
	}

	// Re-encrypt SMTP password
	if err := reEncryptSMTPPassword(store, oldKey, newKey); err != nil {
		errors["smtp"] = err.Error()
	}

	return errors
}

// reEncryptSMTPPassword re-encrypts the SMTP password when admin password changes.
func reEncryptSMTPPassword(store interface {
	GetSetting(key string) (string, error)
	SetSetting(key, value string) error
}, oldKey, newKey string) error {
	smtpJSON, err := store.GetSetting("smtp")
	if err != nil || smtpJSON == "" {
		return nil // No SMTP settings to re-encrypt
	}

	// Parse SMTP settings
	var smtpSettings map[string]interface{}
	if err := json.Unmarshal([]byte(smtpJSON), &smtpSettings); err != nil {
		return fmt.Errorf("failed to parse SMTP settings: %w", err)
	}

	passwordVal, ok := smtpSettings["password"]
	if !ok || passwordVal == nil {
		return nil // No password to re-encrypt
	}

	encryptedPass, ok := passwordVal.(string)
	if !ok || encryptedPass == "" {
		return nil // No password to re-encrypt
	}

	// Check if the password is already encrypted
	if !IsEncryptedValue(encryptedPass) {
		// It's plaintext, encrypt it with the new key
		newEncrypted, err := notify.Encrypt(encryptedPass, newKey)
		if err != nil {
			return fmt.Errorf("failed to encrypt SMTP password: %w", err)
		}
		smtpSettings["password"] = newEncrypted
	} else {
		// It's encrypted, decrypt with old key and re-encrypt with new key
		plaintext, err := notify.Decrypt(encryptedPass, oldKey)
		if err != nil {
			// If decryption fails with old key, try with new key (might already be re-encrypted)
			_, tryNewErr := notify.Decrypt(encryptedPass, newKey)
			if tryNewErr == nil {
				// Already encrypted with new key, nothing to do
				return nil
			}
			return fmt.Errorf("failed to decrypt SMTP password with old key: %w", err)
		}

		// Re-encrypt with new key
		newEncrypted, err := notify.Encrypt(plaintext, newKey)
		if err != nil {
			return fmt.Errorf("failed to re-encrypt SMTP password: %w", err)
		}
		smtpSettings["password"] = newEncrypted
	}

	// Save updated settings
	newJSON, err := json.Marshal(smtpSettings)
	if err != nil {
		return fmt.Errorf("failed to marshal SMTP settings: %w", err)
	}

	if err := store.SetSetting("smtp", string(newJSON)); err != nil {
		return fmt.Errorf("failed to save SMTP settings: %w", err)
	}

	return nil
}
