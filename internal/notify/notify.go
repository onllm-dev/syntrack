package notify

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/onllm-dev/onwatch/internal/store"
)

// NotificationEngine evaluates quota statuses and sends alerts via email.
type NotificationEngine struct {
	store         *store.Store
	logger        *slog.Logger
	mailer        *SMTPMailer
	mu            sync.RWMutex
	cfg           NotificationConfig
	encryptionKey string // hex-encoded key for decrypting SMTP passwords
}

// NotificationConfig holds threshold and delivery settings.
type NotificationConfig struct {
	Warning   float64                      // global warning threshold (default 80)
	Critical  float64                      // global critical threshold (default 95)
	Overrides map[string]ThresholdOverride // per quota key overrides
	Cooldown  time.Duration                // minimum time between notifications
	Types     NotificationTypes            // which notification types are enabled
}

// ThresholdOverride allows per-quota threshold customization.
type ThresholdOverride struct {
	Warning    float64 `json:"warning"`
	Critical   float64 `json:"critical"`
	IsAbsolute bool    `json:"is_absolute"`
}

// NotificationTypes controls which notification types are enabled.
type NotificationTypes struct {
	Warning  bool `json:"warning"`
	Critical bool `json:"critical"`
	Reset    bool `json:"reset"`
}

// QuotaStatus represents the current state of a quota for notification evaluation.
type QuotaStatus struct {
	Provider      string
	QuotaKey      string
	Utilization   float64
	Limit         float64
	ResetOccurred bool
}

// New creates a new NotificationEngine with default configuration.
func New(s *store.Store, logger *slog.Logger) *NotificationEngine {
	return &NotificationEngine{
		store:  s,
		logger: logger,
		cfg: NotificationConfig{
			Warning:   80,
			Critical:  95,
			Overrides: make(map[string]ThresholdOverride),
			Cooldown:  30 * time.Minute,
			Types:     NotificationTypes{Warning: true, Critical: true, Reset: false},
		},
	}
}

// SetEncryptionKey sets the encryption key for decrypting sensitive data like SMTP passwords.
// The key should be a hex-encoded 32-byte string suitable for AES-256-GCM.
func (e *NotificationEngine) SetEncryptionKey(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.encryptionKey = key
}

// Config returns a copy of the current notification config.
func (e *NotificationEngine) Config() NotificationConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	cfg := e.cfg
	// Copy the map to prevent mutation
	overrides := make(map[string]ThresholdOverride, len(e.cfg.Overrides))
	for k, v := range e.cfg.Overrides {
		overrides[k] = v
	}
	cfg.Overrides = overrides
	return cfg
}

// notificationSettingsJSON matches the JSON shape saved by the handler's UpdateSettings.
type notificationSettingsJSON struct {
	WarningThreshold  float64 `json:"warning_threshold"`
	CriticalThreshold float64 `json:"critical_threshold"`
	NotifyWarning     bool    `json:"notify_warning"`
	NotifyCritical    bool    `json:"notify_critical"`
	NotifyReset       bool    `json:"notify_reset"`
	CooldownMinutes   int     `json:"cooldown_minutes"`
	Overrides         []struct {
		QuotaKey   string  `json:"quota_key"`
		Provider   string  `json:"provider"`
		Warning    float64 `json:"warning"`
		Critical   float64 `json:"critical"`
		IsAbsolute bool    `json:"is_absolute"`
	} `json:"overrides"`
}

// Reload reads notification configuration from the settings table.
// The handler stores notifications as a single JSON blob under key "notifications".
func (e *NotificationEngine) Reload() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	v, err := e.store.GetSetting("notifications")
	if err != nil || v == "" {
		return nil // no notification settings saved yet, keep defaults
	}

	var notif notificationSettingsJSON
	if err := json.Unmarshal([]byte(v), &notif); err != nil {
		return fmt.Errorf("notify.Reload: invalid notifications JSON: %w", err)
	}

	if notif.WarningThreshold > 0 {
		e.cfg.Warning = notif.WarningThreshold
	}
	if notif.CriticalThreshold > 0 {
		e.cfg.Critical = notif.CriticalThreshold
	}
	if notif.CooldownMinutes > 0 {
		e.cfg.Cooldown = time.Duration(notif.CooldownMinutes) * time.Minute
	}
	e.cfg.Types = NotificationTypes{
		Warning:  notif.NotifyWarning,
		Critical: notif.NotifyCritical,
		Reset:    notif.NotifyReset,
	}

	overrides := make(map[string]ThresholdOverride, len(notif.Overrides))
	for _, o := range notif.Overrides {
		overrides[o.QuotaKey] = ThresholdOverride{Warning: o.Warning, Critical: o.Critical, IsAbsolute: o.IsAbsolute}
	}
	e.cfg.Overrides = overrides

	return nil
}

// smtpSettingsJSON matches the JSON shape saved by the handler's UpdateSettings.
type smtpSettingsJSON struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Protocol    string `json:"protocol"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	FromAddress string `json:"from_address"`
	FromName    string `json:"from_name"`
	To          string `json:"to"`
}

// ConfigureSMTP initializes or updates the SMTP mailer from DB settings.
// The handler stores SMTP config as a single JSON blob under key "smtp".
func (e *NotificationEngine) ConfigureSMTP() error {
	smtpJSON, err := e.store.GetSetting("smtp")
	if err != nil {
		return fmt.Errorf("notify.ConfigureSMTP: %w", err)
	}
	if smtpJSON == "" {
		e.mu.Lock()
		e.mailer = nil
		e.mu.Unlock()
		return nil
	}

	var s smtpSettingsJSON
	if err := json.Unmarshal([]byte(smtpJSON), &s); err != nil {
		return fmt.Errorf("notify.ConfigureSMTP: invalid smtp JSON: %w", err)
	}

	if s.Host == "" {
		e.mu.Lock()
		e.mailer = nil
		e.mu.Unlock()
		return nil
	}

	port := s.Port
	if port == 0 {
		port = 587
	}

	// Parse comma-separated recipients
	var toAddrs []string
	for _, addr := range strings.Split(s.To, ",") {
		addr = strings.TrimSpace(addr)
		if addr != "" {
			toAddrs = append(toAddrs, addr)
		}
	}

	// Decrypt SMTP password if encrypted
	password := s.Password
	e.mu.RLock()
	key := e.encryptionKey
	e.mu.RUnlock()

	if key != "" && password != "" && len(password) > 24 {
		// Try to decrypt - assume encrypted if base64-like and long enough
		if decrypted, err := Decrypt(password, key); err == nil {
			password = decrypted
		} else {
			// Failed to decrypt - might be plaintext or wrong key
			e.logger.Debug("SMTP password decryption failed (may be plaintext)", "error", err)
		}
	}

	cfg := SMTPConfig{
		Host:     s.Host,
		Port:     port,
		Username: s.Username,
		Password: password,
		Protocol: s.Protocol,
		FromAddr: s.FromAddress,
		FromName: s.FromName,
		ToAddrs:  toAddrs,
	}

	e.mu.Lock()
	e.mailer = NewSMTPMailer(cfg, e.logger)
	e.mu.Unlock()

	return nil
}

// Check evaluates a quota status against thresholds and sends notifications if needed.
// Runs synchronously -- no goroutines spawned.
func (e *NotificationEngine) Check(status QuotaStatus) {
	e.mu.RLock()
	cfg := e.cfg
	mailer := e.mailer
	e.mu.RUnlock()

	if mailer == nil {
		return
	}

	// Handle reset notification
	if status.ResetOccurred {
		if cfg.Types.Reset {
			e.sendNotification(mailer, status, "reset")
		}
		return
	}

	// Resolve thresholds
	warningThreshold := cfg.Warning
	criticalThreshold := cfg.Critical
	if override, ok := cfg.Overrides[status.QuotaKey]; ok {
		if override.IsAbsolute && status.Limit > 0 {
			// Convert absolute values to percentage for comparison
			if override.Warning > 0 {
				warningThreshold = (override.Warning / status.Limit) * 100
			}
			if override.Critical > 0 {
				criticalThreshold = (override.Critical / status.Limit) * 100
			}
		} else {
			if override.Warning > 0 {
				warningThreshold = override.Warning
			}
			if override.Critical > 0 {
				criticalThreshold = override.Critical
			}
		}
	}

	// Check critical first (higher priority)
	if status.Utilization >= criticalThreshold && cfg.Types.Critical {
		e.sendNotification(mailer, status, "critical")
		return
	}

	// Check warning
	if status.Utilization >= warningThreshold && cfg.Types.Warning {
		e.sendNotification(mailer, status, "warning")
		return
	}
}

// SendTestEmail sends a test email to verify SMTP configuration.
func (e *NotificationEngine) SendTestEmail() error {
	e.mu.RLock()
	mailer := e.mailer
	e.mu.RUnlock()

	if mailer == nil {
		return fmt.Errorf("SMTP not configured")
	}

	subject := "[onWatch] Test Email"
	body := "This is a test email from onWatch.\n\nIf you received this, your SMTP settings are configured correctly.\n\n-- Sent by onWatch"
	return mailer.Send(subject, body)
}

// sendNotification sends an email notification if cooldown has elapsed.
func (e *NotificationEngine) sendNotification(mailer *SMTPMailer, status QuotaStatus, notifType string) {
	// Check cooldown
	e.mu.RLock()
	cooldown := e.cfg.Cooldown
	e.mu.RUnlock()

	sentAt, _, err := e.store.GetLastNotification(status.QuotaKey, notifType)
	if err != nil {
		e.logger.Error("failed to check notification log", "error", err)
		return
	}
	if !sentAt.IsZero() && time.Since(sentAt) < cooldown {
		e.logger.Debug("notification cooldown active",
			"quota", status.QuotaKey, "type", notifType,
			"last_sent", sentAt, "cooldown", cooldown)
		return
	}

	subject := e.buildSubject(status, notifType)
	body := e.buildBody(status, notifType)

	if err := mailer.Send(subject, body); err != nil {
		e.logger.Error("failed to send notification", "error", err,
			"quota", status.QuotaKey, "type", notifType)
		return
	}

	// Log the notification
	if err := e.store.UpsertNotificationLog(status.QuotaKey, notifType, status.Utilization); err != nil {
		e.logger.Error("failed to log notification", "error", err)
	}
}

// titleCase capitalizes the first letter of a string.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// buildSubject creates the email subject line.
func (e *NotificationEngine) buildSubject(status QuotaStatus, notifType string) string {
	switch notifType {
	case "critical":
		return fmt.Sprintf("[CRITICAL] %s quota %s at %.1f%%",
			titleCase(status.Provider), status.QuotaKey, status.Utilization)
	case "warning":
		return fmt.Sprintf("[WARNING] %s quota %s at %.1f%%",
			titleCase(status.Provider), status.QuotaKey, status.Utilization)
	case "reset":
		return fmt.Sprintf("[RESET] %s quota %s has been reset",
			titleCase(status.Provider), status.QuotaKey)
	default:
		return fmt.Sprintf("[%s] %s quota %s", notifType, status.Provider, status.QuotaKey)
	}
}

// buildBody creates the email body text.
func (e *NotificationEngine) buildBody(status QuotaStatus, notifType string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Provider: %s\n", status.Provider))
	sb.WriteString(fmt.Sprintf("Quota: %s\n", status.QuotaKey))
	sb.WriteString(fmt.Sprintf("Utilization: %.1f%%\n", status.Utilization))
	if status.Limit > 0 {
		sb.WriteString(fmt.Sprintf("Limit: %.0f\n", status.Limit))
	}
	sb.WriteString(fmt.Sprintf("Alert Type: %s\n", notifType))
	sb.WriteString(fmt.Sprintf("Time: %s\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString("\n-- Sent by onWatch")
	return sb.String()
}
