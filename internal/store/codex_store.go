package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
)

// CodexResetCycle represents a Codex quota reset cycle.
type CodexResetCycle struct {
	ID              int64
	QuotaName       string
	CycleStart      time.Time
	CycleEnd        *time.Time
	ResetsAt        *time.Time
	PeakUtilization float64
	TotalDelta      float64
}

func parseCodexTime(value string, field string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse %s %q: %w", field, value, err)
	}
	return parsed, nil
}

// InsertCodexSnapshot inserts a Codex snapshot with its quota values.
func (s *Store) InsertCodexSnapshot(snapshot *api.CodexSnapshot) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var creditsBalance interface{}
	if snapshot.CreditsBalance != nil {
		creditsBalance = *snapshot.CreditsBalance
	}

	result, err := tx.Exec(
		`INSERT INTO codex_snapshots (captured_at, plan_type, credits_balance, raw_json, quota_count) VALUES (?, ?, ?, ?, ?)`,
		snapshot.CapturedAt.Format(time.RFC3339Nano),
		snapshot.PlanType,
		creditsBalance,
		snapshot.RawJSON,
		len(snapshot.Quotas),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert codex snapshot: %w", err)
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
			`INSERT INTO codex_quota_values (snapshot_id, quota_name, utilization, resets_at, status) VALUES (?, ?, ?, ?, ?)`,
			snapshotID,
			q.Name,
			q.Utilization,
			resetsAt,
			q.Status,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to insert codex quota value %s: %w", q.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit: %w", err)
	}

	return snapshotID, nil
}

// QueryLatestCodex returns the most recent Codex snapshot with quotas.
func (s *Store) QueryLatestCodex() (*api.CodexSnapshot, error) {
	var snapshot api.CodexSnapshot
	var capturedAt string
	var planType sql.NullString
	var creditsBalance sql.NullFloat64

	err := s.db.QueryRow(
		`SELECT id, captured_at, plan_type, credits_balance, quota_count FROM codex_snapshots ORDER BY captured_at DESC LIMIT 1`,
	).Scan(&snapshot.ID, &capturedAt, &planType, &creditsBalance, new(int))

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest codex: %w", err)
	}

	parsedCapturedAt, err := parseCodexTime(capturedAt, "codex snapshot captured_at")
	if err != nil {
		return nil, err
	}
	snapshot.CapturedAt = parsedCapturedAt
	if planType.Valid {
		snapshot.PlanType = planType.String
	}
	if creditsBalance.Valid {
		snapshot.CreditsBalance = &creditsBalance.Float64
	}

	rows, err := s.db.Query(
		`SELECT quota_name, utilization, resets_at, status FROM codex_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
		snapshot.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query codex quota values: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var q api.CodexQuota
		var resetsAt sql.NullString
		var status sql.NullString
		if err := rows.Scan(&q.Name, &q.Utilization, &resetsAt, &status); err != nil {
			return nil, fmt.Errorf("failed to scan codex quota value: %w", err)
		}
		if resetsAt.Valid && resetsAt.String != "" {
			parsedResetsAt, err := parseCodexTime(resetsAt.String, "codex quota resets_at")
			if err != nil {
				return nil, err
			}
			q.ResetsAt = &parsedResetsAt
		}
		if status.Valid {
			q.Status = status.String
		}
		snapshot.Quotas = append(snapshot.Quotas, q)
	}

	return &snapshot, rows.Err()
}

// QueryCodexRange returns Codex snapshots within a time range.
func (s *Store) QueryCodexRange(start, end time.Time, limit ...int) ([]*api.CodexSnapshot, error) {
	query := `SELECT id, captured_at, plan_type, credits_balance, quota_count FROM codex_snapshots
		WHERE captured_at BETWEEN ? AND ? ORDER BY captured_at ASC`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query codex range: %w", err)
	}
	defer rows.Close()

	var snapshots []*api.CodexSnapshot
	for rows.Next() {
		var snap api.CodexSnapshot
		var capturedAt string
		var planType sql.NullString
		var creditsBalance sql.NullFloat64
		if err := rows.Scan(&snap.ID, &capturedAt, &planType, &creditsBalance, new(int)); err != nil {
			return nil, fmt.Errorf("failed to scan codex snapshot: %w", err)
		}
		parsedCapturedAt, err := parseCodexTime(capturedAt, "codex snapshot captured_at")
		if err != nil {
			return nil, err
		}
		snap.CapturedAt = parsedCapturedAt
		if planType.Valid {
			snap.PlanType = planType.String
		}
		if creditsBalance.Valid {
			snap.CreditsBalance = &creditsBalance.Float64
		}
		snapshots = append(snapshots, &snap)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, snap := range snapshots {
		qRows, err := s.db.Query(
			`SELECT quota_name, utilization, resets_at, status FROM codex_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
			snap.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to query codex quota values for snapshot %d: %w", snap.ID, err)
		}
		for qRows.Next() {
			var q api.CodexQuota
			var resetsAt sql.NullString
			var status sql.NullString
			if err := qRows.Scan(&q.Name, &q.Utilization, &resetsAt, &status); err != nil {
				qRows.Close()
				return nil, fmt.Errorf("failed to scan codex quota value: %w", err)
			}
			if resetsAt.Valid && resetsAt.String != "" {
				parsedResetsAt, err := parseCodexTime(resetsAt.String, "codex quota resets_at")
				if err != nil {
					qRows.Close()
					return nil, err
				}
				q.ResetsAt = &parsedResetsAt
			}
			if status.Valid {
				q.Status = status.String
			}
			snap.Quotas = append(snap.Quotas, q)
		}
		qRows.Close()
	}

	return snapshots, nil
}

// CreateCodexCycle creates a new Codex reset cycle.
func (s *Store) CreateCodexCycle(quotaName string, cycleStart time.Time, resetsAt *time.Time) (int64, error) {
	var resetsAtVal interface{}
	if resetsAt != nil {
		resetsAtVal = resetsAt.Format(time.RFC3339Nano)
	}

	result, err := s.db.Exec(
		`INSERT INTO codex_reset_cycles (quota_name, cycle_start, resets_at) VALUES (?, ?, ?)`,
		quotaName,
		cycleStart.Format(time.RFC3339Nano),
		resetsAtVal,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create codex cycle: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get cycle ID: %w", err)
	}
	return id, nil
}

// CloseCodexCycle closes a Codex reset cycle with final stats.
func (s *Store) CloseCodexCycle(quotaName string, cycleEnd time.Time, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE codex_reset_cycles SET cycle_end = ?, peak_utilization = ?, total_delta = ?
		WHERE quota_name = ? AND cycle_end IS NULL`,
		cycleEnd.Format(time.RFC3339Nano),
		peak,
		delta,
		quotaName,
	)
	if err != nil {
		return fmt.Errorf("failed to close codex cycle: %w", err)
	}
	return nil
}

// UpdateCodexCycle updates the peak and delta for an active Codex cycle.
func (s *Store) UpdateCodexCycle(quotaName string, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE codex_reset_cycles SET peak_utilization = ?, total_delta = ?
		WHERE quota_name = ? AND cycle_end IS NULL`,
		peak,
		delta,
		quotaName,
	)
	if err != nil {
		return fmt.Errorf("failed to update codex cycle: %w", err)
	}
	return nil
}

// UpdateCodexCycleResetsAt updates the reset timestamp for an active Codex cycle.
func (s *Store) UpdateCodexCycleResetsAt(quotaName string, resetsAt *time.Time) error {
	var resetsAtValue interface{}
	if resetsAt != nil {
		resetsAtValue = resetsAt.Format(time.RFC3339Nano)
	}

	_, err := s.db.Exec(
		`UPDATE codex_reset_cycles SET resets_at = ?
		WHERE quota_name = ? AND cycle_end IS NULL`,
		resetsAtValue,
		quotaName,
	)
	if err != nil {
		return fmt.Errorf("failed to update codex cycle resets_at: %w", err)
	}
	return nil
}

// QueryActiveCodexCycle returns the active cycle for a Codex quota.
func (s *Store) QueryActiveCodexCycle(quotaName string) (*CodexResetCycle, error) {
	var cycle CodexResetCycle
	var cycleStart string
	var cycleEnd, resetsAt sql.NullString

	err := s.db.QueryRow(
		`SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM codex_reset_cycles WHERE quota_name = ? AND cycle_end IS NULL`,
		quotaName,
	).Scan(
		&cycle.ID,
		&cycle.QuotaName,
		&cycleStart,
		&cycleEnd,
		&resetsAt,
		&cycle.PeakUtilization,
		&cycle.TotalDelta,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active codex cycle: %w", err)
	}

	parsedCycleStart, err := parseCodexTime(cycleStart, "codex cycle_start")
	if err != nil {
		return nil, err
	}
	cycle.CycleStart = parsedCycleStart
	if cycleEnd.Valid {
		parsedCycleEnd, err := parseCodexTime(cycleEnd.String, "codex cycle_end")
		if err != nil {
			return nil, err
		}
		cycle.CycleEnd = &parsedCycleEnd
	}
	if resetsAt.Valid {
		parsedResetsAt, err := parseCodexTime(resetsAt.String, "codex cycle resets_at")
		if err != nil {
			return nil, err
		}
		cycle.ResetsAt = &parsedResetsAt
	}

	return &cycle, nil
}

// QueryCodexCycleHistory returns completed cycles for a Codex quota with optional limit.
func (s *Store) QueryCodexCycleHistory(quotaName string, limit ...int) ([]*CodexResetCycle, error) {
	query := `SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM codex_reset_cycles WHERE quota_name = ? AND cycle_end IS NOT NULL ORDER BY cycle_start DESC`
	args := []interface{}{quotaName}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query codex cycles: %w", err)
	}
	defer rows.Close()

	var cycles []*CodexResetCycle
	for rows.Next() {
		var cycle CodexResetCycle
		var cycleStart, cycleEnd string
		var resetsAt sql.NullString

		if err := rows.Scan(
			&cycle.ID,
			&cycle.QuotaName,
			&cycleStart,
			&cycleEnd,
			&resetsAt,
			&cycle.PeakUtilization,
			&cycle.TotalDelta,
		); err != nil {
			return nil, fmt.Errorf("failed to scan codex cycle: %w", err)
		}

		parsedCycleStart, err := parseCodexTime(cycleStart, "codex cycle_start")
		if err != nil {
			return nil, err
		}
		cycle.CycleStart = parsedCycleStart

		parsedCycleEnd, err := parseCodexTime(cycleEnd, "codex cycle_end")
		if err != nil {
			return nil, err
		}
		cycle.CycleEnd = &parsedCycleEnd
		if resetsAt.Valid {
			parsedResetsAt, err := parseCodexTime(resetsAt.String, "codex cycle resets_at")
			if err != nil {
				return nil, err
			}
			cycle.ResetsAt = &parsedResetsAt
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// QueryCodexCyclesSince returns completed cycles for a quota since a given time.
func (s *Store) QueryCodexCyclesSince(quotaName string, since time.Time) ([]*CodexResetCycle, error) {
	rows, err := s.db.Query(
		`SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM codex_reset_cycles WHERE quota_name = ? AND cycle_end IS NOT NULL AND cycle_start >= ?
		ORDER BY cycle_start DESC`,
		quotaName,
		since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query codex cycles since: %w", err)
	}
	defer rows.Close()

	var cycles []*CodexResetCycle
	for rows.Next() {
		var cycle CodexResetCycle
		var cycleStart, cycleEnd string
		var resetsAt sql.NullString

		if err := rows.Scan(
			&cycle.ID,
			&cycle.QuotaName,
			&cycleStart,
			&cycleEnd,
			&resetsAt,
			&cycle.PeakUtilization,
			&cycle.TotalDelta,
		); err != nil {
			return nil, fmt.Errorf("failed to scan codex cycle: %w", err)
		}

		parsedCycleStart, err := parseCodexTime(cycleStart, "codex cycle_start")
		if err != nil {
			return nil, err
		}
		cycle.CycleStart = parsedCycleStart

		parsedCycleEnd, err := parseCodexTime(cycleEnd, "codex cycle_end")
		if err != nil {
			return nil, err
		}
		cycle.CycleEnd = &parsedCycleEnd
		if resetsAt.Valid {
			parsedResetsAt, err := parseCodexTime(resetsAt.String, "codex cycle resets_at")
			if err != nil {
				return nil, err
			}
			cycle.ResetsAt = &parsedResetsAt
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// QueryCodexUtilizationSeries returns per-quota utilization points since a given time.
func (s *Store) QueryCodexUtilizationSeries(quotaName string, since time.Time) ([]UtilizationPoint, error) {
	rows, err := s.db.Query(
		`SELECT s.captured_at, qv.utilization
		FROM codex_quota_values qv
		JOIN codex_snapshots s ON s.id = qv.snapshot_id
		WHERE qv.quota_name = ? AND s.captured_at >= ?
		ORDER BY s.captured_at ASC`,
		quotaName,
		since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query codex utilization series: %w", err)
	}
	defer rows.Close()

	var points []UtilizationPoint
	for rows.Next() {
		var capturedAt string
		var util float64
		if err := rows.Scan(&capturedAt, &util); err != nil {
			return nil, fmt.Errorf("failed to scan codex utilization point: %w", err)
		}
		parsedCapturedAt, err := parseCodexTime(capturedAt, "codex utilization captured_at")
		if err != nil {
			return nil, err
		}
		points = append(points, UtilizationPoint{CapturedAt: parsedCapturedAt, Utilization: util})
	}

	return points, rows.Err()
}

// QueryCodexCycleOverview returns Codex cycles for a given quota
// with cross-quota snapshot data at the peak moment of each cycle.
func (s *Store) QueryCodexCycleOverview(groupBy string, limit int) ([]CycleOverviewRow, error) {
	if limit <= 0 {
		limit = 50
	}

	var cycles []*CodexResetCycle
	activeCycle, err := s.QueryActiveCodexCycle(groupBy)
	if err != nil {
		return nil, fmt.Errorf("store.QueryCodexCycleOverview: active: %w", err)
	}
	if activeCycle != nil {
		cycles = append(cycles, activeCycle)
		limit--
	}

	completedCycles, err := s.QueryCodexCycleHistory(groupBy, limit)
	if err != nil {
		return nil, fmt.Errorf("store.QueryCodexCycleOverview: %w", err)
	}
	cycles = append(cycles, completedCycles...)

	var overviewRows []CycleOverviewRow
	for _, c := range cycles {
		row := CycleOverviewRow{
			CycleID:    c.ID,
			QuotaType:  c.QuotaName,
			CycleStart: c.CycleStart,
			CycleEnd:   c.CycleEnd,
			PeakValue:  c.PeakUtilization,
			TotalDelta: c.TotalDelta,
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
			`SELECT s.id, s.captured_at FROM codex_snapshots s
			JOIN codex_quota_values qv ON qv.snapshot_id = s.id
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
			return nil, fmt.Errorf("store.QueryCodexCycleOverview: peak snapshot: %w", err)
		}

		parsedPeakTime, err := parseCodexTime(capturedAt, "codex peak captured_at")
		if err != nil {
			return nil, fmt.Errorf("store.QueryCodexCycleOverview: peak time: %w", err)
		}
		row.PeakTime = parsedPeakTime

		startValues := make(map[string]float64)
		var firstSnapshotID int64
		err = s.db.QueryRow(
			`SELECT id FROM codex_snapshots
			WHERE captured_at >= ? AND captured_at < ?
			ORDER BY captured_at ASC LIMIT 1`,
			c.CycleStart.Format(time.RFC3339Nano),
			endBoundary.Format(time.RFC3339Nano),
		).Scan(&firstSnapshotID)
		if err == nil {
			startRows, err := s.db.Query(
				`SELECT quota_name, utilization FROM codex_quota_values WHERE snapshot_id = ?`,
				firstSnapshotID,
			)
			if err == nil {
				for startRows.Next() {
					var name string
					var util float64
					if startRows.Scan(&name, &util) == nil {
						startValues[name] = util
					}
				}
				startRows.Close()
			}
		}

		qRows, err := s.db.Query(
			`SELECT quota_name, utilization FROM codex_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
			snapshotID,
		)
		if err != nil {
			return nil, fmt.Errorf("store.QueryCodexCycleOverview: quota values: %w", err)
		}
		for qRows.Next() {
			var entry CrossQuotaEntry
			if err := qRows.Scan(&entry.Name, &entry.Percent); err != nil {
				qRows.Close()
				return nil, fmt.Errorf("store.QueryCodexCycleOverview: scan quota: %w", err)
			}
			entry.Value = entry.Percent
			entry.StartPercent = startValues[entry.Name]
			entry.Delta = entry.Percent - entry.StartPercent
			row.CrossQuotas = append(row.CrossQuotas, entry)
		}
		qRows.Close()

		overviewRows = append(overviewRows, row)
	}

	return overviewRows, nil
}

// QueryAllCodexQuotaNames returns all distinct quota names from Codex quota values.
func (s *Store) QueryAllCodexQuotaNames() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT quota_name FROM codex_quota_values ORDER BY quota_name`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query codex quota names: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan codex quota name: %w", err)
		}
		names = append(names, name)
	}

	return names, rows.Err()
}
