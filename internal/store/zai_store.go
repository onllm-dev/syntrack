package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/onllm-dev/syntrack/internal/api"
)

// ZaiResetCycle represents a Z.ai quota reset cycle
type ZaiResetCycle struct {
	ID         int64
	QuotaType  string
	CycleStart time.Time
	CycleEnd   *time.Time
	NextReset  *time.Time
	PeakValue  int64
	TotalDelta int64
}

// ZaiHourlyUsage represents hourly usage data from Z.ai
type ZaiHourlyUsage struct {
	ID              int64
	Hour            string
	ModelCalls      *int64
	TokensUsed      *int64
	NetworkSearches *int64
	WebReads        *int64
	Zreads          *int64
	FetchedAt       time.Time
}

// InsertZaiSnapshot inserts a Z.ai quota snapshot
func (s *Store) InsertZaiSnapshot(snapshot *api.ZaiSnapshot) (int64, error) {
	var tokensNextReset interface{}
	if snapshot.TokensNextResetTime != nil {
		tokensNextReset = snapshot.TokensNextResetTime.Format(time.RFC3339Nano)
	} else {
		tokensNextReset = nil
	}

	result, err := s.db.Exec(
		`INSERT INTO zai_snapshots
		(provider, captured_at, time_limit, time_unit, time_number, time_usage,
		 time_current_value, time_remaining, time_percentage, time_usage_details,
		 tokens_limit, tokens_unit, tokens_number, tokens_usage,
		 tokens_current_value, tokens_remaining, tokens_percentage, tokens_next_reset)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"zai",
		snapshot.CapturedAt.Format(time.RFC3339Nano),
		snapshot.TimeLimit, snapshot.TimeUnit, snapshot.TimeNumber,
		snapshot.TimeUsage, snapshot.TimeCurrentValue, snapshot.TimeRemaining, snapshot.TimePercentage,
		snapshot.TimeUsageDetails,
		snapshot.TokensLimit, snapshot.TokensUnit, snapshot.TokensNumber,
		snapshot.TokensUsage, snapshot.TokensCurrentValue, snapshot.TokensRemaining, snapshot.TokensPercentage,
		tokensNextReset,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert zai snapshot: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert ID: %w", err)
	}

	return id, nil
}

// QueryLatestZai returns the most recent Z.ai snapshot
func (s *Store) QueryLatestZai() (*api.ZaiSnapshot, error) {
	var snapshot api.ZaiSnapshot
	var capturedAt string
	var tokensNextReset sql.NullString

	err := s.db.QueryRow(
		`SELECT id, captured_at, time_limit, time_unit, time_number, time_usage,
		 time_current_value, time_remaining, time_percentage, time_usage_details,
		 tokens_limit, tokens_unit, tokens_number, tokens_usage,
		 tokens_current_value, tokens_remaining, tokens_percentage, tokens_next_reset
		FROM zai_snapshots ORDER BY captured_at DESC LIMIT 1`,
	).Scan(
		&snapshot.ID, &capturedAt, &snapshot.TimeLimit, &snapshot.TimeUnit, &snapshot.TimeNumber,
		&snapshot.TimeUsage, &snapshot.TimeCurrentValue, &snapshot.TimeRemaining, &snapshot.TimePercentage,
		&snapshot.TimeUsageDetails,
		&snapshot.TokensLimit, &snapshot.TokensUnit, &snapshot.TokensNumber,
		&snapshot.TokensUsage, &snapshot.TokensCurrentValue, &snapshot.TokensRemaining, &snapshot.TokensPercentage,
		&tokensNextReset,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest zai: %w", err)
	}

	snapshot.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
	if tokensNextReset.Valid && tokensNextReset.String != "" {
		t, _ := time.Parse(time.RFC3339Nano, tokensNextReset.String)
		snapshot.TokensNextResetTime = &t
	}

	return &snapshot, nil
}

// QueryZaiRange returns Z.ai snapshots within a time range with optional limit.
func (s *Store) QueryZaiRange(start, end time.Time, limit ...int) ([]*api.ZaiSnapshot, error) {
	query := `SELECT id, captured_at, time_limit, time_unit, time_number, time_usage,
		 time_current_value, time_remaining, time_percentage, time_usage_details,
		 tokens_limit, tokens_unit, tokens_number, tokens_usage,
		 tokens_current_value, tokens_remaining, tokens_percentage, tokens_next_reset
		FROM zai_snapshots
		WHERE captured_at BETWEEN ? AND ?
		ORDER BY captured_at ASC`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query zai range: %w", err)
	}
	defer rows.Close()

	var snapshots []*api.ZaiSnapshot
	for rows.Next() {
		var snapshot api.ZaiSnapshot
		var capturedAt string
		var tokensNextReset sql.NullString

		err := rows.Scan(
			&snapshot.ID, &capturedAt, &snapshot.TimeLimit, &snapshot.TimeUnit, &snapshot.TimeNumber,
			&snapshot.TimeUsage, &snapshot.TimeCurrentValue, &snapshot.TimeRemaining, &snapshot.TimePercentage,
			&snapshot.TimeUsageDetails,
			&snapshot.TokensLimit, &snapshot.TokensUnit, &snapshot.TokensNumber,
			&snapshot.TokensUsage, &snapshot.TokensCurrentValue, &snapshot.TokensRemaining, &snapshot.TokensPercentage,
			&tokensNextReset,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan zai snapshot: %w", err)
		}

		snapshot.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		if tokensNextReset.Valid && tokensNextReset.String != "" {
			t, _ := time.Parse(time.RFC3339Nano, tokensNextReset.String)
			snapshot.TokensNextResetTime = &t
		}

		snapshots = append(snapshots, &snapshot)
	}

	return snapshots, rows.Err()
}

// CreateZaiCycle creates a new Z.ai reset cycle
func (s *Store) CreateZaiCycle(quotaType string, cycleStart time.Time, nextReset *time.Time) (int64, error) {
	var nextResetValue interface{}
	if nextReset != nil {
		nextResetValue = nextReset.Format(time.RFC3339Nano)
	} else {
		nextResetValue = nil
	}

	result, err := s.db.Exec(
		`INSERT INTO zai_reset_cycles (quota_type, cycle_start, next_reset) VALUES (?, ?, ?)`,
		quotaType, cycleStart.Format(time.RFC3339Nano), nextResetValue,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create zai cycle: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get cycle ID: %w", err)
	}

	return id, nil
}

// CloseZaiCycle closes a Z.ai reset cycle with final stats
func (s *Store) CloseZaiCycle(quotaType string, cycleEnd time.Time, peak, delta int64) error {
	_, err := s.db.Exec(
		`UPDATE zai_reset_cycles SET cycle_end = ?, peak_value = ?, total_delta = ?
		WHERE quota_type = ? AND cycle_end IS NULL`,
		cycleEnd.Format(time.RFC3339Nano), peak, delta, quotaType,
	)
	if err != nil {
		return fmt.Errorf("failed to close zai cycle: %w", err)
	}
	return nil
}

// UpdateZaiCycle updates the peak and delta for an active Z.ai cycle
func (s *Store) UpdateZaiCycle(quotaType string, peak, delta int64) error {
	_, err := s.db.Exec(
		`UPDATE zai_reset_cycles SET peak_value = ?, total_delta = ?
		WHERE quota_type = ? AND cycle_end IS NULL`,
		peak, delta, quotaType,
	)
	if err != nil {
		return fmt.Errorf("failed to update zai cycle: %w", err)
	}
	return nil
}

// QueryActiveZaiCycle returns the active cycle for a Z.ai quota type
func (s *Store) QueryActiveZaiCycle(quotaType string) (*ZaiResetCycle, error) {
	var cycle ZaiResetCycle
	var cycleStart string
	var cycleEnd, nextReset sql.NullString

	err := s.db.QueryRow(
		`SELECT id, quota_type, cycle_start, cycle_end, next_reset, peak_value, total_delta
		FROM zai_reset_cycles WHERE quota_type = ? AND cycle_end IS NULL`,
		quotaType,
	).Scan(
		&cycle.ID, &cycle.QuotaType, &cycleStart, &cycleEnd, &nextReset, &cycle.PeakValue, &cycle.TotalDelta,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active zai cycle: %w", err)
	}

	cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
	if cycleEnd.Valid {
		endTime, _ := time.Parse(time.RFC3339Nano, cycleEnd.String)
		cycle.CycleEnd = &endTime
	}
	if nextReset.Valid {
		resetTime, _ := time.Parse(time.RFC3339Nano, nextReset.String)
		cycle.NextReset = &resetTime
	}

	return &cycle, nil
}

// InsertZaiHourlyUsage inserts or updates hourly usage data
func (s *Store) InsertZaiHourlyUsage(hour string, modelCalls, tokensUsed, networkSearches, webReads, zreads int64) error {
	_, err := s.db.Exec(
		`INSERT INTO zai_hourly_usage (provider, hour, model_calls, tokens_used, network_searches, web_reads, zreads, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(hour) DO UPDATE SET
			model_calls = excluded.model_calls,
			tokens_used = excluded.tokens_used,
			network_searches = excluded.network_searches,
			web_reads = excluded.web_reads,
			zreads = excluded.zreads,
			fetched_at = excluded.fetched_at`,
		"zai", hour, modelCalls, tokensUsed, networkSearches, webReads, zreads,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("failed to insert zai hourly usage: %w", err)
	}
	return nil
}

// QueryZaiHourlyUsage returns hourly usage within a time range
func (s *Store) QueryZaiHourlyUsage(start, end time.Time) ([]*ZaiHourlyUsage, error) {
	startHour := start.Format("2006-01-02 15:00")
	endHour := end.Format("2006-01-02 15:00")

	rows, err := s.db.Query(
		`SELECT id, hour, model_calls, tokens_used, network_searches, web_reads, zreads, fetched_at
		FROM zai_hourly_usage 
		WHERE hour BETWEEN ? AND ?
		ORDER BY hour ASC`,
		startHour, endHour,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query zai hourly usage: %w", err)
	}
	defer rows.Close()

	var usages []*ZaiHourlyUsage
	for rows.Next() {
		var usage ZaiHourlyUsage
		var fetchedAt string
		var modelCalls, tokensUsed, networkSearches, webReads, zreads sql.NullInt64

		err := rows.Scan(
			&usage.ID, &usage.Hour, &modelCalls, &tokensUsed, &networkSearches, &webReads, &zreads, &fetchedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan zai hourly usage: %w", err)
		}

		if modelCalls.Valid {
			usage.ModelCalls = &modelCalls.Int64
		}
		if tokensUsed.Valid {
			usage.TokensUsed = &tokensUsed.Int64
		}
		if networkSearches.Valid {
			usage.NetworkSearches = &networkSearches.Int64
		}
		if webReads.Valid {
			usage.WebReads = &webReads.Int64
		}
		if zreads.Valid {
			usage.Zreads = &zreads.Int64
		}
		usage.FetchedAt, _ = time.Parse(time.RFC3339Nano, fetchedAt)

		usages = append(usages, &usage)
	}

	return usages, rows.Err()
}

// QueryZaiCycleHistory returns completed cycles for a Z.ai quota type with optional limit.
func (s *Store) QueryZaiCycleHistory(quotaType string, limit ...int) ([]*ZaiResetCycle, error) {
	query := `SELECT id, quota_type, cycle_start, cycle_end, next_reset, peak_value, total_delta
		FROM zai_reset_cycles WHERE quota_type = ? AND cycle_end IS NOT NULL ORDER BY cycle_start DESC`
	args := []interface{}{quotaType}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query zai cycles: %w", err)
	}
	defer rows.Close()

	var cycles []*ZaiResetCycle
	for rows.Next() {
		var cycle ZaiResetCycle
		var cycleStart, cycleEnd string
		var nextReset sql.NullString

		err := rows.Scan(
			&cycle.ID, &cycle.QuotaType, &cycleStart, &cycleEnd, &nextReset, &cycle.PeakValue, &cycle.TotalDelta,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan zai cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		endTime, _ := time.Parse(time.RFC3339Nano, cycleEnd)
		cycle.CycleEnd = &endTime
		if nextReset.Valid {
			resetTime, _ := time.Parse(time.RFC3339Nano, nextReset.String)
			cycle.NextReset = &resetTime
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// QueryZaiCyclesSince returns all Z.ai cycles (completed and active) for a quota type since a given time.
func (s *Store) QueryZaiCyclesSince(quotaType string, since time.Time) ([]*ZaiResetCycle, error) {
	rows, err := s.db.Query(
		`SELECT id, quota_type, cycle_start, cycle_end, next_reset, peak_value, total_delta
		FROM zai_reset_cycles WHERE quota_type = ? AND cycle_start >= ? ORDER BY cycle_start DESC`,
		quotaType, since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query zai cycles since: %w", err)
	}
	defer rows.Close()

	var cycles []*ZaiResetCycle
	for rows.Next() {
		var cycle ZaiResetCycle
		var cycleStart string
		var cycleEnd, nextReset sql.NullString

		err := rows.Scan(
			&cycle.ID, &cycle.QuotaType, &cycleStart, &cycleEnd, &nextReset, &cycle.PeakValue, &cycle.TotalDelta,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan zai cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		if cycleEnd.Valid {
			endTime, _ := time.Parse(time.RFC3339Nano, cycleEnd.String)
			cycle.CycleEnd = &endTime
		}
		if nextReset.Valid {
			resetTime, _ := time.Parse(time.RFC3339Nano, nextReset.String)
			cycle.NextReset = &resetTime
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}
