package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
)

// MiniMaxResetCycle represents a MiniMax model reset cycle.
type MiniMaxResetCycle struct {
	ID         int64
	ModelName  string
	CycleStart time.Time
	CycleEnd   *time.Time
	ResetAt    *time.Time
	PeakUsed   int
	TotalDelta int
}

// MiniMaxUsagePoint is a lightweight time+used pair for usage series.
type MiniMaxUsagePoint struct {
	CapturedAt time.Time
	Total      int
	Remain     int
	Used       int
}

// InsertMiniMaxSnapshot inserts a MiniMax snapshot with model values.
func (s *Store) InsertMiniMaxSnapshot(snapshot *api.MiniMaxSnapshot) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`INSERT INTO minimax_snapshots (captured_at, raw_json, model_count) VALUES (?, ?, ?)`,
		snapshot.CapturedAt.Format(time.RFC3339Nano),
		snapshot.RawJSON,
		len(snapshot.Models),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert minimax snapshot: %w", err)
	}

	snapshotID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get snapshot ID: %w", err)
	}

	for _, m := range snapshot.Models {
		var resetAtVal interface{}
		if m.ResetAt != nil {
			resetAtVal = m.ResetAt.Format(time.RFC3339Nano)
		}
		_, err := tx.Exec(
			`INSERT INTO minimax_model_values (snapshot_id, model_name, total, remain, used, used_percent, reset_at, time_until_reset_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			snapshotID, m.ModelName, m.Total, m.Remain, m.Used, m.UsedPercent, resetAtVal, m.TimeUntilReset.Milliseconds(),
		)
		if err != nil {
			return 0, fmt.Errorf("failed to insert minimax model value %s: %w", m.ModelName, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit: %w", err)
	}

	return snapshotID, nil
}

// QueryLatestMiniMax returns the most recent MiniMax snapshot with model values.
func (s *Store) QueryLatestMiniMax() (*api.MiniMaxSnapshot, error) {
	var snapshot api.MiniMaxSnapshot
	var capturedAt string

	err := s.db.QueryRow(
		`SELECT id, captured_at FROM minimax_snapshots ORDER BY captured_at DESC LIMIT 1`,
	).Scan(&snapshot.ID, &capturedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest minimax: %w", err)
	}

	snapshot.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)

	rows, err := s.db.Query(
		`SELECT model_name, total, remain, used, used_percent, reset_at, time_until_reset_ms
		FROM minimax_model_values WHERE snapshot_id = ? ORDER BY model_name`,
		snapshot.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query minimax model values: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var m api.MiniMaxModelQuota
		var resetAt sql.NullString
		var resetMs int64
		if err := rows.Scan(&m.ModelName, &m.Total, &m.Remain, &m.Used, &m.UsedPercent, &resetAt, &resetMs); err != nil {
			return nil, fmt.Errorf("failed to scan minimax model value: %w", err)
		}
		if resetAt.Valid && resetAt.String != "" {
			t, _ := time.Parse(time.RFC3339Nano, resetAt.String)
			m.ResetAt = &t
		}
		m.TimeUntilReset = time.Duration(resetMs) * time.Millisecond
		snapshot.Models = append(snapshot.Models, m)
	}

	return &snapshot, rows.Err()
}

// QueryMiniMaxRange returns MiniMax snapshots within a time range.
func (s *Store) QueryMiniMaxRange(start, end time.Time, limit ...int) ([]*api.MiniMaxSnapshot, error) {
	query := `SELECT id, captured_at FROM minimax_snapshots
		WHERE captured_at BETWEEN ? AND ? ORDER BY captured_at ASC`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query minimax range: %w", err)
	}
	defer rows.Close()

	var snapshots []*api.MiniMaxSnapshot
	for rows.Next() {
		var snap api.MiniMaxSnapshot
		var capturedAt string
		if err := rows.Scan(&snap.ID, &capturedAt); err != nil {
			return nil, fmt.Errorf("failed to scan minimax snapshot: %w", err)
		}
		snap.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		snapshots = append(snapshots, &snap)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, snap := range snapshots {
		mRows, err := s.db.Query(
			`SELECT model_name, total, remain, used, used_percent, reset_at, time_until_reset_ms
			FROM minimax_model_values WHERE snapshot_id = ? ORDER BY model_name`,
			snap.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to query minimax model values for snapshot %d: %w", snap.ID, err)
		}
		for mRows.Next() {
			var m api.MiniMaxModelQuota
			var resetAt sql.NullString
			var resetMs int64
			if err := mRows.Scan(&m.ModelName, &m.Total, &m.Remain, &m.Used, &m.UsedPercent, &resetAt, &resetMs); err != nil {
				mRows.Close()
				return nil, fmt.Errorf("failed to scan minimax model value: %w", err)
			}
			if resetAt.Valid && resetAt.String != "" {
				t, _ := time.Parse(time.RFC3339Nano, resetAt.String)
				m.ResetAt = &t
			}
			m.TimeUntilReset = time.Duration(resetMs) * time.Millisecond
			snap.Models = append(snap.Models, m)
		}
		mRows.Close()
	}

	return snapshots, nil
}

// CreateMiniMaxCycle creates a new MiniMax reset cycle for a model.
func (s *Store) CreateMiniMaxCycle(modelName string, cycleStart time.Time, resetAt *time.Time) (int64, error) {
	var resetAtVal interface{}
	if resetAt != nil {
		resetAtVal = resetAt.Format(time.RFC3339Nano)
	}

	result, err := s.db.Exec(
		`INSERT INTO minimax_reset_cycles (model_name, cycle_start, reset_at) VALUES (?, ?, ?)`,
		modelName, cycleStart.Format(time.RFC3339Nano), resetAtVal,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create minimax cycle: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get cycle ID: %w", err)
	}
	return id, nil
}

// CloseMiniMaxCycle closes a MiniMax reset cycle with final stats.
func (s *Store) CloseMiniMaxCycle(modelName string, cycleEnd time.Time, peakUsed, totalDelta int) error {
	_, err := s.db.Exec(
		`UPDATE minimax_reset_cycles SET cycle_end = ?, peak_used = ?, total_delta = ?
		WHERE model_name = ? AND cycle_end IS NULL`,
		cycleEnd.Format(time.RFC3339Nano), peakUsed, totalDelta, modelName,
	)
	if err != nil {
		return fmt.Errorf("failed to close minimax cycle: %w", err)
	}
	return nil
}

// UpdateMiniMaxCycle updates the peak and delta for an active MiniMax cycle.
func (s *Store) UpdateMiniMaxCycle(modelName string, peakUsed, totalDelta int) error {
	_, err := s.db.Exec(
		`UPDATE minimax_reset_cycles SET peak_used = ?, total_delta = ?
		WHERE model_name = ? AND cycle_end IS NULL`,
		peakUsed, totalDelta, modelName,
	)
	if err != nil {
		return fmt.Errorf("failed to update minimax cycle: %w", err)
	}
	return nil
}

// QueryActiveMiniMaxCycle returns the active cycle for a MiniMax model.
func (s *Store) QueryActiveMiniMaxCycle(modelName string) (*MiniMaxResetCycle, error) {
	var cycle MiniMaxResetCycle
	var cycleStart string
	var cycleEnd, resetAt sql.NullString

	err := s.db.QueryRow(
		`SELECT id, model_name, cycle_start, cycle_end, reset_at, peak_used, total_delta
		FROM minimax_reset_cycles WHERE model_name = ? AND cycle_end IS NULL`,
		modelName,
	).Scan(&cycle.ID, &cycle.ModelName, &cycleStart, &cycleEnd, &resetAt, &cycle.PeakUsed, &cycle.TotalDelta)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active minimax cycle: %w", err)
	}

	cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
	if cycleEnd.Valid {
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd.String)
		cycle.CycleEnd = &t
	}
	if resetAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, resetAt.String)
		cycle.ResetAt = &t
	}

	return &cycle, nil
}

// QueryMiniMaxCycleHistory returns completed cycles for a MiniMax model with optional limit.
func (s *Store) QueryMiniMaxCycleHistory(modelName string, limit ...int) ([]*MiniMaxResetCycle, error) {
	query := `SELECT id, model_name, cycle_start, cycle_end, reset_at, peak_used, total_delta
		FROM minimax_reset_cycles WHERE model_name = ? AND cycle_end IS NOT NULL ORDER BY cycle_start DESC`
	args := []interface{}{modelName}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query minimax cycles: %w", err)
	}
	defer rows.Close()

	var cycles []*MiniMaxResetCycle
	for rows.Next() {
		var cycle MiniMaxResetCycle
		var cycleStart, cycleEnd string
		var resetAt sql.NullString

		if err := rows.Scan(&cycle.ID, &cycle.ModelName, &cycleStart, &cycleEnd, &resetAt, &cycle.PeakUsed, &cycle.TotalDelta); err != nil {
			return nil, fmt.Errorf("failed to scan minimax cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd)
		cycle.CycleEnd = &t
		if resetAt.Valid {
			rt, _ := time.Parse(time.RFC3339Nano, resetAt.String)
			cycle.ResetAt = &rt
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// QueryMiniMaxUsageSeries returns per-model usage points since a given time.
func (s *Store) QueryMiniMaxUsageSeries(modelName string, since time.Time) ([]MiniMaxUsagePoint, error) {
	rows, err := s.db.Query(
		`SELECT s.captured_at, mv.total, mv.remain, mv.used
		FROM minimax_model_values mv
		JOIN minimax_snapshots s ON s.id = mv.snapshot_id
		WHERE mv.model_name = ? AND s.captured_at >= ?
		ORDER BY s.captured_at ASC`,
		modelName, since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query minimax usage series: %w", err)
	}
	defer rows.Close()

	var points []MiniMaxUsagePoint
	for rows.Next() {
		var capturedAt string
		var pt MiniMaxUsagePoint
		if err := rows.Scan(&capturedAt, &pt.Total, &pt.Remain, &pt.Used); err != nil {
			return nil, fmt.Errorf("failed to scan minimax usage point: %w", err)
		}
		pt.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		points = append(points, pt)
	}

	return points, rows.Err()
}

// QueryMiniMaxCycleOverview returns MiniMax cycles for a given model
// with cross-model snapshot data at the peak moment of each cycle.
func (s *Store) QueryMiniMaxCycleOverview(groupBy string, limit int) ([]CycleOverviewRow, error) {
	if limit <= 0 {
		limit = 50
	}

	var cycles []*MiniMaxResetCycle
	activeCycle, err := s.QueryActiveMiniMaxCycle(groupBy)
	if err != nil {
		return nil, fmt.Errorf("store.QueryMiniMaxCycleOverview: active: %w", err)
	}
	if activeCycle != nil {
		cycles = append(cycles, activeCycle)
		limit--
	}

	if limit > 0 {
		completedCycles, err := s.QueryMiniMaxCycleHistory(groupBy, limit)
		if err != nil {
			return nil, fmt.Errorf("store.QueryMiniMaxCycleOverview: %w", err)
		}
		cycles = append(cycles, completedCycles...)
	}

	var overviewRows []CycleOverviewRow
	for _, c := range cycles {
		row := CycleOverviewRow{
			CycleID:    c.ID,
			QuotaType:  c.ModelName,
			CycleStart: c.CycleStart,
			CycleEnd:   c.CycleEnd,
			PeakValue:  float64(c.PeakUsed),
			TotalDelta: float64(c.TotalDelta),
		}

		var endBoundary time.Time
		if c.CycleEnd != nil {
			endBoundary = *c.CycleEnd
		} else {
			endBoundary = time.Now().Add(time.Minute)
		}

		var snapshotID int64
		var capturedAt string
		err := s.db.QueryRow(
			`SELECT s.id, s.captured_at FROM minimax_snapshots s
			JOIN minimax_model_values mv ON mv.snapshot_id = s.id
			WHERE mv.model_name = ? AND s.captured_at >= ? AND s.captured_at < ?
			ORDER BY mv.used DESC LIMIT 1`,
			groupBy,
			c.CycleStart.Format(time.RFC3339Nano),
			endBoundary.Format(time.RFC3339Nano),
		).Scan(&snapshotID, &capturedAt)

		if err == sql.ErrNoRows {
			overviewRows = append(overviewRows, row)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("store.QueryMiniMaxCycleOverview: peak snapshot: %w", err)
		}

		row.PeakTime, _ = time.Parse(time.RFC3339Nano, capturedAt)

		qRows, err := s.db.Query(
			`SELECT model_name, total, used FROM minimax_model_values WHERE snapshot_id = ? ORDER BY model_name`,
			snapshotID,
		)
		if err != nil {
			return nil, fmt.Errorf("store.QueryMiniMaxCycleOverview: model values: %w", err)
		}
		for qRows.Next() {
			var entry CrossQuotaEntry
			var total, used int
			if err := qRows.Scan(&entry.Name, &total, &used); err != nil {
				qRows.Close()
				return nil, fmt.Errorf("store.QueryMiniMaxCycleOverview: scan model: %w", err)
			}
			entry.Value = float64(used)
			entry.Limit = float64(total)
			if total > 0 {
				entry.Percent = float64(used) / float64(total) * 100
			}
			row.CrossQuotas = append(row.CrossQuotas, entry)
		}
		qRows.Close()

		overviewRows = append(overviewRows, row)
	}

	return overviewRows, nil
}

// QueryAllMiniMaxModelNames returns all distinct model names from MiniMax model values.
func (s *Store) QueryAllMiniMaxModelNames() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT model_name FROM minimax_model_values ORDER BY model_name`)
	if err != nil {
		return nil, fmt.Errorf("failed to query minimax model names: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan minimax model name: %w", err)
		}
		names = append(names, name)
	}

	return names, rows.Err()
}
