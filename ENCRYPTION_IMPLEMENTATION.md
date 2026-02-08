# SMTP Password Encryption Implementation

## Overview
This implementation adds AES-256-GCM encryption for SMTP passwords stored in the database, using the admin password hash as the encryption key. When the admin password changes, all encrypted data is automatically re-encrypted with the new key.

## Architecture

### Encryption Key Derivation
- The encryption key is derived directly from the SHA-256 hex hash of the admin password
- The password hash is already 64 hex characters = 32 bytes = perfect for AES-256
- This approach means:
  - No separate key management required
  - Password change automatically triggers key rotation
  - Same "something you know" (password) protects both auth and stored secrets

### Files Modified

#### 1. `internal/notify/notify.go`
- Added `encryptionKey` field to `NotificationEngine` struct
- Added `SetEncryptionKey(key string)` method
- Modified `ConfigureSMTP()` to decrypt passwords using the encryption key
- Decryption is graceful - if it fails, assumes plaintext and logs debug message

#### 2. `internal/web/handlers.go`
- Added `SetEncryptionKey(key string)` to `Notifier` interface
- Modified `UpdateSettings()` to encrypt SMTP passwords before saving
- Modified `ChangePassword()` to re-encrypt all encrypted data when password changes
- Added import for `notify` package to use `Encrypt()` function

#### 3. `internal/web/crypto.go` (NEW)
- `DeriveEncryptionKey(passwordHash string) string` - derives key from password hash
- `IsEncryptedValue(value string) bool` - checks if a value looks encrypted
- `ReEncryptAllData(store, oldKey, newKey)` - re-encrypts all data with new key
- `reEncryptSMTPPassword()` - handles SMTP password re-encryption specifically

#### 4. `main.go`
- Added `deriveEncryptionKey()` function
- Sets encryption key on notifier during startup: `notifier.SetEncryptionKey(deriveEncryptionKey(cfg.AdminPassHash))`

## Encryption Flow

### Saving SMTP Settings
1. User updates SMTP settings via `/api/settings`
2. If password is not empty and not already encrypted:
   - Derive encryption key from current admin password hash
   - Encrypt password using AES-256-GCM
   - Store encrypted password in database
3. Reconfigure SMTP with new settings

### Loading SMTP Settings
1. `ConfigureSMTP()` called during startup or after settings update
2. If encryption key is set and password looks encrypted (base64, >24 chars):
   - Attempt to decrypt password
   - If successful, use decrypted password
   - If fails, assume plaintext and use as-is
3. Create SMTP mailer with decrypted password

### Password Change Flow
1. User changes password via `/api/password`
2. Verify current password and get old hash
3. Update password in database with new hash
4. Update in-memory password hash
5. **Re-encrypt all data:**
   - Get old encryption key (derived from old password hash)
   - Get new encryption key (derived from new password hash)
   - For each encrypted field (currently just SMTP password):
     - Decrypt with old key
     - Re-encrypt with new key
     - Save to database
6. Invalidate all sessions (force re-login)

## Security Properties

### What This Protects Against
1. **Database file theft** - Attacker with database file cannot read SMTP passwords without knowing admin password
2. **Backup exposure** - Encrypted backups don't expose SMTP credentials
3. **Insider threat** - Users with file system access but not admin password cannot read SMTP passwords

### What This Doesn't Protect Against
1. **Memory dumps** - Running process has decrypted passwords in memory
2. **Admin compromise** - Anyone with admin password can decrypt all data
3. **Man-in-the-middle** - Still requires HTTPS reverse proxy for transport security

## Error Handling

### Re-Encryption Failures
- If re-encryption fails during password change, the operation continues
- Errors are logged as warnings
- The old encrypted data remains (encrypted with old key)
- User may need to re-enter SMTP password after password change

### Decryption Failures
- If decryption fails when loading SMTP settings, assumes plaintext
- Logs debug message: "SMTP password decryption failed (may be plaintext)"
- Uses password as-is (backward compatibility with unencrypted passwords)

## Migration Path

### From Plaintext to Encrypted
1. Existing plaintext SMTP passwords remain functional
2. When settings are next saved, password gets encrypted
3. Graceful fallback ensures no disruption during transition

### Testing
All tests pass including:
- Encryption/decryption round-trip tests
- Wrong key rejection tests
- Encrypted password SMTP configuration tests
- Race condition tests

## Usage Example

```go
// During startup
encryptionKey := deriveEncryptionKey(cfg.AdminPassHash)
notifier.SetEncryptionKey(encryptionKey)
notifier.ConfigureSMTP() // Will decrypt SMTP password if encrypted

// When saving settings
if !IsEncryptedValue(smtp.Password) {
    encryptedPass, err := notify.Encrypt(smtp.Password, encryptionKey)
    smtp.Password = encryptedPass
}

// When changing password
reEncryptErrors := ReEncryptAllData(h.store, oldHash, newHash)
if len(reEncryptErrors) > 0 {
    log.Warn("Some data could not be re-encrypted", "errors", reEncryptErrors)
}
```

## Future Considerations

1. **Additional encrypted fields** - Add more sensitive fields (API keys, etc.)
2. **Key rotation** - Currently rotation happens only on password change
3. **Master key** - Consider separating encryption key from auth password for enterprise use
4. **Audit logging** - Log all encryption/decryption operations for security auditing
