package notify

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTPConfig holds SMTP connection settings.
type SMTPConfig struct {
	Host     string   // SMTP server hostname
	Port     int      // SMTP server port (25, 465, 587)
	Username string   // SMTP auth username
	Password string   // SMTP auth password (plaintext or decrypted)
	Protocol string   // "tls" (port 465), "starttls" (port 587), "none" (port 25)
	FromAddr string   // Sender email address
	FromName string   // Sender display name
	ToAddrs  []string // Recipient email addresses
}

// SMTPMailer sends email notifications via SMTP.
type SMTPMailer struct {
	config SMTPConfig
	logger *slog.Logger
}

// NewSMTPMailer creates a new SMTP mailer with the given config.
func NewSMTPMailer(cfg SMTPConfig, logger *slog.Logger) *SMTPMailer {
	if cfg.Protocol == "none" && logger != nil {
		logger.Warn("SMTP using unencrypted connection - credentials will be sent in plaintext. Consider using TLS or STARTTLS.")
	}
	return &SMTPMailer{config: cfg, logger: logger}
}

// Send sends an email with the given subject and plaintext body.
func (m *SMTPMailer) Send(subject, body string) error {
	msg := m.buildMessage(subject, body)

	client, err := m.connect()
	if err != nil {
		return fmt.Errorf("notify.Send: connect: %w", err)
	}
	defer client.Close()

	if err := m.authenticate(client); err != nil {
		return fmt.Errorf("notify.Send: auth: %w", err)
	}

	if err := client.Mail(m.config.FromAddr); err != nil {
		return fmt.Errorf("notify.Send: MAIL FROM: %w", err)
	}

	for _, addr := range m.config.ToAddrs {
		if err := client.Rcpt(addr); err != nil {
			return fmt.Errorf("notify.Send: RCPT TO %s: %w", addr, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("notify.Send: DATA: %w", err)
	}

	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("notify.Send: write: %w", err)
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("notify.Send: close data: %w", err)
	}

	client.Quit()
	m.logger.Info("email sent", "subject", subject, "recipients", len(m.config.ToAddrs))
	return nil
}

// TestConnection verifies SMTP connectivity and authentication.
func (m *SMTPMailer) TestConnection() error {
	client, err := m.connect()
	if err != nil {
		return fmt.Errorf("notify.TestConnection: connect: %w", err)
	}
	defer client.Close()

	if m.config.Username != "" {
		if err := m.authenticate(client); err != nil {
			return fmt.Errorf("notify.TestConnection: auth: %w", err)
		}
	}

	client.Quit()
	return nil
}

// connect establishes an SMTP connection using the configured protocol.
func (m *SMTPMailer) connect() (*smtp.Client, error) {
	addr := net.JoinHostPort(m.config.Host, fmt.Sprintf("%d", m.config.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	switch m.config.Protocol {
	case "tls":
		// Implicit TLS (port 465): TLS from the start
		tlsConn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
			ServerName: m.config.Host,
		})
		if err != nil {
			return nil, fmt.Errorf("TLS dial: %w", err)
		}
		client, err := smtp.NewClient(tlsConn, m.config.Host)
		if err != nil {
			tlsConn.Close()
			return nil, fmt.Errorf("SMTP client: %w", err)
		}
		return client, nil

	case "starttls":
		// STARTTLS (port 587): plain connect, then upgrade
		conn, err := dialer.Dial("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("dial: %w", err)
		}
		client, err := smtp.NewClient(conn, m.config.Host)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("SMTP client: %w", err)
		}
		if err := client.StartTLS(&tls.Config{ServerName: m.config.Host}); err != nil {
			client.Close()
			return nil, fmt.Errorf("STARTTLS: %w", err)
		}
		return client, nil

	default:
		// Plain SMTP (port 25): no encryption
		conn, err := dialer.Dial("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("dial: %w", err)
		}
		client, err := smtp.NewClient(conn, m.config.Host)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("SMTP client: %w", err)
		}
		return client, nil
	}
}

// authenticate performs SMTP AUTH PLAIN.
func (m *SMTPMailer) authenticate(client *smtp.Client) error {
	auth := smtp.PlainAuth("", m.config.Username, m.config.Password, m.config.Host)
	return client.Auth(auth)
}

// buildMessage constructs an RFC 2822 email message.
func (m *SMTPMailer) buildMessage(subject, body string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("From: %s <%s>\r\n", m.config.FromName, m.config.FromAddr))
	sb.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(m.config.ToAddrs, ", ")))
	sb.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return sb.String()
}
