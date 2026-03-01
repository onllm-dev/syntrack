package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
)

// CopilotResetCycle represents a Copilot quota reset cycle.
type CopilotResetCycle struct {
	ID         int64
	QuotaName  string
	CycleStart time.Time
	CycleEnd   *time.Time
	ResetDate  *time.Time
	PeakUsed   int
	TotalDelta int
}

// CopilotUsagePoint is a lightweight time+remaining pair for rate/series computation.
type CopilotUsagePoint struct {
	CapturedAt  time.Time
	Entitlement int
	Remaining   int
	Unlimited   bool
}

// InsertCopilotSnapshot inserts a Copilot snapshot with its quota values.
func (s *Store) InsertCopilotSnapshot(snapshot *api.CopilotSnapshot) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var resetDateVal interface{}
	if snapshot.ResetDate != nil {
		resetDateVal = snapshot.ResetDate.Format(time.RFC3339Nano)
	}

	result, err := tx.Exec(
		`INSERT INTO copilot_snapshots (captured_at, copilot_plan, reset_date, raw_json, quota_count) VALUES (?, ?, ?, ?, ?)`,
		snapshot.CapturedAt.Format(time.RFC3339Nano),
		snapshot.CopilotPlan,
		resetDateVal,
		snapshot.RawJSON,
		len(snapshot.Quotas),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert copilot snapshot: %w", err)
	}

	snapshotID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get snapshot ID: %w", err)
	}

	for _, q := range snapshot.Quotas {
		unlimited := 0
		if q.Unlimited {
			unlimited = 1
		}
		_, err := tx.Exec(
			`INSERT INTO copilot_quota_values (snapshot_id, quota_name, entitlement, remaining, percent_remaining, unlimited, overage_count) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			snapshotID, q.Name, q.Entitlement, q.Remaining, q.PercentRemaining, unlimited, q.OverageCount,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to insert copilot quota value %s: %w", q.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit: %w", err)
	}

	return snapshotID, nil
}

// QueryLatestCopilot returns the most recent Copilot snapshot with quotas.
func (s *Store) QueryLatestCopilot() (*api.CopilotSnapshot, error) {
	var snapshot api.CopilotSnapshot
	var capturedAt string
	var copilotPlan sql.NullString
	var resetDate sql.NullString

	err := s.db.QueryRow(
		`SELECT id, captured_at, copilot_plan, reset_date, quota_count FROM copilot_snapshots ORDER BY captured_at DESC LIMIT 1`,
	).Scan(&snapshot.ID, &capturedAt, &copilotPlan, &resetDate, new(int))

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest copilot: %w", err)
	}

	snapshot.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
	if copilotPlan.Valid {
		snapshot.CopilotPlan = copilotPlan.String
	}
	if resetDate.Valid && resetDate.String != "" {
		t, _ := time.Parse(time.RFC3339Nano, resetDate.String)
		snapshot.ResetDate = &t
	}

	// Load quota values
	rows, err := s.db.Query(
		`SELECT quota_name, entitlement, remaining, percent_remaining, unlimited, overage_count
		FROM copilot_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
		snapshot.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query copilot quota values: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var q api.CopilotQuota
		var unlimited int
		if err := rows.Scan(&q.Name, &q.Entitlement, &q.Remaining, &q.PercentRemaining, &unlimited, &q.OverageCount); err != nil {
			return nil, fmt.Errorf("failed to scan copilot quota value: %w", err)
		}
		q.Unlimited = unlimited == 1
		snapshot.Quotas = append(snapshot.Quotas, q)
	}

	return &snapshot, rows.Err()
}

// QueryCopilotRange returns Copilot snapshots within a time range.
func (s *Store) QueryCopilotRange(start, end time.Time, limit ...int) ([]*api.CopilotSnapshot, error) {
	query := `SELECT id, captured_at, copilot_plan, reset_date, quota_count FROM copilot_snapshots
		WHERE captured_at BETWEEN ? AND ? ORDER BY captured_at ASC`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query = `SELECT id, captured_at, copilot_plan, reset_date, quota_count
			FROM (
				SELECT id, captured_at, copilot_plan, reset_date, quota_count
				FROM copilot_snapshots
				WHERE captured_at BETWEEN ? AND ?
				ORDER BY captured_at DESC
				LIMIT ?
			) recent
			ORDER BY captured_at ASC`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query copilot range: %w", err)
	}
	defer rows.Close()

	var snapshots []*api.CopilotSnapshot
	for rows.Next() {
		var snap api.CopilotSnapshot
		var capturedAt string
		var copilotPlan, resetDate sql.NullString
		if err := rows.Scan(&snap.ID, &capturedAt, &copilotPlan, &resetDate, new(int)); err != nil {
			return nil, fmt.Errorf("failed to scan copilot snapshot: %w", err)
		}
		snap.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		if copilotPlan.Valid {
			snap.CopilotPlan = copilotPlan.String
		}
		if resetDate.Valid && resetDate.String != "" {
			t, _ := time.Parse(time.RFC3339Nano, resetDate.String)
			snap.ResetDate = &t
		}
		snapshots = append(snapshots, &snap)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load quota values for each snapshot
	for _, snap := range snapshots {
		qRows, err := s.db.Query(
			`SELECT quota_name, entitlement, remaining, percent_remaining, unlimited, overage_count
			FROM copilot_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
			snap.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to query copilot quota values for snapshot %d: %w", snap.ID, err)
		}
		for qRows.Next() {
			var q api.CopilotQuota
			var unlimited int
			if err := qRows.Scan(&q.Name, &q.Entitlement, &q.Remaining, &q.PercentRemaining, &unlimited, &q.OverageCount); err != nil {
				qRows.Close()
				return nil, fmt.Errorf("failed to scan copilot quota value: %w", err)
			}
			q.Unlimited = unlimited == 1
			snap.Quotas = append(snap.Quotas, q)
		}
		qRows.Close()
	}

	return snapshots, nil
}

// CreateCopilotCycle creates a new Copilot reset cycle.
func (s *Store) CreateCopilotCycle(quotaName string, cycleStart time.Time, resetDate *time.Time) (int64, error) {
	var resetDateVal interface{}
	if resetDate != nil {
		resetDateVal = resetDate.Format(time.RFC3339Nano)
	}

	result, err := s.db.Exec(
		`INSERT INTO copilot_reset_cycles (quota_name, cycle_start, reset_date) VALUES (?, ?, ?)`,
		quotaName, cycleStart.Format(time.RFC3339Nano), resetDateVal,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create copilot cycle: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get cycle ID: %w", err)
	}
	return id, nil
}

// CloseCopilotCycle closes a Copilot reset cycle with final stats.
func (s *Store) CloseCopilotCycle(quotaName string, cycleEnd time.Time, peakUsed, totalDelta int) error {
	_, err := s.db.Exec(
		`UPDATE copilot_reset_cycles SET cycle_end = ?, peak_used = ?, total_delta = ?
		WHERE quota_name = ? AND cycle_end IS NULL`,
		cycleEnd.Format(time.RFC3339Nano), peakUsed, totalDelta, quotaName,
	)
	if err != nil {
		return fmt.Errorf("failed to close copilot cycle: %w", err)
	}
	return nil
}

// UpdateCopilotCycle updates the peak and delta for an active Copilot cycle.
func (s *Store) UpdateCopilotCycle(quotaName string, peakUsed, totalDelta int) error {
	_, err := s.db.Exec(
		`UPDATE copilot_reset_cycles SET peak_used = ?, total_delta = ?
		WHERE quota_name = ? AND cycle_end IS NULL`,
		peakUsed, totalDelta, quotaName,
	)
	if err != nil {
		return fmt.Errorf("failed to update copilot cycle: %w", err)
	}
	return nil
}

// QueryActiveCopilotCycle returns the active cycle for a Copilot quota.
func (s *Store) QueryActiveCopilotCycle(quotaName string) (*CopilotResetCycle, error) {
	var cycle CopilotResetCycle
	var cycleStart string
	var cycleEnd, resetDate sql.NullString

	err := s.db.QueryRow(
		`SELECT id, quota_name, cycle_start, cycle_end, reset_date, peak_used, total_delta
		FROM copilot_reset_cycles WHERE quota_name = ? AND cycle_end IS NULL`,
		quotaName,
	).Scan(&cycle.ID, &cycle.QuotaName, &cycleStart, &cycleEnd, &resetDate, &cycle.PeakUsed, &cycle.TotalDelta)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active copilot cycle: %w", err)
	}

	cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
	if cycleEnd.Valid {
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd.String)
		cycle.CycleEnd = &t
	}
	if resetDate.Valid {
		t, _ := time.Parse(time.RFC3339Nano, resetDate.String)
		cycle.ResetDate = &t
	}

	return &cycle, nil
}

// QueryCopilotCycleHistory returns completed cycles for a Copilot quota with optional limit.
func (s *Store) QueryCopilotCycleHistory(quotaName string, limit ...int) ([]*CopilotResetCycle, error) {
	query := `SELECT id, quota_name, cycle_start, cycle_end, reset_date, peak_used, total_delta
		FROM copilot_reset_cycles WHERE quota_name = ? AND cycle_end IS NOT NULL ORDER BY cycle_start DESC`
	args := []interface{}{quotaName}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query copilot cycles: %w", err)
	}
	defer rows.Close()

	var cycles []*CopilotResetCycle
	for rows.Next() {
		var cycle CopilotResetCycle
		var cycleStart, cycleEnd string
		var resetDate sql.NullString

		if err := rows.Scan(&cycle.ID, &cycle.QuotaName, &cycleStart, &cycleEnd, &resetDate,
			&cycle.PeakUsed, &cycle.TotalDelta); err != nil {
			return nil, fmt.Errorf("failed to scan copilot cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd)
		cycle.CycleEnd = &t
		if resetDate.Valid {
			rt, _ := time.Parse(time.RFC3339Nano, resetDate.String)
			cycle.ResetDate = &rt
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// QueryCopilotCyclesSince returns completed cycles for a quota since a given time.
func (s *Store) QueryCopilotCyclesSince(quotaName string, since time.Time) ([]*CopilotResetCycle, error) {
	rows, err := s.db.Query(
		`SELECT id, quota_name, cycle_start, cycle_end, reset_date, peak_used, total_delta
		FROM copilot_reset_cycles WHERE quota_name = ? AND cycle_end IS NOT NULL AND cycle_start >= ?
		ORDER BY cycle_start DESC`,
		quotaName, since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query copilot cycles since: %w", err)
	}
	defer rows.Close()

	var cycles []*CopilotResetCycle
	for rows.Next() {
		var cycle CopilotResetCycle
		var cycleStart, cycleEnd string
		var resetDate sql.NullString

		if err := rows.Scan(&cycle.ID, &cycle.QuotaName, &cycleStart, &cycleEnd, &resetDate,
			&cycle.PeakUsed, &cycle.TotalDelta); err != nil {
			return nil, fmt.Errorf("failed to scan copilot cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd)
		cycle.CycleEnd = &t
		if resetDate.Valid {
			rt, _ := time.Parse(time.RFC3339Nano, resetDate.String)
			cycle.ResetDate = &rt
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// QueryCopilotUsageSeries returns per-quota usage points since a given time.
func (s *Store) QueryCopilotUsageSeries(quotaName string, since time.Time) ([]CopilotUsagePoint, error) {
	rows, err := s.db.Query(
		`SELECT s.captured_at, qv.entitlement, qv.remaining, qv.unlimited
		FROM copilot_quota_values qv
		JOIN copilot_snapshots s ON s.id = qv.snapshot_id
		WHERE qv.quota_name = ? AND s.captured_at >= ?
		ORDER BY s.captured_at ASC`,
		quotaName, since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query copilot usage series: %w", err)
	}
	defer rows.Close()

	var points []CopilotUsagePoint
	for rows.Next() {
		var capturedAt string
		var unlimited int
		var pt CopilotUsagePoint
		if err := rows.Scan(&capturedAt, &pt.Entitlement, &pt.Remaining, &unlimited); err != nil {
			return nil, fmt.Errorf("failed to scan copilot usage point: %w", err)
		}
		pt.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		pt.Unlimited = unlimited == 1
		points = append(points, pt)
	}

	return points, rows.Err()
}

// QueryCopilotCycleOverview returns Copilot cycles for a given quota
// with cross-quota snapshot data at the peak moment of each cycle.
func (s *Store) QueryCopilotCycleOverview(groupBy string, limit int) ([]CycleOverviewRow, error) {
	if limit <= 0 {
		limit = 50
	}

	// Get active cycle first (if any)
	var cycles []*CopilotResetCycle
	activeCycle, err := s.QueryActiveCopilotCycle(groupBy)
	if err != nil {
		return nil, fmt.Errorf("store.QueryCopilotCycleOverview: active: %w", err)
	}
	if activeCycle != nil {
		cycles = append(cycles, activeCycle)
		limit--
	}

	// Get completed cycles
	completedCycles, err := s.QueryCopilotCycleHistory(groupBy, limit)
	if err != nil {
		return nil, fmt.Errorf("store.QueryCopilotCycleOverview: %w", err)
	}
	cycles = append(cycles, completedCycles...)

	var overviewRows []CycleOverviewRow
	for _, c := range cycles {
		row := CycleOverviewRow{
			CycleID:    c.ID,
			QuotaType:  c.QuotaName,
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

		// Find the snapshot where the primary quota had highest usage within this cycle
		var snapshotID int64
		var capturedAt string
		err := s.db.QueryRow(
			`SELECT s.id, s.captured_at FROM copilot_snapshots s
			JOIN copilot_quota_values qv ON qv.snapshot_id = s.id
			WHERE qv.quota_name = ? AND s.captured_at >= ? AND s.captured_at < ?
			ORDER BY (qv.entitlement - qv.remaining) DESC LIMIT 1`,
			groupBy,
			c.CycleStart.Format(time.RFC3339Nano),
			endBoundary.Format(time.RFC3339Nano),
		).Scan(&snapshotID, &capturedAt)

		if err == sql.ErrNoRows {
			overviewRows = append(overviewRows, row)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("store.QueryCopilotCycleOverview: peak snapshot: %w", err)
		}

		row.PeakTime, _ = time.Parse(time.RFC3339Nano, capturedAt)

		// Load all quota values from peak snapshot
		qRows, err := s.db.Query(
			`SELECT quota_name, entitlement, remaining, unlimited FROM copilot_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
			snapshotID,
		)
		if err != nil {
			return nil, fmt.Errorf("store.QueryCopilotCycleOverview: quota values: %w", err)
		}
		for qRows.Next() {
			var entry CrossQuotaEntry
			var entitlement, remaining, unlimited int
			if err := qRows.Scan(&entry.Name, &entitlement, &remaining, &unlimited); err != nil {
				qRows.Close()
				return nil, fmt.Errorf("store.QueryCopilotCycleOverview: scan quota: %w", err)
			}
			used := entitlement - remaining
			entry.Value = float64(used)
			entry.Limit = float64(entitlement)
			if entitlement > 0 {
				entry.Percent = float64(used) / float64(entitlement) * 100
			}
			row.CrossQuotas = append(row.CrossQuotas, entry)
		}
		qRows.Close()

		overviewRows = append(overviewRows, row)
	}

	return overviewRows, nil
}

// QueryAllCopilotQuotaNames returns all distinct quota names from Copilot quota values.
func (s *Store) QueryAllCopilotQuotaNames() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT quota_name FROM copilot_quota_values ORDER BY quota_name`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query copilot quota names: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan copilot quota name: %w", err)
		}
		names = append(names, name)
	}

	return names, rows.Err()
}
