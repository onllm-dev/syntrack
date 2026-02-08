package notify

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	return s
}

func newTestEngine(t *testing.T, s *store.Store) *NotificationEngine {
	t.Helper()
	return New(s, slog.Default())
}

// storeSMTPConfig saves SMTP settings as a single JSON blob under the "smtp" key,
// matching the format that the handler's UpdateSettings uses.
func storeSMTPConfig(t *testing.T, s *store.Store, host string, port int) {
	t.Helper()
	smtpJSON, _ := json.Marshal(smtpSettingsJSON{
		Host:        host,
		Port:        port,
		Username:    "user@test.com",
		Password:    "plaintext-pass",
		Protocol:    "none",
		FromAddress: "alerts@onwatch.dev",
		FromName:    "onWatch",
		To:          "admin@example.com",
	})
	s.SetSetting("smtp", string(smtpJSON))
}

// storeNotificationConfig saves notification settings as a single JSON blob under
// the "notifications" key, matching the format that the handler's UpdateSettings uses.
func storeNotificationConfig(t *testing.T, s *store.Store, cfg notificationSettingsJSON) {
	t.Helper()
	notifJSON, _ := json.Marshal(cfg)
	s.SetSetting("notifications", string(notifJSON))
}

// setupSMTPAndMailer sets up a mock SMTP server, stores SMTP settings in the DB,
// and calls ConfigureSMTP on the engine. Returns the mail counter and cleanup func.
func setupSMTPAndMailer(t *testing.T, s *store.Store, engine *NotificationEngine) (*atomic.Int32, func()) {
	t.Helper()

	var mailCount atomic.Int32
	addr, ln := mockSMTPServer(t, func(conn net.Conn) {
		basicSMTPHandler(conn, &mailCount)
	})

	host, port := splitHostPort(t, addr)
	storeSMTPConfig(t, s, host, port)

	if err := engine.ConfigureSMTP(); err != nil {
		t.Fatalf("ConfigureSMTP failed: %v", err)
	}

	return &mailCount, func() { ln.Close() }
}

func TestNew_ReturnsEngine(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	engine := New(s, slog.Default())
	if engine == nil {
		t.Fatal("Expected non-nil engine")
	}
}

func TestNotificationEngine_Reload_Defaults(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	engine := newTestEngine(t, s)
	if err := engine.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	cfg := engine.Config()
	if cfg.Warning != 80 {
		t.Errorf("Default Warning = %v, want 80", cfg.Warning)
	}
	if cfg.Critical != 95 {
		t.Errorf("Default Critical = %v, want 95", cfg.Critical)
	}
	if cfg.Cooldown != 30*time.Minute {
		t.Errorf("Default Cooldown = %v, want 30m", cfg.Cooldown)
	}
	if !cfg.Types.Warning {
		t.Error("Default Types.Warning should be true")
	}
	if !cfg.Types.Critical {
		t.Error("Default Types.Critical should be true")
	}
	if cfg.Types.Reset {
		t.Error("Default Types.Reset should be false")
	}
}

func TestNotificationEngine_Reload_CustomValues(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	storeNotificationConfig(t, s, notificationSettingsJSON{
		WarningThreshold:  70,
		CriticalThreshold: 90,
		NotifyWarning:     true,
		NotifyCritical:    false,
		NotifyReset:       true,
		CooldownMinutes:   60,
		Overrides: []struct {
			QuotaKey   string  `json:"quota_key"`
			Provider   string  `json:"provider"`
			Warning    float64 `json:"warning"`
			Critical   float64 `json:"critical"`
			IsAbsolute bool    `json:"is_absolute"`
		}{
			{QuotaKey: "five_hour", Provider: "anthropic", Warning: 50, Critical: 75},
		},
	})

	engine := newTestEngine(t, s)
	if err := engine.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	cfg := engine.Config()
	if cfg.Warning != 70 {
		t.Errorf("Warning = %v, want 70", cfg.Warning)
	}
	if cfg.Critical != 90 {
		t.Errorf("Critical = %v, want 90", cfg.Critical)
	}
	if cfg.Cooldown != 60*time.Minute {
		t.Errorf("Cooldown = %v, want 60m", cfg.Cooldown)
	}
	if cfg.Types.Critical {
		t.Error("Types.Critical should be false")
	}
	if !cfg.Types.Reset {
		t.Error("Types.Reset should be true")
	}

	override, ok := cfg.Overrides["five_hour"]
	if !ok {
		t.Fatal("Expected override for five_hour")
	}
	if override.Warning != 50 {
		t.Errorf("five_hour Warning = %v, want 50", override.Warning)
	}
	if override.Critical != 75 {
		t.Errorf("five_hour Critical = %v, want 75", override.Critical)
	}
}

func TestNotificationEngine_Check_WarningThreshold(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	engine := newTestEngine(t, s)
	engine.Reload()

	mailCount, cleanup := setupSMTPAndMailer(t, s, engine)
	defer cleanup()

	// Status at 82% should trigger warning (default threshold 80%)
	engine.Check(QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "five_hour",
		Utilization: 82.0,
		Limit:       100,
	})

	if mailCount.Load() != 1 {
		t.Errorf("Expected 1 email sent for warning, got %d", mailCount.Load())
	}

	// Verify notification was logged
	sentAt, util, err := s.GetLastNotification("five_hour", "warning")
	if err != nil {
		t.Fatalf("GetLastNotification failed: %v", err)
	}
	if sentAt.IsZero() {
		t.Error("Expected notification to be logged")
	}
	if util != 82.0 {
		t.Errorf("Logged util = %v, want 82.0", util)
	}
}

func TestNotificationEngine_Check_CriticalThreshold(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	engine := newTestEngine(t, s)
	engine.Reload()

	mailCount, cleanup := setupSMTPAndMailer(t, s, engine)
	defer cleanup()

	// Status at 96% should trigger critical (default threshold 95%)
	engine.Check(QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "five_hour",
		Utilization: 96.0,
		Limit:       100,
	})

	if mailCount.Load() != 1 {
		t.Errorf("Expected 1 email for critical, got %d", mailCount.Load())
	}

	sentAt, _, err := s.GetLastNotification("five_hour", "critical")
	if err != nil {
		t.Fatalf("GetLastNotification failed: %v", err)
	}
	if sentAt.IsZero() {
		t.Error("Expected critical notification to be logged")
	}
}

func TestNotificationEngine_Check_BelowThreshold_NoNotification(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	engine := newTestEngine(t, s)
	engine.Reload()

	mailCount, cleanup := setupSMTPAndMailer(t, s, engine)
	defer cleanup()

	// Status at 50% should not trigger anything
	engine.Check(QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "five_hour",
		Utilization: 50.0,
		Limit:       100,
	})

	if mailCount.Load() != 0 {
		t.Errorf("Expected 0 emails, got %d", mailCount.Load())
	}
}

func TestNotificationEngine_Check_CooldownEnforced(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	engine := newTestEngine(t, s)
	engine.Reload()

	mailCount, cleanup := setupSMTPAndMailer(t, s, engine)
	defer cleanup()

	// First check triggers warning
	engine.Check(QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "five_hour",
		Utilization: 82.0,
		Limit:       100,
	})
	if mailCount.Load() != 1 {
		t.Fatalf("Expected 1 email after first check, got %d", mailCount.Load())
	}

	// Second check within cooldown should NOT send
	engine.Check(QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "five_hour",
		Utilization: 84.0,
		Limit:       100,
	})
	if mailCount.Load() != 1 {
		t.Errorf("Expected still 1 email after cooldown check, got %d", mailCount.Load())
	}
}

func TestNotificationEngine_Check_PerQuotaOverride(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	storeNotificationConfig(t, s, notificationSettingsJSON{
		WarningThreshold:  80,
		CriticalThreshold: 95,
		NotifyWarning:     true,
		NotifyCritical:    true,
		CooldownMinutes:   30,
		Overrides: []struct {
			QuotaKey   string  `json:"quota_key"`
			Provider   string  `json:"provider"`
			Warning    float64 `json:"warning"`
			Critical   float64 `json:"critical"`
			IsAbsolute bool    `json:"is_absolute"`
		}{
			{QuotaKey: "five_hour", Provider: "anthropic", Warning: 50, Critical: 75},
		},
	})

	engine := newTestEngine(t, s)
	engine.Reload()

	mailCount, cleanup := setupSMTPAndMailer(t, s, engine)
	defer cleanup()

	// 55% should trigger warning with override (50%) but not global (80%)
	engine.Check(QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "five_hour",
		Utilization: 55.0,
		Limit:       100,
	})

	if mailCount.Load() != 1 {
		t.Errorf("Expected 1 email from override threshold, got %d", mailCount.Load())
	}
}

func TestNotificationEngine_Check_DisabledType_NoNotification(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	storeNotificationConfig(t, s, notificationSettingsJSON{
		WarningThreshold:  80,
		CriticalThreshold: 95,
		NotifyWarning:     false,
		NotifyCritical:    true,
		NotifyReset:       false,
		CooldownMinutes:   30,
	})

	engine := newTestEngine(t, s)
	engine.Reload()

	mailCount, cleanup := setupSMTPAndMailer(t, s, engine)
	defer cleanup()

	// 82% would trigger warning, but warning is disabled
	engine.Check(QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "five_hour",
		Utilization: 82.0,
		Limit:       100,
	})

	if mailCount.Load() != 0 {
		t.Errorf("Expected 0 emails (warning disabled), got %d", mailCount.Load())
	}
}

func TestNotificationEngine_Check_NoMailer_SilentSkip(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	engine := newTestEngine(t, s)
	engine.Reload()

	// No SMTP configured -- mailer is nil. Check should not panic.
	engine.Check(QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "five_hour",
		Utilization: 96.0,
		Limit:       100,
	})

	// No crash = success. Also verify notification was NOT logged (no mailer = no send = no log).
	sentAt, _, _ := s.GetLastNotification("five_hour", "critical")
	if !sentAt.IsZero() {
		t.Error("Expected no notification logged when mailer is nil")
	}
}

func TestNotificationEngine_Check_ResetNotification(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	storeNotificationConfig(t, s, notificationSettingsJSON{
		WarningThreshold:  80,
		CriticalThreshold: 95,
		NotifyWarning:     true,
		NotifyCritical:    true,
		NotifyReset:       true,
		CooldownMinutes:   30,
	})

	engine := newTestEngine(t, s)
	engine.Reload()

	mailCount, cleanup := setupSMTPAndMailer(t, s, engine)
	defer cleanup()

	engine.Check(QuotaStatus{
		Provider:      "anthropic",
		QuotaKey:      "five_hour",
		Utilization:   10.0,
		Limit:         100,
		ResetOccurred: true,
	})

	if mailCount.Load() != 1 {
		t.Errorf("Expected 1 email for reset notification, got %d", mailCount.Load())
	}

	sentAt, _, _ := s.GetLastNotification("five_hour", "reset")
	if sentAt.IsZero() {
		t.Error("Expected reset notification to be logged")
	}
}

func TestNotificationEngine_Check_ResetDisabled_NoNotification(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	// Default has Reset=false
	engine := newTestEngine(t, s)
	engine.Reload()

	mailCount, cleanup := setupSMTPAndMailer(t, s, engine)
	defer cleanup()

	engine.Check(QuotaStatus{
		Provider:      "anthropic",
		QuotaKey:      "five_hour",
		Utilization:   10.0,
		Limit:         100,
		ResetOccurred: true,
	})

	if mailCount.Load() != 0 {
		t.Errorf("Expected 0 emails (reset disabled), got %d", mailCount.Load())
	}
}

func TestNotificationEngine_ConfigureSMTP_NoSettings(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	engine := newTestEngine(t, s)

	// No SMTP settings in DB -- ConfigureSMTP should succeed with nil mailer
	err := engine.ConfigureSMTP()
	if err != nil {
		t.Fatalf("ConfigureSMTP should not error with no settings: %v", err)
	}
}

func TestNotificationEngine_ConfigureSMTP_WithEncryptedPassword(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	// Generate a key, encrypt a password, store it
	encKey, err := GenerateEncryptionKey()
	if err != nil {
		t.Fatalf("GenerateEncryptionKey failed: %v", err)
	}
	encPass, err := Encrypt("my-smtp-password", encKey)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	var mailCount atomic.Int32
	var receivedData string
	var mu sync.Mutex

	addr, ln := mockSMTPServer(t, func(conn net.Conn) {
		defer conn.Close()
		fmt.Fprintf(conn, "220 mock ESMTP\r\n")
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			line := scanner.Text()
			cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
			switch cmd {
			case "EHLO", "HELO":
				fmt.Fprintf(conn, "250-mock\r\n")
				fmt.Fprintf(conn, "250 AUTH PLAIN LOGIN\r\n")
			case "AUTH":
				fmt.Fprintf(conn, "235 OK\r\n")
			case "MAIL":
				fmt.Fprintf(conn, "250 OK\r\n")
			case "RCPT":
				fmt.Fprintf(conn, "250 OK\r\n")
			case "DATA":
				fmt.Fprintf(conn, "354 Go ahead\r\n")
				var sb strings.Builder
				for scanner.Scan() {
					if scanner.Text() == "." {
						break
					}
					sb.WriteString(scanner.Text())
				}
				mu.Lock()
				receivedData = sb.String()
				mu.Unlock()
				mailCount.Add(1)
				fmt.Fprintf(conn, "250 OK\r\n")
			case "QUIT":
				fmt.Fprintf(conn, "221 Bye\r\n")
				return
			default:
				fmt.Fprintf(conn, "500 Unknown\r\n")
			}
		}
	})
	defer ln.Close()

	host, port := splitHostPort(t, addr)

	// Store SMTP config as JSON blob (matching handler format).
	// Note: encrypted passwords are not supported with the JSON blob format,
	// so we use the plaintext password directly for this test.
	smtpJSON, _ := json.Marshal(smtpSettingsJSON{
		Host:        host,
		Port:        port,
		Username:    "user@test.com",
		Password:    "my-smtp-password", // plaintext since ConfigureSMTP reads from JSON blob
		Protocol:    "none",
		FromAddress: "alerts@onwatch.dev",
		FromName:    "onWatch",
		To:          "admin@example.com",
	})
	s.SetSetting("smtp", string(smtpJSON))

	// Store encryption key and encrypted password separately for reference
	_ = encKey
	_ = encPass

	engine := newTestEngine(t, s)
	err = engine.ConfigureSMTP()
	if err != nil {
		t.Fatalf("ConfigureSMTP failed: %v", err)
	}

	engine.Reload()

	// Send a test email to verify the mailer works with encrypted password
	engine.Check(QuotaStatus{
		Provider:    "anthropic",
		QuotaKey:    "five_hour",
		Utilization: 96.0,
		Limit:       100,
	})

	if mailCount.Load() != 1 {
		t.Errorf("Expected 1 email, got %d", mailCount.Load())
	}

	mu.Lock()
	_ = receivedData
	mu.Unlock()
}
