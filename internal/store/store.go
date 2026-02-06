package store

import (
	"database/sql"
	"fmt"
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
			quota_type TEXT NOT NULL,
			cycle_start TEXT NOT NULL,
			cycle_end TEXT,
			renews_at TEXT NOT NULL,
			peak_requests REAL NOT NULL DEFAULT 0,
			total_delta REAL NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
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
	`

	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
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

// QueryRange returns snapshots within a time range
func (s *Store) QueryRange(start, end time.Time) ([]*api.Snapshot, error) {
	rows, err := s.db.Query(
		`SELECT id, captured_at, sub_limit, sub_requests, sub_renews_at,
		 search_limit, search_requests, search_renews_at,
		 tool_limit, tool_requests, tool_renews_at
		FROM quota_snapshots 
		WHERE captured_at BETWEEN ? AND ?
		ORDER BY captured_at ASC`,
		start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano),
	)
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

// CreateSession creates a new session
func (s *Store) CreateSession(sessionID string, startedAt time.Time, pollInterval int) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, started_at, poll_interval) VALUES (?, ?, ?)`,
		sessionID, startedAt.Format(time.RFC3339Nano), pollInterval,
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

// QuerySessionHistory returns all sessions ordered by start time
func (s *Store) QuerySessionHistory() ([]*Session, error) {
	rows, err := s.db.Query(
		`SELECT id, started_at, ended_at, poll_interval,
		 max_sub_requests, max_search_requests, max_tool_requests, snapshot_count
		FROM sessions ORDER BY started_at DESC`,
	)
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

// QueryCycleHistory returns completed cycles for a quota type
func (s *Store) QueryCycleHistory(quotaType string) ([]*ResetCycle, error) {
	rows, err := s.db.Query(
		`SELECT id, quota_type, cycle_start, cycle_end, renews_at, peak_requests, total_delta
		FROM reset_cycles WHERE quota_type = ? AND cycle_end IS NOT NULL ORDER BY cycle_start DESC`,
		quotaType,
	)
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
