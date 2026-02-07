package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/onllm-dev/syntrack/internal/api"
	_ "modernc.org/sqlite"
)

// Store provides SQLite storage for SynTrack
type Store struct {
	db *sql.DB
}

// Session represents an agent session
type Session struct {
	ID                string
	StartedAt         time.Time
	EndedAt           *time.Time
	PollInterval      int
	MaxSubRequests    float64
	MaxSearchRequests float64
	MaxToolRequests   float64
	SnapshotCount     int
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

// New creates a new Store with the given database path
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure SQLite for RAM efficiency
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA cache_size=-2000;",
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

	return nil
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

// CreateSession creates a new session with the given provider
func (s *Store) CreateSession(sessionID string, startedAt time.Time, pollInterval int, provider string) error {
	if provider == "" {
		provider = "synthetic"
	}
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, started_at, poll_interval, provider) VALUES (?, ?, ?, ?)`,
		sessionID, startedAt.Format(time.RFC3339Nano), pollInterval, provider,
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
		 max_sub_requests, max_search_requests, max_tool_requests, snapshot_count
		FROM sessions WHERE ended_at IS NULL ORDER BY started_at DESC LIMIT 1`,
	).Scan(
		&session.ID, &startedAt, &endedAt, &session.PollInterval,
		&session.MaxSubRequests, &session.MaxSearchRequests, &session.MaxToolRequests, &session.SnapshotCount,
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
		 max_sub_requests, max_search_requests, max_tool_requests, snapshot_count
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
			&session.MaxSubRequests, &session.MaxSearchRequests, &session.MaxToolRequests, &session.SnapshotCount,
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

// DeleteAllAuthTokens removes all session tokens (used after password change).
func (s *Store) DeleteAllAuthTokens() error {
	_, err := s.db.Exec("DELETE FROM auth_tokens")
	if err != nil {
		return fmt.Errorf("store.DeleteAllAuthTokens: %w", err)
	}
	return nil
}
