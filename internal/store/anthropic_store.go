package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
)

// AnthropicResetCycle represents an Anthropic quota reset cycle
type AnthropicResetCycle struct {
	ID              int64
	QuotaName       string
	CycleStart      time.Time
	CycleEnd        *time.Time
	ResetsAt        *time.Time
	PeakUtilization float64
	TotalDelta      float64
}

// InsertAnthropicSnapshot inserts an Anthropic snapshot with its quota values.
func (s *Store) InsertAnthropicSnapshot(snapshot *api.AnthropicSnapshot) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`INSERT INTO anthropic_snapshots (captured_at, raw_json, quota_count) VALUES (?, ?, ?)`,
		snapshot.CapturedAt.Format(time.RFC3339Nano),
		snapshot.RawJSON,
		len(snapshot.Quotas),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert anthropic snapshot: %w", err)
	}

	snapshotID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get snapshot ID: %w", err)
	}

	for _, q := range snapshot.Quotas {
		var resetsAt interface{}
		if q.ResetsAt != nil {
			resetsAt = q.ResetsAt.Format(time.RFC3339Nano)
		}
		_, err := tx.Exec(
			`INSERT INTO anthropic_quota_values (snapshot_id, quota_name, utilization, resets_at) VALUES (?, ?, ?, ?)`,
			snapshotID, q.Name, q.Utilization, resetsAt,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to insert quota value %s: %w", q.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit: %w", err)
	}

	return snapshotID, nil
}

// QueryLatestAnthropic returns the most recent Anthropic snapshot with quotas.
func (s *Store) QueryLatestAnthropic() (*api.AnthropicSnapshot, error) {
	var snapshot api.AnthropicSnapshot
	var capturedAt string

	err := s.db.QueryRow(
		`SELECT id, captured_at, quota_count FROM anthropic_snapshots ORDER BY captured_at DESC LIMIT 1`,
	).Scan(&snapshot.ID, &capturedAt, new(int))

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest anthropic: %w", err)
	}

	snapshot.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)

	// Load quota values
	rows, err := s.db.Query(
		`SELECT quota_name, utilization, resets_at FROM anthropic_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
		snapshot.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query quota values: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var q api.AnthropicQuota
		var resetsAt sql.NullString
		if err := rows.Scan(&q.Name, &q.Utilization, &resetsAt); err != nil {
			return nil, fmt.Errorf("failed to scan quota value: %w", err)
		}
		if resetsAt.Valid && resetsAt.String != "" {
			t, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
			q.ResetsAt = &t
		}
		snapshot.Quotas = append(snapshot.Quotas, q)
	}

	return &snapshot, rows.Err()
}

// QueryAnthropicRange returns Anthropic snapshots within a time range with optional limit.
func (s *Store) QueryAnthropicRange(start, end time.Time, limit ...int) ([]*api.AnthropicSnapshot, error) {
	query := `SELECT id, captured_at, quota_count FROM anthropic_snapshots
		WHERE captured_at BETWEEN ? AND ? ORDER BY captured_at ASC`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query anthropic range: %w", err)
	}
	defer rows.Close()

	var snapshots []*api.AnthropicSnapshot
	for rows.Next() {
		var snap api.AnthropicSnapshot
		var capturedAt string
		if err := rows.Scan(&snap.ID, &capturedAt, new(int)); err != nil {
			return nil, fmt.Errorf("failed to scan anthropic snapshot: %w", err)
		}
		snap.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		snapshots = append(snapshots, &snap)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load quota values for each snapshot
	for _, snap := range snapshots {
		qRows, err := s.db.Query(
			`SELECT quota_name, utilization, resets_at FROM anthropic_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
			snap.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to query quota values for snapshot %d: %w", snap.ID, err)
		}
		for qRows.Next() {
			var q api.AnthropicQuota
			var resetsAt sql.NullString
			if err := qRows.Scan(&q.Name, &q.Utilization, &resetsAt); err != nil {
				qRows.Close()
				return nil, fmt.Errorf("failed to scan quota value: %w", err)
			}
			if resetsAt.Valid && resetsAt.String != "" {
				t, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
				q.ResetsAt = &t
			}
			snap.Quotas = append(snap.Quotas, q)
		}
		qRows.Close()
	}

	return snapshots, nil
}

// CreateAnthropicCycle creates a new Anthropic reset cycle.
func (s *Store) CreateAnthropicCycle(quotaName string, cycleStart time.Time, resetsAt *time.Time) (int64, error) {
	var resetsAtVal interface{}
	if resetsAt != nil {
		resetsAtVal = resetsAt.Format(time.RFC3339Nano)
	}

	result, err := s.db.Exec(
		`INSERT INTO anthropic_reset_cycles (quota_name, cycle_start, resets_at) VALUES (?, ?, ?)`,
		quotaName, cycleStart.Format(time.RFC3339Nano), resetsAtVal,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create anthropic cycle: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get cycle ID: %w", err)
	}
	return id, nil
}

// CloseAnthropicCycle closes an Anthropic reset cycle with final stats.
func (s *Store) CloseAnthropicCycle(quotaName string, cycleEnd time.Time, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE anthropic_reset_cycles SET cycle_end = ?, peak_utilization = ?, total_delta = ?
		WHERE quota_name = ? AND cycle_end IS NULL`,
		cycleEnd.Format(time.RFC3339Nano), peak, delta, quotaName,
	)
	if err != nil {
		return fmt.Errorf("failed to close anthropic cycle: %w", err)
	}
	return nil
}

// UpdateAnthropicCycle updates the peak and delta for an active Anthropic cycle.
func (s *Store) UpdateAnthropicCycle(quotaName string, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE anthropic_reset_cycles SET peak_utilization = ?, total_delta = ?
		WHERE quota_name = ? AND cycle_end IS NULL`,
		peak, delta, quotaName,
	)
	if err != nil {
		return fmt.Errorf("failed to update anthropic cycle: %w", err)
	}
	return nil
}

// QueryActiveAnthropicCycle returns the active cycle for an Anthropic quota.
func (s *Store) QueryActiveAnthropicCycle(quotaName string) (*AnthropicResetCycle, error) {
	var cycle AnthropicResetCycle
	var cycleStart string
	var cycleEnd, resetsAt sql.NullString

	err := s.db.QueryRow(
		`SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM anthropic_reset_cycles WHERE quota_name = ? AND cycle_end IS NULL`,
		quotaName,
	).Scan(
		&cycle.ID, &cycle.QuotaName, &cycleStart, &cycleEnd, &resetsAt,
		&cycle.PeakUtilization, &cycle.TotalDelta,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active anthropic cycle: %w", err)
	}

	cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
	if cycleEnd.Valid {
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd.String)
		cycle.CycleEnd = &t
	}
	if resetsAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
		cycle.ResetsAt = &t
	}

	return &cycle, nil
}

// QueryAnthropicCycleHistory returns completed cycles for an Anthropic quota with optional limit.
func (s *Store) QueryAnthropicCycleHistory(quotaName string, limit ...int) ([]*AnthropicResetCycle, error) {
	query := `SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM anthropic_reset_cycles WHERE quota_name = ? AND cycle_end IS NOT NULL ORDER BY cycle_start DESC`
	args := []interface{}{quotaName}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query anthropic cycles: %w", err)
	}
	defer rows.Close()

	var cycles []*AnthropicResetCycle
	for rows.Next() {
		var cycle AnthropicResetCycle
		var cycleStart, cycleEnd string
		var resetsAt sql.NullString

		if err := rows.Scan(&cycle.ID, &cycle.QuotaName, &cycleStart, &cycleEnd, &resetsAt,
			&cycle.PeakUtilization, &cycle.TotalDelta); err != nil {
			return nil, fmt.Errorf("failed to scan anthropic cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd)
		cycle.CycleEnd = &t
		if resetsAt.Valid {
			rt, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
			cycle.ResetsAt = &rt
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// QueryAnthropicCyclesSince returns completed cycles for a quota since a given time.
func (s *Store) QueryAnthropicCyclesSince(quotaName string, since time.Time) ([]*AnthropicResetCycle, error) {
	rows, err := s.db.Query(
		`SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM anthropic_reset_cycles WHERE quota_name = ? AND cycle_end IS NOT NULL AND cycle_start >= ?
		ORDER BY cycle_start DESC`,
		quotaName, since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query anthropic cycles since: %w", err)
	}
	defer rows.Close()

	var cycles []*AnthropicResetCycle
	for rows.Next() {
		var cycle AnthropicResetCycle
		var cycleStart, cycleEnd string
		var resetsAt sql.NullString

		if err := rows.Scan(&cycle.ID, &cycle.QuotaName, &cycleStart, &cycleEnd, &resetsAt,
			&cycle.PeakUtilization, &cycle.TotalDelta); err != nil {
			return nil, fmt.Errorf("failed to scan anthropic cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd)
		cycle.CycleEnd = &t
		if resetsAt.Valid {
			rt, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
			cycle.ResetsAt = &rt
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// UtilizationPoint is a lightweight time+utilization pair for rate computation.
type UtilizationPoint struct {
	CapturedAt  time.Time
	Utilization float64
}

// QueryAnthropicUtilizationSeries returns per-quota utilization points since a given time.
// This is lighter than QueryAnthropicRange as it avoids loading all quotas per snapshot.
func (s *Store) QueryAnthropicUtilizationSeries(quotaName string, since time.Time) ([]UtilizationPoint, error) {
	rows, err := s.db.Query(
		`SELECT s.captured_at, qv.utilization
		FROM anthropic_quota_values qv
		JOIN anthropic_snapshots s ON s.id = qv.snapshot_id
		WHERE qv.quota_name = ? AND s.captured_at >= ?
		ORDER BY s.captured_at ASC`,
		quotaName, since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query utilization series: %w", err)
	}
	defer rows.Close()

	var points []UtilizationPoint
	for rows.Next() {
		var capturedAt string
		var util float64
		if err := rows.Scan(&capturedAt, &util); err != nil {
			return nil, fmt.Errorf("failed to scan utilization point: %w", err)
		}
		t, _ := time.Parse(time.RFC3339Nano, capturedAt)
		points = append(points, UtilizationPoint{CapturedAt: t, Utilization: util})
	}

	return points, rows.Err()
}

// QueryAnthropicCycleOverview returns Anthropic cycles for a given quota
// with cross-quota snapshot data at the peak moment of each cycle.
// Includes the currently active cycle (if any) at the top.
func (s *Store) QueryAnthropicCycleOverview(groupBy string, limit int) ([]CycleOverviewRow, error) {
	if limit <= 0 {
		limit = 50
	}

	// Get active cycle first (if any)
	var cycles []*AnthropicResetCycle
	activeCycle, err := s.QueryActiveAnthropicCycle(groupBy)
	if err != nil {
		return nil, fmt.Errorf("store.QueryAnthropicCycleOverview: active: %w", err)
	}
	if activeCycle != nil {
		cycles = append(cycles, activeCycle)
		limit-- // Reduce limit for completed cycles
	}

	// Get completed cycles
	completedCycles, err := s.QueryAnthropicCycleHistory(groupBy, limit)
	if err != nil {
		return nil, fmt.Errorf("store.QueryAnthropicCycleOverview: %w", err)
	}
	cycles = append(cycles, completedCycles...)

	var overviewRows []CycleOverviewRow
	for _, c := range cycles {
		row := CycleOverviewRow{
			CycleID:    c.ID,
			QuotaType:  c.QuotaName,
			CycleStart: c.CycleStart,
			CycleEnd:   c.CycleEnd, // nil for active cycles
			PeakValue:  c.PeakUtilization,
			TotalDelta: c.TotalDelta,
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

		// Find the snapshot where the primary quota peaked within this cycle
		var snapshotID int64
		var capturedAt string
		err := s.db.QueryRow(
			`SELECT s.id, s.captured_at FROM anthropic_snapshots s
			JOIN anthropic_quota_values qv ON qv.snapshot_id = s.id
			WHERE qv.quota_name = ? AND s.captured_at >= ? AND s.captured_at < ?
			ORDER BY qv.utilization DESC LIMIT 1`,
			groupBy,
			c.CycleStart.Format(time.RFC3339Nano),
			endBoundary.Format(time.RFC3339Nano),
		).Scan(&snapshotID, &capturedAt)

		if err == sql.ErrNoRows {
			overviewRows = append(overviewRows, row)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("store.QueryAnthropicCycleOverview: peak snapshot: %w", err)
		}

		row.PeakTime, _ = time.Parse(time.RFC3339Nano, capturedAt)

		// Load all quota values from that snapshot
		qRows, err := s.db.Query(
			`SELECT quota_name, utilization FROM anthropic_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
			snapshotID,
		)
		if err != nil {
			return nil, fmt.Errorf("store.QueryAnthropicCycleOverview: quota values: %w", err)
		}
		for qRows.Next() {
			var entry CrossQuotaEntry
			if err := qRows.Scan(&entry.Name, &entry.Percent); err != nil {
				qRows.Close()
				return nil, fmt.Errorf("store.QueryAnthropicCycleOverview: scan quota: %w", err)
			}
			entry.Value = entry.Percent // utilization is already a percentage
			row.CrossQuotas = append(row.CrossQuotas, entry)
		}
		qRows.Close()

		overviewRows = append(overviewRows, row)
	}

	return overviewRows, nil
}

// QueryAllAnthropicQuotaNames returns all distinct quota names from Anthropic reset cycles.
func (s *Store) QueryAllAnthropicQuotaNames() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT quota_name FROM anthropic_reset_cycles ORDER BY quota_name`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query anthropic quota names: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan quota name: %w", err)
		}
		names = append(names, name)
	}

	return names, rows.Err()
}
