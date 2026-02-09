// Package config handles loading and validation of onWatch configuration.
// It loads from .env files, environment variables, and CLI flags.
package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all application configuration.
type Config struct {
	// Synthetic provider configuration
	SyntheticAPIKey string // SYNTHETIC_API_KEY

	// Z.ai provider configuration
	ZaiAPIKey  string // ZAI_API_KEY
	ZaiBaseURL string // ZAI_BASE_URL

	// Anthropic provider configuration
	AnthropicToken     string // ANTHROPIC_TOKEN or auto-detected
	AnthropicAutoToken bool   // true if token was auto-detected

	// Shared configuration
	PollInterval       time.Duration // ONWATCH_POLL_INTERVAL (seconds → Duration)
	Port               int           // ONWATCH_PORT
	Host               string        // ONWATCH_HOST (bind address, default: 0.0.0.0)
	AdminUser          string        // ONWATCH_ADMIN_USER
	AdminPass          string        // ONWATCH_ADMIN_PASS
	AdminPassHash      string        // SHA-256 hash of password (set after DB check)
	DBPath             string        // ONWATCH_DB_PATH
	DBPathExplicit     bool          // true if user explicitly set --db or ONWATCH_DB_PATH
	LogLevel           string        // ONWATCH_LOG_LEVEL
	SessionIdleTimeout time.Duration // ONWATCH_SESSION_IDLE_TIMEOUT (seconds → Duration)
	DebugMode          bool          // --debug flag (foreground mode)
	TestMode           bool          // --test flag (test mode isolation)
}

// envWithFallback reads the primary env var, falling back to the legacy name.
// This provides backward compatibility for SYNTRACK_* → ONWATCH_* rename.
func envWithFallback(primary, fallback string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	return os.Getenv(fallback)
}

// flagValues holds parsed CLI flags.
type flagValues struct {
	interval int
	port     int
	db       string
	debug    bool
	test     bool
}

// Load reads configuration from .env file, environment variables, and CLI flags.
// Flags take precedence over environment variables.
func Load() (*Config, error) {
	return loadWithArgs(os.Args[1:])
}

// loadWithArgs loads config with specific arguments (for testing).
func loadWithArgs(args []string) (*Config, error) {
	flags := &flagValues{}

	// Parse CLI flags manually to avoid flag.ExitOnError in tests
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--debug":
			flags.debug = true
		case arg == "--test":
			flags.test = true
		case strings.HasPrefix(arg, "--interval="):
			val := strings.TrimPrefix(arg, "--interval=")
			if v, err := strconv.Atoi(val); err == nil {
				flags.interval = v
			}
		case arg == "--interval":
			if i+1 < len(args) {
				if v, err := strconv.Atoi(args[i+1]); err == nil {
					flags.interval = v
					i++
				}
			}
		case strings.HasPrefix(arg, "--port="):
			val := strings.TrimPrefix(arg, "--port=")
			if v, err := strconv.Atoi(val); err == nil {
				flags.port = v
			}
		case arg == "--port":
			if i+1 < len(args) {
				if v, err := strconv.Atoi(args[i+1]); err == nil {
					flags.port = v
					i++
				}
			}
		case strings.HasPrefix(arg, "--db="):
			flags.db = strings.TrimPrefix(arg, "--db=")
		case arg == "--db":
			if i+1 < len(args) {
				flags.db = args[i+1]
				i++
			}
		}
	}

	return loadFromEnvAndFlags(flags)
}

// loadFromEnvAndFlags combines environment variables with CLI flags.
func loadFromEnvAndFlags(flags *flagValues) (*Config, error) {
	// Try to load .env file (ignore errors - file is optional)
	_ = godotenv.Load(".env")

	cfg := &Config{}

	// Synthetic provider
	cfg.SyntheticAPIKey = os.Getenv("SYNTHETIC_API_KEY")

	// Z.ai provider
	cfg.ZaiAPIKey = os.Getenv("ZAI_API_KEY")
	cfg.ZaiBaseURL = os.Getenv("ZAI_BASE_URL")

	// Anthropic provider
	cfg.AnthropicToken = os.Getenv("ANTHROPIC_TOKEN")

	// Poll Interval (seconds) — ONWATCH_* first, SYNTRACK_* fallback
	if flags.interval > 0 {
		cfg.PollInterval = time.Duration(flags.interval) * time.Second
	} else if env := envWithFallback("ONWATCH_POLL_INTERVAL", "SYNTRACK_POLL_INTERVAL"); env != "" {
		if v, err := strconv.Atoi(env); err == nil {
			cfg.PollInterval = time.Duration(v) * time.Second
		}
	}

	// Port
	if flags.port > 0 {
		cfg.Port = flags.port
	} else if env := envWithFallback("ONWATCH_PORT", "SYNTRACK_PORT"); env != "" {
		if v, err := strconv.Atoi(env); err == nil {
			cfg.Port = v
		}
	}

	// Admin credentials
	cfg.AdminUser = envWithFallback("ONWATCH_ADMIN_USER", "SYNTRACK_ADMIN_USER")
	cfg.AdminPass = envWithFallback("ONWATCH_ADMIN_PASS", "SYNTRACK_ADMIN_PASS")

	// DB Path
	if flags.db != "" {
		cfg.DBPath = flags.db
		cfg.DBPathExplicit = true
	} else if envDB := envWithFallback("ONWATCH_DB_PATH", "SYNTRACK_DB_PATH"); envDB != "" {
		cfg.DBPath = envDB
		cfg.DBPathExplicit = true
	}

	// Log Level
	cfg.LogLevel = envWithFallback("ONWATCH_LOG_LEVEL", "SYNTRACK_LOG_LEVEL")

	// Session Idle Timeout (seconds)
	if env := envWithFallback("ONWATCH_SESSION_IDLE_TIMEOUT", "SYNTRACK_SESSION_IDLE_TIMEOUT"); env != "" {
		if v, err := strconv.Atoi(env); err == nil {
			cfg.SessionIdleTimeout = time.Duration(v) * time.Second
		}
	}

	// Debug mode (CLI flag only)
	cfg.DebugMode = flags.debug

	// Test mode (CLI flag only)
	cfg.TestMode = flags.test

	// Apply defaults
	cfg.applyDefaults()

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// applyDefaults sets default values for empty config fields.
func (c *Config) applyDefaults() {
	if c.PollInterval == 0 {
		c.PollInterval = 60 * time.Second
	}
	if c.Port == 0 {
		c.Port = 9211
	}
	if c.AdminUser == "" {
		c.AdminUser = "admin"
	}
	if c.AdminPass == "" {
		c.AdminPass = "changeme"
	}
	if c.DBPath == "" {
		// Check if running in Docker and use /data/onwatch.db as default
		if c.isDockerEnvironment() {
			c.DBPath = "/data/onwatch.db"
		} else {
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				c.DBPath = "./onwatch.db"
			} else {
				c.DBPath = filepath.Join(home, ".onwatch", "data", "onwatch.db")
			}
		}
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.ZaiBaseURL == "" {
		c.ZaiBaseURL = "https://api.z.ai/api"
	}
	if c.SessionIdleTimeout == 0 {
		c.SessionIdleTimeout = 600 * time.Second
	}
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	// At least one provider must be configured
	if c.SyntheticAPIKey == "" && c.ZaiAPIKey == "" && c.AnthropicToken == "" {
		return fmt.Errorf("at least one provider must be configured: set SYNTHETIC_API_KEY, ZAI_API_KEY, or ANTHROPIC_TOKEN")
	}

	// Validate Synthetic API key if provided
	if c.SyntheticAPIKey != "" && !strings.HasPrefix(c.SyntheticAPIKey, "syn_") {
		return fmt.Errorf("SYNTHETIC_API_KEY must start with 'syn_'")
	}

	// Validate Z.ai API key if provided
	if c.ZaiAPIKey != "" && c.ZaiAPIKey == "" {
		return fmt.Errorf("ZAI_API_KEY cannot be empty if provided")
	}

	// Poll interval bounds
	minInterval := 10 * time.Second
	maxInterval := 3600 * time.Second
	if c.PollInterval < minInterval {
		return fmt.Errorf("poll interval must be at least %v", minInterval)
	}
	if c.PollInterval > maxInterval {
		return fmt.Errorf("poll interval must be at most %v", maxInterval)
	}

	// Port range
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1024 and 65535")
	}

	return nil
}

// AvailableProviders returns which providers are configured.
func (c *Config) AvailableProviders() []string {
	var providers []string
	if c.AnthropicToken != "" {
		providers = append(providers, "anthropic")
	}
	if c.SyntheticAPIKey != "" {
		providers = append(providers, "synthetic")
	}
	if c.ZaiAPIKey != "" {
		providers = append(providers, "zai")
	}
	return providers
}

// HasProvider returns true if the given provider is configured.
func (c *Config) HasProvider(name string) bool {
	switch name {
	case "synthetic":
		return c.SyntheticAPIKey != ""
	case "zai":
		return c.ZaiAPIKey != ""
	case "anthropic":
		return c.AnthropicToken != ""
	}
	return false
}

// HasMultipleProviders returns true if more than one provider is configured.
func (c *Config) HasMultipleProviders() bool {
	count := 0
	if c.SyntheticAPIKey != "" {
		count++
	}
	if c.ZaiAPIKey != "" {
		count++
	}
	if c.AnthropicToken != "" {
		count++
	}
	return count > 1
}

// HasBothProviders is an alias for HasMultipleProviders (backward compat).
func (c *Config) HasBothProviders() bool {
	return c.HasMultipleProviders()
}

// String returns a redacted string representation of the config.
func (c *Config) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Config{\n")

	// Providers section
	fmt.Fprintf(&sb, "  Providers: %v,\n", c.AvailableProviders())

	// Redact Synthetic API key
	syntheticKeyDisplay := redactAPIKey(c.SyntheticAPIKey, "syn_")
	fmt.Fprintf(&sb, "  SyntheticAPIKey: %s,\n", syntheticKeyDisplay)

	// Redact Z.ai API key
	zaiKeyDisplay := redactAPIKey(c.ZaiAPIKey, "")
	fmt.Fprintf(&sb, "  ZaiAPIKey: %s,\n", zaiKeyDisplay)
	fmt.Fprintf(&sb, "  ZaiBaseURL: %s,\n", c.ZaiBaseURL)

	// Redact Anthropic token
	anthropicDisplay := redactAPIKey(c.AnthropicToken, "")
	fmt.Fprintf(&sb, "  AnthropicToken: %s,\n", anthropicDisplay)
	if c.AnthropicAutoToken {
		fmt.Fprintf(&sb, "  AnthropicAutoToken: true,\n")
	}

	fmt.Fprintf(&sb, "  PollInterval: %v,\n", c.PollInterval)
	fmt.Fprintf(&sb, "  SessionIdleTimeout: %v,\n", c.SessionIdleTimeout)
	fmt.Fprintf(&sb, "  Port: %d,\n", c.Port)
	fmt.Fprintf(&sb, "  AdminUser: %s,\n", c.AdminUser)
	fmt.Fprintf(&sb, "  AdminPass: ****,\n")
	fmt.Fprintf(&sb, "  DBPath: %s,\n", c.DBPath)
	fmt.Fprintf(&sb, "  LogLevel: %s,\n", c.LogLevel)
	fmt.Fprintf(&sb, "  DebugMode: %v,\n", c.DebugMode)
	fmt.Fprintf(&sb, "}")

	return sb.String()
}

// redactAPIKey masks the API key for display.
func redactAPIKey(key string, expectedPrefix string) string {
	if key == "" {
		return "(not set)"
	}

	if expectedPrefix != "" && !strings.HasPrefix(key, expectedPrefix) {
		return "***...***"
	}

	prefixLen := len(expectedPrefix)
	if len(key) <= prefixLen+7 {
		return expectedPrefix + "***...***"
	}

	// Show first 4 chars after prefix and last 3 chars
	return key[:prefixLen+4] + "***...***" + key[len(key)-3:]
}

// LogWriter returns the appropriate log destination based on debug mode.
// In debug mode: returns os.Stdout
// In Docker: returns os.Stdout (containers should log to stdout)
// In background mode: returns a file handle to .onwatch.log
func (c *Config) LogWriter() (io.Writer, error) {
	if c.DebugMode {
		return os.Stdout, nil
	}

	// Docker mode: always log to stdout
	if c.isDockerEnvironment() {
		return os.Stdout, nil
	}

	// Background mode: log to file in same directory as DB
	logName := ".onwatch.log"
	if c.TestMode {
		logName = ".onwatch-test.log"
	}
	logPath := filepath.Join(filepath.Dir(c.DBPath), logName)

	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	return file, nil
}

// isDockerEnvironment detects if running inside a Docker container.
// Checks for the presence of /.dockerenv (created by Docker) or the
// DOCKER_CONTAINER environment variable (set by some container runtimes).
func (c *Config) isDockerEnvironment() bool {
	// Check for /.dockerenv file (standard Docker indicator)
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	// Check for DOCKER_CONTAINER environment variable
	if os.Getenv("DOCKER_CONTAINER") != "" {
		return true
	}
	return false
}
