package store

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	_ "modernc.org/sqlite"
)

// Store provides SQLite storage for onWatch
type Store struct {
	db *sql.DB
}

// validateOrderByColumn validates that the column name is in the allowlist.
// This prevents SQL injection via ORDER BY clauses. The allowlist must contain
// only known-safe column names. Usage with fmt.Sprintf is safe only when
// combined with this validation.
func validateOrderByColumn(column string, allowlist []string) (string, bool) {
	for _, allowed := range allowlist {
		if column == allowed {
			return column, true
		}
	}
	return "", false
}

// Session represents an agent session
type Session struct {
	ID                  string
	StartedAt           time.Time
	EndedAt             *time.Time
	PollInterval        int
	MaxSubRequests      float64
	MaxSearchRequests   float64
	MaxToolRequests     float64
	StartSubRequests    float64
	StartSearchRequests float64
	StartToolRequests   float64
	SnapshotCount       int
}

// ResetCycle represents a quota reset cycle
type ResetCycle struct {
	ID           int64
	QuotaType    string
	CycleStart   time.Time
	CycleEnd     *time.Time
	RenewsAt     time.Time
	PeakRequests float64
	TotalDelta   float64
}

// CycleOverviewRow represents a single cycle with cross-quota data at peak time.
type CycleOverviewRow struct {
	CycleID     int64
	QuotaType   string
	CycleStart  time.Time
	CycleEnd    *time.Time
	PeakValue   float64
	TotalDelta  float64
	PeakTime    time.Time
	CrossQuotas []CrossQuotaEntry
}

// CrossQuotaEntry holds a single quota's value at a given point in time.
type CrossQuotaEntry struct {
	Name         string
	Value        float64
	Limit        float64 // 0 for Anthropic (utilization is already %)
	Percent      float64
	StartPercent float64 // Value at cycle start (for delta calculation)
	Delta        float64 // Percent - StartPercent
}

// New creates a new Store with the given database path
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Single connection: SQLite is single-writer anyway, and each connection
	// allocates its own page cache (~2 MB with default settings). Limiting to 1
	// connection saves 2-4 MB RSS. busy_timeout handles any contention.
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)

	// Configure SQLite for RAM efficiency
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA cache_size=-500;",
		"PRAGMA foreign_keys=ON;",
		"PRAGMA busy_timeout=5000;",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return nil, fmt.Errorf("failed to set pragma: %w", err)
		}
	}

	s := &Store{db: db}
	if err := s.createTables(); err != nil {
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	return s, nil
}

// createTables creates the database schema
func (s *Store) createTables() error {
	schema := `
		CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS quota_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL DEFAULT 'synthetic',
			captured_at TEXT NOT NULL,
			sub_limit REAL NOT NULL,
			sub_requests REAL NOT NULL,
			sub_renews_at TEXT NOT NULL,
			search_limit REAL NOT NULL,
			search_requests REAL NOT NULL,
			search_renews_at TEXT NOT NULL,
			tool_limit REAL NOT NULL,
			tool_requests REAL NOT NULL,
			tool_renews_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS reset_cycles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL DEFAULT 'synthetic',
			quota_type TEXT NOT NULL,
			cycle_start TEXT NOT NULL,
			cycle_end TEXT,
			renews_at TEXT NOT NULL,
			peak_requests REAL NOT NULL DEFAULT 0,
			total_delta REAL NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL DEFAULT 'synthetic',
			started_at TEXT NOT NULL,
			ended_at TEXT,
			poll_interval INTEGER NOT NULL,
			max_sub_requests REAL NOT NULL DEFAULT 0,
			max_search_requests REAL NOT NULL DEFAULT 0,
			max_tool_requests REAL NOT NULL DEFAULT 0,
			start_sub_requests REAL NOT NULL DEFAULT 0,
			start_search_requests REAL NOT NULL DEFAULT 0,
			start_tool_requests REAL NOT NULL DEFAULT 0,
			snapshot_count INTEGER NOT NULL DEFAULT 0
		);

		CREATE INDEX IF NOT EXISTS idx_snapshots_captured ON quota_snapshots(captured_at);
		CREATE INDEX IF NOT EXISTS idx_snapshots_sub_renews ON quota_snapshots(sub_renews_at);
		CREATE INDEX IF NOT EXISTS idx_snapshots_tool_renews ON quota_snapshots(tool_renews_at);
		CREATE INDEX IF NOT EXISTS idx_cycles_type_start ON reset_cycles(quota_type, cycle_start);
		CREATE INDEX IF NOT EXISTS idx_cycles_type_active ON reset_cycles(quota_type, cycle_end) WHERE cycle_end IS NULL;
		CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at);

		CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS auth_tokens (
			token      TEXT PRIMARY KEY,
			expires_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS users (
			username TEXT PRIMARY KEY,
			password_hash TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);

		-- Z.ai-specific tables
		CREATE TABLE IF NOT EXISTS zai_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL DEFAULT 'zai',
			captured_at TEXT NOT NULL,
			time_limit INTEGER NOT NULL,
			time_unit INTEGER NOT NULL,
			time_number INTEGER NOT NULL,
			time_usage REAL NOT NULL,
			time_current_value REAL NOT NULL,
			time_remaining REAL NOT NULL,
			time_percentage INTEGER NOT NULL,
			time_usage_details TEXT NOT NULL DEFAULT '',
			tokens_limit INTEGER NOT NULL,
			tokens_unit INTEGER NOT NULL,
			tokens_number INTEGER NOT NULL,
			tokens_usage REAL NOT NULL,
			tokens_current_value REAL NOT NULL,
			tokens_remaining REAL NOT NULL,
			tokens_percentage INTEGER NOT NULL,
			tokens_next_reset TEXT
		);

		CREATE TABLE IF NOT EXISTS zai_hourly_usage (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL DEFAULT 'zai',
			hour TEXT NOT NULL,
			model_calls INTEGER,
			tokens_used INTEGER,
			network_searches INTEGER,
			web_reads INTEGER,
			zreads INTEGER,
			fetched_at TEXT NOT NULL,
			UNIQUE(hour)
		);

		CREATE TABLE IF NOT EXISTS zai_reset_cycles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			quota_type TEXT NOT NULL,
			cycle_start TEXT NOT NULL,
			cycle_end TEXT,
			next_reset TEXT,
			peak_value INTEGER NOT NULL DEFAULT 0,
			total_delta INTEGER NOT NULL DEFAULT 0
		);

		-- Z.ai indexes
		CREATE INDEX IF NOT EXISTS idx_zai_snapshots_captured ON zai_snapshots(captured_at);
		CREATE INDEX IF NOT EXISTS idx_zai_snapshots_tokens_reset ON zai_snapshots(tokens_next_reset);
		CREATE INDEX IF NOT EXISTS idx_zai_hourly_hour ON zai_hourly_usage(hour);
		CREATE INDEX IF NOT EXISTS idx_zai_cycles_type_start ON zai_reset_cycles(quota_type, cycle_start);
		CREATE INDEX IF NOT EXISTS idx_zai_cycles_type_active ON zai_reset_cycles(quota_type, cycle_end) WHERE cycle_end IS NULL;

		-- Anthropic-specific tables
		CREATE TABLE IF NOT EXISTS anthropic_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			captured_at TEXT NOT NULL,
			raw_json TEXT NOT NULL DEFAULT '',
			quota_count INTEGER NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS anthropic_quota_values (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			snapshot_id INTEGER NOT NULL,
			quota_name TEXT NOT NULL,
			utilization REAL NOT NULL,
			resets_at TEXT,
			FOREIGN KEY (snapshot_id) REFERENCES anthropic_snapshots(id)
		);

		CREATE TABLE IF NOT EXISTS anthropic_reset_cycles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			quota_name TEXT NOT NULL,
			cycle_start TEXT NOT NULL,
			cycle_end TEXT,
			resets_at TEXT,
			peak_utilization REAL NOT NULL DEFAULT 0,
			total_delta REAL NOT NULL DEFAULT 0
		);

		-- Anthropic indexes
		CREATE INDEX IF NOT EXISTS idx_anthropic_snapshots_captured ON anthropic_snapshots(captured_at);
		CREATE INDEX IF NOT EXISTS idx_anthropic_quota_values_snapshot ON anthropic_quota_values(snapshot_id);
		CREATE INDEX IF NOT EXISTS idx_anthropic_cycles_name_start ON anthropic_reset_cycles(quota_name, cycle_start);
		CREATE INDEX IF NOT EXISTS idx_anthropic_cycles_name_active ON anthropic_reset_cycles(quota_name, cycle_end) WHERE cycle_end IS NULL;

		-- Notification log (dedup: one row per provider + quota_key + notification_type)
		CREATE TABLE IF NOT EXISTS notification_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL DEFAULT 'legacy',
			quota_key TEXT NOT NULL,
			notification_type TEXT NOT NULL,
			sent_at TEXT NOT NULL,
			utilization REAL,
			UNIQUE(provider, quota_key, notification_type)
		);

		-- Push notification subscriptions
		CREATE TABLE IF NOT EXISTS push_subscriptions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			endpoint TEXT NOT NULL UNIQUE,
			p256dh TEXT NOT NULL,
			auth TEXT NOT NULL,
			created_at TEXT NOT NULL
		);

		-- Copilot-specific tables
		CREATE TABLE IF NOT EXISTS copilot_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			captured_at TEXT NOT NULL,
			copilot_plan TEXT,
			reset_date TEXT,
			raw_json TEXT,
			quota_count INTEGER DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS copilot_quota_values (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			snapshot_id INTEGER NOT NULL,
			quota_name TEXT NOT NULL,
			entitlement INTEGER NOT NULL DEFAULT 0,
			remaining INTEGER NOT NULL DEFAULT 0,
			percent_remaining REAL NOT NULL DEFAULT 0,
			unlimited INTEGER NOT NULL DEFAULT 0,
			overage_count INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (snapshot_id) REFERENCES copilot_snapshots(id)
		);

		CREATE TABLE IF NOT EXISTS copilot_reset_cycles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			quota_name TEXT NOT NULL,
			cycle_start TEXT NOT NULL,
			cycle_end TEXT,
			reset_date TEXT,
			peak_used INTEGER NOT NULL DEFAULT 0,
			total_delta INTEGER NOT NULL DEFAULT 0
		);

		-- Copilot indexes
		CREATE INDEX IF NOT EXISTS idx_copilot_snapshots_captured ON copilot_snapshots(captured_at);
		CREATE INDEX IF NOT EXISTS idx_copilot_quota_values_snapshot ON copilot_quota_values(snapshot_id);
		CREATE INDEX IF NOT EXISTS idx_copilot_cycles_name_start ON copilot_reset_cycles(quota_name, cycle_start);
		CREATE INDEX IF NOT EXISTS idx_copilot_cycles_name_active ON copilot_reset_cycles(quota_name) WHERE cycle_end IS NULL;

		-- Codex-specific tables
		CREATE TABLE IF NOT EXISTS codex_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			captured_at TEXT NOT NULL,
			plan_type TEXT,
			credits_balance REAL,
			raw_json TEXT,
			quota_count INTEGER DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS codex_quota_values (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			snapshot_id INTEGER NOT NULL,
			quota_name TEXT NOT NULL,
			utilization REAL NOT NULL,
			resets_at TEXT,
			status TEXT,
			FOREIGN KEY (snapshot_id) REFERENCES codex_snapshots(id)
		);

		CREATE TABLE IF NOT EXISTS codex_reset_cycles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			quota_name TEXT NOT NULL,
			cycle_start TEXT NOT NULL,
			cycle_end TEXT,
			resets_at TEXT,
			peak_utilization REAL NOT NULL DEFAULT 0,
			total_delta REAL NOT NULL DEFAULT 0
		);

		-- Codex indexes
		CREATE INDEX IF NOT EXISTS idx_codex_snapshots_captured ON codex_snapshots(captured_at);
		CREATE INDEX IF NOT EXISTS idx_codex_quota_values_snapshot ON codex_quota_values(snapshot_id);
		CREATE INDEX IF NOT EXISTS idx_codex_cycles_name_start ON codex_reset_cycles(quota_name, cycle_start);
		CREATE INDEX IF NOT EXISTS idx_codex_cycles_name_active ON codex_reset_cycles(quota_name) WHERE cycle_end IS NULL;

		-- Antigravity-specific tables
		CREATE TABLE IF NOT EXISTS antigravity_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			captured_at TEXT NOT NULL,
			email TEXT,
			plan_name TEXT,
			prompt_credits REAL,
			monthly_credits INTEGER,
			raw_json TEXT,
			model_count INTEGER DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS antigravity_model_values (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			snapshot_id INTEGER NOT NULL,
			model_id TEXT NOT NULL,
			label TEXT,
			remaining_fraction REAL NOT NULL DEFAULT 0,
			remaining_percent REAL NOT NULL DEFAULT 0,
			is_exhausted INTEGER NOT NULL DEFAULT 0,
			reset_time TEXT,
			FOREIGN KEY (snapshot_id) REFERENCES antigravity_snapshots(id)
		);

		CREATE TABLE IF NOT EXISTS antigravity_reset_cycles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			model_id TEXT NOT NULL,
			cycle_start TEXT NOT NULL,
			cycle_end TEXT,
			reset_time TEXT,
			peak_usage REAL NOT NULL DEFAULT 0,
			total_delta REAL NOT NULL DEFAULT 0
		);

		-- Antigravity indexes
		CREATE INDEX IF NOT EXISTS idx_antigravity_snapshots_captured ON antigravity_snapshots(captured_at);
		CREATE INDEX IF NOT EXISTS idx_antigravity_model_values_snapshot ON antigravity_model_values(snapshot_id);
		CREATE INDEX IF NOT EXISTS idx_antigravity_model_values_model_id ON antigravity_model_values(model_id);
		CREATE INDEX IF NOT EXISTS idx_antigravity_model_values_model_snapshot ON antigravity_model_values(model_id, snapshot_id);
		CREATE INDEX IF NOT EXISTS idx_antigravity_cycles_model_start ON antigravity_reset_cycles(model_id, cycle_start);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_antigravity_cycles_model_active_unique ON antigravity_reset_cycles(model_id) WHERE cycle_end IS NULL;
	`

	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	// Run migrations for existing databases
	if err := s.migrateSchema(); err != nil {
		return fmt.Errorf("failed to migrate schema: %w", err)
	}

	return nil
}

// migrateSchema handles schema migrations for existing databases
func (s *Store) migrateSchema() error {
	// Add provider column to quota_snapshots if not exists
	if _, err := s.db.Exec(`
		ALTER TABLE quota_snapshots ADD COLUMN provider TEXT NOT NULL DEFAULT 'synthetic'
	`); err != nil {
		// Ignore error - column might already exist
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("failed to add provider to quota_snapshots: %w", err)
		}
	}

	// Add provider column to reset_cycles if not exists
	if _, err := s.db.Exec(`
		ALTER TABLE reset_cycles ADD COLUMN provider TEXT NOT NULL DEFAULT 'synthetic'
	`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("failed to add provider to reset_cycles: %w", err)
		}
	}

	// Add provider column to sessions if not exists
	if _, err := s.db.Exec(`
		ALTER TABLE sessions ADD COLUMN provider TEXT NOT NULL DEFAULT 'synthetic'
	`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("failed to add provider to sessions: %w", err)
		}
	}

	// Add start_* columns to sessions if not exists
	for _, col := range []string{"start_sub_requests", "start_search_requests", "start_tool_requests"} {
		if _, err := s.db.Exec(fmt.Sprintf(
			`ALTER TABLE sessions ADD COLUMN %s REAL NOT NULL DEFAULT 0`, col,
		)); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("failed to add %s to sessions: %w", col, err)
			}
		}
	}

	// Add time_usage_details column to zai_snapshots if not exists
	if _, err := s.db.Exec(`
		ALTER TABLE zai_snapshots ADD COLUMN time_usage_details TEXT NOT NULL DEFAULT ''
	`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			// Table might not exist yet (new install) — ignore
			if !strings.Contains(err.Error(), "no such table") {
				return fmt.Errorf("failed to add time_usage_details to zai_snapshots: %w", err)
			}
		}
	}

	// Ensure newer Antigravity indexes exist for grouped queries.
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_antigravity_model_values_model_id ON antigravity_model_values(model_id)`,
		`CREATE INDEX IF NOT EXISTS idx_antigravity_model_values_model_snapshot ON antigravity_model_values(model_id, snapshot_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_antigravity_cycles_model_active_unique ON antigravity_reset_cycles(model_id) WHERE cycle_end IS NULL`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			if !strings.Contains(err.Error(), "no such table") {
				return fmt.Errorf("failed antigravity index migration: %w", err)
			}
		}
	}

	// Migrate notification_log to provider-scoped dedupe keys.
	if err := s.migrateNotificationLogProviderScope(); err != nil {
		return fmt.Errorf("failed to migrate notification_log provider scope: %w", err)
	}

	return nil
}

func (s *Store) migrateNotificationLogProviderScope() error {
	hasProviderCol, err := s.tableHasColumn("notification_log", "provider")
	if err != nil {
		return err
	}
	if hasProviderCol {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin notification_log migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		CREATE TABLE notification_log_v2 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL DEFAULT 'legacy',
			quota_key TEXT NOT NULL,
			notification_type TEXT NOT NULL,
			sent_at TEXT NOT NULL,
			utilization REAL,
			UNIQUE(provider, quota_key, notification_type)
		)
	`); err != nil {
		return fmt.Errorf("failed to create notification_log_v2: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO notification_log_v2 (provider, quota_key, notification_type, sent_at, utilization)
		SELECT 'legacy', quota_key, notification_type, sent_at, utilization FROM notification_log
	`); err != nil {
		return fmt.Errorf("failed to copy notification_log rows: %w", err)
	}

	if _, err := tx.Exec(`DROP TABLE notification_log`); err != nil {
		return fmt.Errorf("failed to drop old notification_log table: %w", err)
	}

	if _, err := tx.Exec(`ALTER TABLE notification_log_v2 RENAME TO notification_log`); err != nil {
		return fmt.Errorf("failed to rename notification_log_v2: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit notification_log migration: %w", err)
	}

	return nil
}

func (s *Store) tableHasColumn(tableName, columnName string) (bool, error) {
	query := fmt.Sprintf("PRAGMA table_info(%s)", tableName)
	rows, err := s.db.Query(query)
	if err != nil {
		return false, fmt.Errorf("failed to inspect table %s: %w", tableName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return false, fmt.Errorf("failed to scan table_info for %s: %w", tableName, err)
		}
		if name == columnName {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("failed to iterate table_info for %s: %w", tableName, err)
	}
	return false, nil
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.db.Close()
}

// InsertSnapshot inserts a quota snapshot
func (s *Store) InsertSnapshot(snapshot *api.Snapshot) (int64, error) {
	result, err := s.db.Exec(
		`INSERT INTO quota_snapshots 
		(captured_at, sub_limit, sub_requests, sub_renews_at, 
		 search_limit, search_requests, search_renews_at,
		 tool_limit, tool_requests, tool_renews_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snapshot.CapturedAt.Format(time.RFC3339Nano),
		snapshot.Sub.Limit, snapshot.Sub.Requests, snapshot.Sub.RenewsAt.Format(time.RFC3339Nano),
		snapshot.Search.Limit, snapshot.Search.Requests, snapshot.Search.RenewsAt.Format(time.RFC3339Nano),
		snapshot.ToolCall.Limit, snapshot.ToolCall.Requests, snapshot.ToolCall.RenewsAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert snapshot: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert ID: %w", err)
	}

	return id, nil
}

// QueryLatest returns the most recent snapshot
func (s *Store) QueryLatest() (*api.Snapshot, error) {
	var snapshot api.Snapshot
	var capturedAt, subRenewsAt, searchRenewsAt, toolRenewsAt string

	err := s.db.QueryRow(
		`SELECT id, captured_at, sub_limit, sub_requests, sub_renews_at,
		 search_limit, search_requests, search_renews_at,
		 tool_limit, tool_requests, tool_renews_at
		FROM quota_snapshots ORDER BY captured_at DESC LIMIT 1`,
	).Scan(
		&snapshot.ID, &capturedAt, &snapshot.Sub.Limit, &snapshot.Sub.Requests, &subRenewsAt,
		&snapshot.Search.Limit, &snapshot.Search.Requests, &searchRenewsAt,
		&snapshot.ToolCall.Limit, &snapshot.ToolCall.Requests, &toolRenewsAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest: %w", err)
	}

	snapshot.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
	snapshot.Sub.RenewsAt, _ = time.Parse(time.RFC3339Nano, subRenewsAt)
	snapshot.Search.RenewsAt, _ = time.Parse(time.RFC3339Nano, searchRenewsAt)
	snapshot.ToolCall.RenewsAt, _ = time.Parse(time.RFC3339Nano, toolRenewsAt)

	return &snapshot, nil
}

// QueryRange returns snapshots within a time range with optional limit.
// Pass limit=0 for no limit.
func (s *Store) QueryRange(start, end time.Time, limit ...int) ([]*api.Snapshot, error) {
	query := `SELECT id, captured_at, sub_limit, sub_requests, sub_renews_at,
		 search_limit, search_requests, search_renews_at,
		 tool_limit, tool_requests, tool_renews_at
		FROM quota_snapshots
		WHERE captured_at BETWEEN ? AND ?
		ORDER BY captured_at ASC`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query range: %w", err)
	}
	defer rows.Close()

	var snapshots []*api.Snapshot
	for rows.Next() {
		var snapshot api.Snapshot
		var capturedAt, subRenewsAt, searchRenewsAt, toolRenewsAt string

		err := rows.Scan(
			&snapshot.ID, &capturedAt, &snapshot.Sub.Limit, &snapshot.Sub.Requests, &subRenewsAt,
			&snapshot.Search.Limit, &snapshot.Search.Requests, &searchRenewsAt,
			&snapshot.ToolCall.Limit, &snapshot.ToolCall.Requests, &toolRenewsAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan snapshot: %w", err)
		}

		snapshot.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		snapshot.Sub.RenewsAt, _ = time.Parse(time.RFC3339Nano, subRenewsAt)
		snapshot.Search.RenewsAt, _ = time.Parse(time.RFC3339Nano, searchRenewsAt)
		snapshot.ToolCall.RenewsAt, _ = time.Parse(time.RFC3339Nano, toolRenewsAt)

		snapshots = append(snapshots, &snapshot)
	}

	return snapshots, rows.Err()
}

// CreateSession creates a new session with the given provider and start values.
func (s *Store) CreateSession(sessionID string, startedAt time.Time, pollInterval int, provider string, startValues ...float64) error {
	if provider == "" {
		provider = "synthetic"
	}
	var startSub, startSearch, startTool float64
	if len(startValues) > 0 {
		startSub = startValues[0]
	}
	if len(startValues) > 1 {
		startSearch = startValues[1]
	}
	if len(startValues) > 2 {
		startTool = startValues[2]
	}
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, started_at, poll_interval, provider, start_sub_requests, start_search_requests, start_tool_requests) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sessionID, startedAt.Format(time.RFC3339Nano), pollInterval, provider, startSub, startSearch, startTool,
	)
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	return nil
}

// CloseOrphanedSessions closes any sessions that were left open (e.g., process was killed).
// Sets ended_at to started_at + (snapshot_count * poll_interval) as best estimate,
// or now if no snapshots were captured.
func (s *Store) CloseOrphanedSessions() (int, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(
		`UPDATE sessions SET ended_at = ? WHERE ended_at IS NULL`,
		now,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to close orphaned sessions: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// CloseSession marks a session as ended
func (s *Store) CloseSession(sessionID string, endedAt time.Time) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET ended_at = ? WHERE id = ?`,
		endedAt.Format(time.RFC3339Nano), sessionID,
	)
	if err != nil {
		return fmt.Errorf("failed to close session: %w", err)
	}
	return nil
}

// UpdateSessionMaxRequests updates max request counts if higher
func (s *Store) UpdateSessionMaxRequests(sessionID string, sub, search, tool float64) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET
			max_sub_requests = CASE WHEN max_sub_requests < ? THEN ? ELSE max_sub_requests END,
			max_search_requests = CASE WHEN max_search_requests < ? THEN ? ELSE max_search_requests END,
			max_tool_requests = CASE WHEN max_tool_requests < ? THEN ? ELSE max_tool_requests END
		WHERE id = ?`,
		sub, sub, search, search, tool, tool, sessionID,
	)
	if err != nil {
		return fmt.Errorf("failed to update session max: %w", err)
	}
	return nil
}

// IncrementSnapshotCount increments the snapshot count for a session
func (s *Store) IncrementSnapshotCount(sessionID string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET snapshot_count = snapshot_count + 1 WHERE id = ?`,
		sessionID,
	)
	if err != nil {
		return fmt.Errorf("failed to increment snapshot count: %w", err)
	}
	return nil
}

// QueryActiveSession returns the currently active session
func (s *Store) QueryActiveSession() (*Session, error) {
	var session Session
	var startedAt string
	var endedAt sql.NullString

	err := s.db.QueryRow(
		`SELECT id, started_at, ended_at, poll_interval,
		 max_sub_requests, max_search_requests, max_tool_requests,
		 start_sub_requests, start_search_requests, start_tool_requests, snapshot_count
		FROM sessions WHERE ended_at IS NULL ORDER BY started_at DESC LIMIT 1`,
	).Scan(
		&session.ID, &startedAt, &endedAt, &session.PollInterval,
		&session.MaxSubRequests, &session.MaxSearchRequests, &session.MaxToolRequests,
		&session.StartSubRequests, &session.StartSearchRequests, &session.StartToolRequests, &session.SnapshotCount,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active session: %w", err)
	}

	session.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
	if endedAt.Valid {
		endTime, _ := time.Parse(time.RFC3339Nano, endedAt.String)
		session.EndedAt = &endTime
	}

	return &session, nil
}

// QuerySessionHistory returns sessions ordered by start time, optionally filtered by provider.
// If provider is empty, all sessions are returned. Second variadic param is limit.
func (s *Store) QuerySessionHistory(provider ...string) ([]*Session, error) {
	query := `SELECT id, started_at, ended_at, poll_interval,
		 max_sub_requests, max_search_requests, max_tool_requests,
		 start_sub_requests, start_search_requests, start_tool_requests, snapshot_count
		FROM sessions`
	var args []interface{}
	if len(provider) > 0 && provider[0] != "" {
		query += ` WHERE provider = ?`
		args = append(args, provider[0])
	}
	query += ` ORDER BY started_at DESC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		var session Session
		var startedAt string
		var endedAt sql.NullString

		err := rows.Scan(
			&session.ID, &startedAt, &endedAt, &session.PollInterval,
			&session.MaxSubRequests, &session.MaxSearchRequests, &session.MaxToolRequests,
			&session.StartSubRequests, &session.StartSearchRequests, &session.StartToolRequests, &session.SnapshotCount,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}

		session.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
		if endedAt.Valid {
			endTime, _ := time.Parse(time.RFC3339Nano, endedAt.String)
			session.EndedAt = &endTime
		}

		sessions = append(sessions, &session)
	}

	return sessions, rows.Err()
}

// CreateCycle creates a new reset cycle
func (s *Store) CreateCycle(quotaType string, cycleStart, renewsAt time.Time) (int64, error) {
	result, err := s.db.Exec(
		`INSERT INTO reset_cycles (quota_type, cycle_start, renews_at) VALUES (?, ?, ?)`,
		quotaType, cycleStart.Format(time.RFC3339Nano), renewsAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create cycle: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get cycle ID: %w", err)
	}

	return id, nil
}

// CloseCycle closes a reset cycle with final stats
func (s *Store) CloseCycle(quotaType string, cycleEnd time.Time, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE reset_cycles SET cycle_end = ?, peak_requests = ?, total_delta = ?
		WHERE quota_type = ? AND cycle_end IS NULL`,
		cycleEnd.Format(time.RFC3339Nano), peak, delta, quotaType,
	)
	if err != nil {
		return fmt.Errorf("failed to close cycle: %w", err)
	}
	return nil
}

// UpdateCycle updates the peak and delta for an active cycle
func (s *Store) UpdateCycle(quotaType string, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE reset_cycles SET peak_requests = ?, total_delta = ?
		WHERE quota_type = ? AND cycle_end IS NULL`,
		peak, delta, quotaType,
	)
	if err != nil {
		return fmt.Errorf("failed to update cycle: %w", err)
	}
	return nil
}

// QueryActiveCycle returns the active cycle for a quota type
func (s *Store) QueryActiveCycle(quotaType string) (*ResetCycle, error) {
	var cycle ResetCycle
	var cycleStart, renewsAt string

	err := s.db.QueryRow(
		`SELECT id, quota_type, cycle_start, cycle_end, renews_at, peak_requests, total_delta
		FROM reset_cycles WHERE quota_type = ? AND cycle_end IS NULL`,
		quotaType,
	).Scan(
		&cycle.ID, &cycle.QuotaType, &cycleStart, &cycle.CycleEnd, &renewsAt, &cycle.PeakRequests, &cycle.TotalDelta,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active cycle: %w", err)
	}

	cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
	cycle.RenewsAt, _ = time.Parse(time.RFC3339Nano, renewsAt)

	return &cycle, nil
}

// QueryCycleHistory returns completed cycles for a quota type with optional limit.
func (s *Store) QueryCycleHistory(quotaType string, limit ...int) ([]*ResetCycle, error) {
	query := `SELECT id, quota_type, cycle_start, cycle_end, renews_at, peak_requests, total_delta
		FROM reset_cycles WHERE quota_type = ? AND cycle_end IS NOT NULL ORDER BY cycle_start DESC`
	args := []interface{}{quotaType}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query cycles: %w", err)
	}
	defer rows.Close()

	var cycles []*ResetCycle
	for rows.Next() {
		var cycle ResetCycle
		var cycleStart, cycleEnd, renewsAt string

		err := rows.Scan(
			&cycle.ID, &cycle.QuotaType, &cycleStart, &cycleEnd, &renewsAt, &cycle.PeakRequests, &cycle.TotalDelta,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		cycle.RenewsAt, _ = time.Parse(time.RFC3339Nano, renewsAt)
		endTime, _ := time.Parse(time.RFC3339Nano, cycleEnd)
		cycle.CycleEnd = &endTime

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// QueryCyclesSince returns all cycles (completed and active) for a quota type since a given time
func (s *Store) QueryCyclesSince(quotaType string, since time.Time) ([]*ResetCycle, error) {
	rows, err := s.db.Query(
		`SELECT id, quota_type, cycle_start, cycle_end, renews_at, peak_requests, total_delta
		FROM reset_cycles WHERE quota_type = ? AND cycle_start >= ? ORDER BY cycle_start DESC`,
		quotaType, since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query cycles since: %w", err)
	}
	defer rows.Close()

	var cycles []*ResetCycle
	for rows.Next() {
		var cycle ResetCycle
		var cycleStart, renewsAt string
		var cycleEnd sql.NullString

		err := rows.Scan(
			&cycle.ID, &cycle.QuotaType, &cycleStart, &cycleEnd, &renewsAt, &cycle.PeakRequests, &cycle.TotalDelta,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		cycle.RenewsAt, _ = time.Parse(time.RFC3339Nano, renewsAt)
		if cycleEnd.Valid {
			endTime, _ := time.Parse(time.RFC3339Nano, cycleEnd.String)
			cycle.CycleEnd = &endTime
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// QuerySyntheticCycleOverview returns cycles for a given quota type
// with cross-quota snapshot data at the peak moment of each cycle.
// Includes the currently active cycle (if any) at the top.
func (s *Store) QuerySyntheticCycleOverview(groupBy string, limit int) ([]CycleOverviewRow, error) {
	if limit <= 0 {
		limit = 50
	}

	// Get active cycle first (if any)
	var allCycles []*ResetCycle
	activeCycle, err := s.QueryActiveCycle(groupBy)
	if err != nil {
		return nil, fmt.Errorf("store.QuerySyntheticCycleOverview: active: %w", err)
	}
	if activeCycle != nil {
		allCycles = append(allCycles, activeCycle)
		limit-- // Reduce limit for completed cycles
	}

	// Get completed cycles
	completedCycles, err := s.QueryCycleHistory(groupBy, limit)
	if err != nil {
		return nil, fmt.Errorf("store.QuerySyntheticCycleOverview: %w", err)
	}
	allCycles = append(allCycles, completedCycles...)

	var rows []CycleOverviewRow
	for _, c := range allCycles {
		row := CycleOverviewRow{
			CycleID:    c.ID,
			QuotaType:  c.QuotaType,
			CycleStart: c.CycleStart,
			CycleEnd:   c.CycleEnd,
			PeakValue:  c.PeakRequests,
			TotalDelta: c.TotalDelta,
		}

		// Find the snapshot at peak time for the primary quota within this cycle
		var peakCol string
		switch groupBy {
		case "subscription":
			peakCol = "sub_requests"
		case "search":
			peakCol = "search_requests"
		case "toolcall":
			peakCol = "tool_requests"
		default:
			peakCol = "sub_requests"
		}

		// Determine the end boundary for the snapshot query
		// For active cycles (no cycle_end), use current time
		// For completed cycles, use cycle_end (exclusive, as it's the first snapshot of NEW cycle)
		var endBoundary time.Time
		if c.CycleEnd != nil {
			endBoundary = *c.CycleEnd
		} else {
			endBoundary = time.Now().Add(time.Minute) // Include current snapshots
		}

		var capturedAt string
		var subLimit, subReq, searchLimit, searchReq, toolLimit, toolReq float64
		err = s.db.QueryRow(
			fmt.Sprintf(`SELECT captured_at, sub_limit, sub_requests, search_limit, search_requests, tool_limit, tool_requests
			FROM quota_snapshots
			WHERE captured_at >= ? AND captured_at < ?
			ORDER BY %s DESC LIMIT 1`, peakCol),
			c.CycleStart.Format(time.RFC3339Nano),
			endBoundary.Format(time.RFC3339Nano),
		).Scan(&capturedAt, &subLimit, &subReq, &searchLimit, &searchReq, &toolLimit, &toolReq)

		if err == sql.ErrNoRows {
			rows = append(rows, row)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("store.QuerySyntheticCycleOverview: peak snapshot: %w", err)
		}

		row.PeakTime, _ = time.Parse(time.RFC3339Nano, capturedAt)

		pct := func(val, lim float64) float64 {
			if lim == 0 {
				return 0
			}
			return val / lim * 100
		}
		row.CrossQuotas = []CrossQuotaEntry{
			{Name: "subscription", Value: subReq, Limit: subLimit, Percent: pct(subReq, subLimit)},
			{Name: "search", Value: searchReq, Limit: searchLimit, Percent: pct(searchReq, searchLimit)},
			{Name: "toolcall", Value: toolReq, Limit: toolLimit, Percent: pct(toolReq, toolLimit)},
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// GetSetting returns the value for a setting key. Returns "" if not found.
func (s *Store) GetSetting(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store.GetSetting: %w", err)
	}
	return value, nil
}

// SetSetting inserts or replaces a setting value.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value)
	if err != nil {
		return fmt.Errorf("store.SetSetting: %w", err)
	}
	return nil
}

// SaveAuthToken persists a session token with its expiry.
func (s *Store) SaveAuthToken(token string, expiresAt time.Time) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO auth_tokens (token, expires_at) VALUES (?, ?)",
		token, expiresAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("store.SaveAuthToken: %w", err)
	}
	return nil
}

// GetAuthTokenExpiry returns the expiry time for a token. Returns zero time and false if not found.
func (s *Store) GetAuthTokenExpiry(token string) (time.Time, bool, error) {
	var expiresAtStr string
	err := s.db.QueryRow("SELECT expires_at FROM auth_tokens WHERE token = ?", token).Scan(&expiresAtStr)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("store.GetAuthTokenExpiry: %w", err)
	}
	t, _ := time.Parse(time.RFC3339Nano, expiresAtStr)
	return t, true, nil
}

// DeleteAuthToken removes a session token.
func (s *Store) DeleteAuthToken(token string) error {
	_, err := s.db.Exec("DELETE FROM auth_tokens WHERE token = ?", token)
	if err != nil {
		return fmt.Errorf("store.DeleteAuthToken: %w", err)
	}
	return nil
}

// CleanExpiredAuthTokens removes all expired tokens.
func (s *Store) CleanExpiredAuthTokens() error {
	_, err := s.db.Exec("DELETE FROM auth_tokens WHERE expires_at < ?", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store.CleanExpiredAuthTokens: %w", err)
	}
	return nil
}

// GetUser returns the password hash for a username. Returns "" if not found.
func (s *Store) GetUser(username string) (string, error) {
	var hash string
	err := s.db.QueryRow("SELECT password_hash FROM users WHERE username = ?", username).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store.GetUser: %w", err)
	}
	return hash, nil
}

// UpsertUser inserts or updates a user's password hash.
func (s *Store) UpsertUser(username, passwordHash string) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO users (username, password_hash, updated_at) VALUES (?, ?, ?)",
		username, passwordHash, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("store.UpsertUser: %w", err)
	}
	return nil
}

// MigrateSessionsToUsageBased recomputes sessions from historical snapshot data
// using usage-based idle detection. It deletes all existing sessions (which represent
// agent runs, not actual usage) and creates new ones based on when API values changed.
// This runs once on first upgrade, controlled by a settings flag.
func (s *Store) MigrateSessionsToUsageBased(idleTimeout time.Duration) error {
	// Check if migration already done
	done, err := s.GetSetting("session_migration_v2")
	if err != nil {
		return fmt.Errorf("store.MigrateSessionsToUsageBased: check flag: %w", err)
	}
	if done == "done" {
		return nil
	}

	// Delete all existing sessions
	if _, err := s.db.Exec("DELETE FROM sessions"); err != nil {
		return fmt.Errorf("store.MigrateSessionsToUsageBased: delete sessions: %w", err)
	}

	// Migrate each provider
	if err := s.migrateSyntheticSessions(idleTimeout); err != nil {
		return fmt.Errorf("store.MigrateSessionsToUsageBased: synthetic: %w", err)
	}
	if err := s.migrateZaiSessions(idleTimeout); err != nil {
		return fmt.Errorf("store.MigrateSessionsToUsageBased: zai: %w", err)
	}
	if err := s.migrateAnthropicSessions(idleTimeout); err != nil {
		return fmt.Errorf("store.MigrateSessionsToUsageBased: anthropic: %w", err)
	}

	// Mark migration as complete
	if err := s.SetSetting("session_migration_v2", "done"); err != nil {
		return fmt.Errorf("store.MigrateSessionsToUsageBased: set flag: %w", err)
	}

	return nil
}

// migrateSyntheticSessions walks through synthetic snapshots and creates usage-based sessions.
func (s *Store) migrateSyntheticSessions(idleTimeout time.Duration) error {
	rows, err := s.db.Query(
		`SELECT captured_at, sub_requests, search_requests, tool_requests
		FROM quota_snapshots ORDER BY captured_at ASC`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	return s.walkSnapshots(rows, "synthetic", idleTimeout, 3)
}

// migrateZaiSessions walks through Z.ai snapshots and creates usage-based sessions.
func (s *Store) migrateZaiSessions(idleTimeout time.Duration) error {
	rows, err := s.db.Query(
		`SELECT captured_at, tokens_current_value, time_current_value
		FROM zai_snapshots ORDER BY captured_at ASC`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	return s.walkSnapshots(rows, "zai", idleTimeout, 2)
}

// migrateAnthropicSessions walks through Anthropic snapshots and creates usage-based sessions.
func (s *Store) migrateAnthropicSessions(idleTimeout time.Duration) error {
	// Anthropic has normalized data — we need to pivot per-snapshot quota values into a flat slice.
	// Walk snapshot by snapshot.
	snapRows, err := s.db.Query(
		`SELECT id, captured_at FROM anthropic_snapshots ORDER BY captured_at ASC`,
	)
	if err != nil {
		return err
	}
	defer snapRows.Close()

	type snapshotRow struct {
		id         int64
		capturedAt time.Time
	}
	var snapshots []snapshotRow
	for snapRows.Next() {
		var sr snapshotRow
		var capturedAtStr string
		if err := snapRows.Scan(&sr.id, &capturedAtStr); err != nil {
			return err
		}
		sr.capturedAt, _ = time.Parse(time.RFC3339Nano, capturedAtStr)
		snapshots = append(snapshots, sr)
	}
	if err := snapRows.Err(); err != nil {
		return err
	}

	if len(snapshots) == 0 {
		return nil
	}

	var (
		sessionID        string
		sessionStart     time.Time
		lastActivityTime time.Time
		prevValues       map[string]float64
		snapshotCount    int
		maxVals          [3]float64 // sub, search, tool mapped from utilization
	)

	closeSession := func(endTime time.Time) error {
		if sessionID == "" {
			return nil
		}
		_, err := s.db.Exec(
			`UPDATE sessions SET ended_at = ?, snapshot_count = ?,
			 max_sub_requests = ?, max_search_requests = ?, max_tool_requests = ?
			 WHERE id = ?`,
			endTime.Format(time.RFC3339Nano), snapshotCount,
			maxVals[0], maxVals[1], maxVals[2],
			sessionID,
		)
		sessionID = ""
		snapshotCount = 0
		maxVals = [3]float64{}
		return err
	}

	for _, snap := range snapshots {
		// Load quota values for this snapshot
		qRows, err := s.db.Query(
			`SELECT quota_name, utilization FROM anthropic_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
			snap.id,
		)
		if err != nil {
			return err
		}

		currentValues := make(map[string]float64)
		for qRows.Next() {
			var name string
			var util float64
			if err := qRows.Scan(&name, &util); err != nil {
				qRows.Close()
				return err
			}
			currentValues[name] = util
		}
		qRows.Close()

		// Determine if values changed
		changed := false
		if prevValues == nil {
			// First snapshot — baseline
			prevValues = currentValues
			continue
		}

		if len(currentValues) != len(prevValues) {
			changed = true
		} else {
			for k, v := range currentValues {
				if pv, ok := prevValues[k]; !ok || pv != v {
					changed = true
					break
				}
			}
		}
		prevValues = currentValues

		if changed {
			if sessionID == "" {
				// Start new session
				sessionID = fmt.Sprintf("migrated-anthropic-%d", snap.id)
				sessionStart = snap.capturedAt
				lastActivityTime = snap.capturedAt
				if _, err := s.db.Exec(
					`INSERT INTO sessions (id, started_at, poll_interval, provider) VALUES (?, ?, 0, 'anthropic')`,
					sessionID, sessionStart.Format(time.RFC3339Nano),
				); err != nil {
					return err
				}
			}
			lastActivityTime = snap.capturedAt
			snapshotCount++

			// Track max utilization values
			i := 0
			for _, v := range currentValues {
				if i < 3 && v > maxVals[i] {
					maxVals[i] = v
				}
				i++
			}
		} else {
			// No change
			if sessionID != "" {
				if snap.capturedAt.Sub(lastActivityTime) > idleTimeout {
					if err := closeSession(lastActivityTime.Add(idleTimeout)); err != nil {
						return err
					}
				} else {
					snapshotCount++
				}
			}
		}
	}

	// Close any remaining open session
	return closeSession(lastActivityTime.Add(idleTimeout))
}

// walkSnapshots is a generic helper that walks DB rows (captured_at + N float64 values)
// and creates usage-based sessions.
func (s *Store) walkSnapshots(rows *sql.Rows, provider string, idleTimeout time.Duration, valueCount int) error {
	var (
		sessionID        string
		sessionStart     time.Time
		lastActivityTime time.Time
		prevValues       []float64
		snapshotCount    int
		maxVals          [3]float64
		rowIndex         int
	)

	closeSession := func(endTime time.Time) error {
		if sessionID == "" {
			return nil
		}
		_, err := s.db.Exec(
			`UPDATE sessions SET ended_at = ?, snapshot_count = ?,
			 max_sub_requests = ?, max_search_requests = ?, max_tool_requests = ?
			 WHERE id = ?`,
			endTime.Format(time.RFC3339Nano), snapshotCount,
			maxVals[0], maxVals[1], maxVals[2],
			sessionID,
		)
		sessionID = ""
		snapshotCount = 0
		maxVals = [3]float64{}
		return err
	}

	for rows.Next() {
		rowIndex++
		var capturedAtStr string
		values := make([]float64, valueCount)
		scanArgs := make([]interface{}, 1+valueCount)
		scanArgs[0] = &capturedAtStr
		for i := range values {
			scanArgs[i+1] = &values[i]
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return err
		}
		capturedAt, _ := time.Parse(time.RFC3339Nano, capturedAtStr)

		// Determine if values changed
		changed := false
		if prevValues == nil {
			prevValues = values
			continue
		}
		for i, v := range values {
			if v != prevValues[i] {
				changed = true
				break
			}
		}
		prevValues = make([]float64, valueCount)
		copy(prevValues, values)

		if changed {
			if sessionID == "" {
				sessionID = fmt.Sprintf("migrated-%s-%d", provider, rowIndex)
				sessionStart = capturedAt
				lastActivityTime = capturedAt
				if _, err := s.db.Exec(
					`INSERT INTO sessions (id, started_at, poll_interval, provider) VALUES (?, ?, 0, ?)`,
					sessionID, sessionStart.Format(time.RFC3339Nano), provider,
				); err != nil {
					return err
				}
			}
			lastActivityTime = capturedAt
			snapshotCount++

			for i := 0; i < 3 && i < len(values); i++ {
				if values[i] > maxVals[i] {
					maxVals[i] = values[i]
				}
			}
		} else {
			if sessionID != "" {
				if capturedAt.Sub(lastActivityTime) > idleTimeout {
					if err := closeSession(lastActivityTime.Add(idleTimeout)); err != nil {
						return err
					}
				} else {
					snapshotCount++
				}
			}
		}
	}

	if err := rows.Err(); err != nil {
		return err
	}

	// Close any remaining open session
	return closeSession(lastActivityTime.Add(idleTimeout))
}

// DeleteAllAuthTokens removes all session tokens (used after password change).
func (s *Store) DeleteAllAuthTokens() error {
	_, err := s.db.Exec("DELETE FROM auth_tokens")
	if err != nil {
		return fmt.Errorf("store.DeleteAllAuthTokens: %w", err)
	}
	return nil
}

// UpsertNotificationLog inserts or replaces a notification log entry.
// The UNIQUE(provider, quota_key, notification_type) constraint ensures only the
// most recent notification per provider+quota+type pair is kept.
func (s *Store) UpsertNotificationLog(provider, quotaKey, notifType string, util float64) error {
	if provider == "" {
		provider = "legacy"
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO notification_log (provider, quota_key, notification_type, sent_at, utilization)
		 VALUES (?, ?, ?, ?, ?)`,
		provider, quotaKey, notifType, time.Now().UTC().Format(time.RFC3339Nano), util,
	)
	if err != nil {
		return fmt.Errorf("store.UpsertNotificationLog: %w", err)
	}
	return nil
}

// GetLastNotification returns the last notification time and utilization for a provider+quota+type pair.
// Returns zero time and 0 utilization if no entry exists.
func (s *Store) GetLastNotification(provider, quotaKey, notifType string) (time.Time, float64, error) {
	if provider == "" {
		provider = "legacy"
	}
	var sentAtStr string
	var util float64
	err := s.db.QueryRow(
		`SELECT sent_at, utilization FROM notification_log
		WHERE provider = ? AND quota_key = ? AND notification_type = ?`,
		provider, quotaKey, notifType,
	).Scan(&sentAtStr, &util)
	if err == sql.ErrNoRows {
		return time.Time{}, 0, nil
	}
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("store.GetLastNotification: %w", err)
	}
	sentAt, _ := time.Parse(time.RFC3339Nano, sentAtStr)
	return sentAt, util, nil
}

// ClearNotificationLog removes all notification log entries for a provider+quota key.
// Called on quota reset to allow notifications to fire again in the new cycle.
func (s *Store) ClearNotificationLog(provider, quotaKey string) error {
	if provider == "" {
		provider = "legacy"
	}
	_, err := s.db.Exec(`DELETE FROM notification_log WHERE provider = ? AND quota_key = ?`, provider, quotaKey)
	if err != nil {
		return fmt.Errorf("store.ClearNotificationLog: %w", err)
	}
	return nil
}

// SavePushSubscription stores a push notification subscription (upsert by endpoint).
// Validates endpoint, p256dh, and auth before storing.
func (s *Store) SavePushSubscription(endpoint, p256dh, auth string) error {
	// Validate endpoint must be HTTPS
	if !strings.HasPrefix(endpoint, "https://") {
		return errors.New("store.SavePushSubscription: endpoint must use HTTPS")
	}

	// Validate p256dh is valid base64url and decodes to 65 bytes (uncompressed P-256 point)
	if p256dhBytes, err := base64.RawURLEncoding.DecodeString(p256dh); err != nil || len(p256dhBytes) != 65 {
		return errors.New("store.SavePushSubscription: p256dh must be base64url-encoded 65-byte P-256 point")
	}

	// Validate auth is valid base64url and decodes to 16 bytes
	if authBytes, err := base64.RawURLEncoding.DecodeString(auth); err != nil || len(authBytes) != 16 {
		return errors.New("store.SavePushSubscription: auth must be base64url-encoded 16-byte secret")
	}

	_, err := s.db.Exec(`
		INSERT INTO push_subscriptions (endpoint, p256dh, auth, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh = ?, auth = ?`,
		endpoint, p256dh, auth, time.Now().UTC().Format(time.RFC3339),
		p256dh, auth,
	)
	if err != nil {
		return fmt.Errorf("store.SavePushSubscription: %w", err)
	}
	return nil
}

// DeletePushSubscription removes a push subscription by endpoint.
func (s *Store) DeletePushSubscription(endpoint string) error {
	_, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	if err != nil {
		return fmt.Errorf("store.DeletePushSubscription: %w", err)
	}
	return nil
}

// PushSubscriptionRow represents a stored push subscription.
type PushSubscriptionRow struct {
	Endpoint string
	P256dh   string
	Auth     string
}

// GetPushSubscriptions returns all stored push subscriptions.
func (s *Store) GetPushSubscriptions() ([]PushSubscriptionRow, error) {
	rows, err := s.db.Query(`SELECT endpoint, p256dh, auth FROM push_subscriptions`)
	if err != nil {
		return nil, fmt.Errorf("store.GetPushSubscriptions: %w", err)
	}
	defer rows.Close()

	var subs []PushSubscriptionRow
	for rows.Next() {
		var sub PushSubscriptionRow
		if err := rows.Scan(&sub.Endpoint, &sub.P256dh, &sub.Auth); err != nil {
			return nil, fmt.Errorf("store.GetPushSubscriptions: scan: %w", err)
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}
